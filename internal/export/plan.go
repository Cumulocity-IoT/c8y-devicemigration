package export

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/inventory/managedobjects"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/jsonraw"
)

// Answering "how big is this migration?" without performing it.
//
// checkLimits has always known: it walks the hierarchy, counts every device's
// records and refuses when a limit is breached. But it is reached only from
// Run, it renders a fixed-width table byte-compatible with the bash
// implementation, and it discards the totals whenever nothing is breached - so
// the numbers exist and nobody outside can have them.
//
// This exposes the same walk and the same counting as data, for a caller that
// wants to say "9 objects, 1.2 million measurements" before anything is
// created. It is built on the same Exporter the real pass uses, writing to
// io.Discard: a second copy of the traversal would answer a slightly different
// question within a release or two.

// PlanOptions is the part of an export that decides how large it is.
type PlanOptions struct {
	RootID string

	ChildTypes []ChildType

	// DataTypes selects which record kinds to count. Empty counts none, which
	// matches an export that moves no data.
	DataTypes []DataType

	// Windows is the time range per data type, from ResolveWindows. A plan and
	// the run that follows it must use the same windows or the plan describes a
	// different migration.
	Windows map[DataType]Window
}

// Plan is what an export would move.
type Plan struct {
	// ObjectIDs is every object the export would touch, root first and the rest
	// sorted, so two plans of the same hierarchy read the same.
	ObjectIDs []string

	// Identities is how many external identities those objects carry.
	Identities int

	// Devices holds each object's record counts, for the kinds that were asked
	// for. Present even when nothing is breached, which is the whole point.
	Devices []DeviceCounts

	// Totals is the sum per kind across the hierarchy.
	Totals map[DataType]int

	// Breaches is what would stop an export, in the order checkLimits reports
	// them. Empty means the export would be allowed.
	Breaches []Breach

	// Limits is what the engine enforces, so a caller can show the allowance
	// beside the count instead of hardcoding it and drifting.
	Limits Limits
}

// DeviceCounts is what a plan learned about one object.
//
// Named for the records it started as; it now also carries the two facts that
// decide whether a device could be given a credential, so a caller can predict
// that without walking the hierarchy a second time.
type DeviceCounts struct {
	ObjectID string
	Records  map[DataType]int

	// IsDevice is whether the object carries c8y_IsDevice. A group has no device
	// user, so nothing would be registered for it.
	IsDevice bool

	// IdentityTypes is the type of each external identity the object carries, in
	// the order the platform returned them. Types only: which identity would be
	// chosen is decided from these, and the external ids themselves are of no
	// use to a count and would put device identifiers in an estimate response
	// that does not otherwise carry any.
	IdentityTypes []string
}

// Breach is one limit that would refuse the export.
type Breach struct {
	// Kind is the record kind, or empty when the object count itself is the
	// problem.
	Kind DataType

	// ObjectID is the worst offender, or empty for the object count.
	ObjectID string

	// Devices is how many objects breach this limit.
	Devices int

	Count   int
	Allowed int
}

// Limits is what the engine will refuse.
type Limits struct {
	MaxChildObjects int
	PerDevice       map[DataType]int
}

// EnforcedLimits is the engine's limits as data.
func EnforcedLimits() Limits {
	return Limits{
		MaxChildObjects: MaxChildObjects,
		PerDevice: map[DataType]int{
			DataMeasurements: MaxMeasurementsPerDevice,
			DataEvents:       MaxEventsPerDevice,
			DataAlarms:       MaxAlarmsPerDevice,
		},
	}
}

// MakePlan walks the hierarchy and counts what an export would move.
//
// It writes nothing and creates nothing. The cost is one child listing per
// object per requested child type, one identity listing per object, and one
// count per object per requested data type - all of them the cheapest questions
// the platform answers, but on a wide hierarchy there are a lot of them, so the
// context is honoured throughout.
func MakePlan(ctx context.Context, client *api.Client, opts PlanOptions) (*Plan, error) {
	if err := ValidateRootID(opts.RootID); err != nil {
		return nil, err
	}

	// The traversal and the counting are Exporter methods, and this is the same
	// Exporter with its output thrown away.
	e := New(client, io.Discard, Options{
		RootID:     opts.RootID,
		ChildTypes: opts.ChildTypes,
		DataTypes:  opts.DataTypes,
		Windows:    opts.Windows,
	})

	planned, err := e.collectPlannedIDs(ctx, opts.RootID, map[string]bool{})
	if err != nil {
		return nil, err
	}

	plan := &Plan{
		ObjectIDs: orderedIDs(opts.RootID, planned),
		Totals:    map[DataType]int{},
		Limits:    EnforcedLimits(),
	}

	// Exactly as checkLimits reckons it: the root is not a child of anything, so
	// it does not count against a limit on child objects.
	if childObjects := len(planned) - 1; childObjects > plan.Limits.MaxChildObjects {
		plan.Breaches = append(plan.Breaches, Breach{
			Devices: 1,
			Count:   childObjects,
			Allowed: plan.Limits.MaxChildObjects,
		})
	}

	for _, id := range plan.ObjectIDs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		identityTypes, err := e.identityTypes(ctx, id)
		if err != nil {
			return nil, err
		}
		plan.Identities += len(identityTypes)

		isDevice, err := e.isDevice(ctx, id)
		if err != nil {
			return nil, err
		}

		counts := DeviceCounts{
			ObjectID:      id,
			Records:       map[DataType]int{},
			IsDevice:      isDevice,
			IdentityTypes: identityTypes,
		}
		for _, kind := range opts.DataTypes {
			count, err := e.countRecords(ctx, kind, id)
			if err != nil {
				return nil, err
			}
			counts.Records[kind] = count
			plan.Totals[kind] += count
		}
		plan.Devices = append(plan.Devices, counts)
	}

	plan.Breaches = append(plan.Breaches, recordBreaches(opts.DataTypes, plan)...)

	// The order the table uses, so a caller rendering these reads them the same
	// way the command line prints them.
	sort.SliceStable(plan.Breaches, func(i, j int) bool {
		return breachRank(plan.Breaches[i]) < breachRank(plan.Breaches[j])
	})
	return plan, nil
}

// recordBreaches finds the per-device limits a plan would breach, counting the
// offenders and naming the worst - which is what the report shows.
func recordBreaches(kinds []DataType, plan *Plan) []Breach {
	var breaches []Breach

	for _, kind := range kinds {
		limit := recordLimit(kind)
		worst, worstID, devices := 0, "", 0

		for _, device := range plan.Devices {
			count := device.Records[kind]
			if count <= limit {
				continue
			}
			devices++
			if count > worst {
				worst, worstID = count, device.ObjectID
			}
		}
		if devices > 0 {
			breaches = append(breaches, Breach{
				Kind: kind, ObjectID: worstID,
				Devices: devices, Count: worst, Allowed: limit,
			})
		}
	}
	return breaches
}

// breachRank orders breaches the way the violation table does: the object count
// first, then the record kinds in their own order.
func breachRank(b Breach) int {
	if b.Kind == "" {
		return 1
	}
	return rankOf(b.Kind)
}

// orderedIDs puts the root first and sorts the rest, so a plan of the same
// hierarchy is the same plan twice - a map's iteration order is not.
func orderedIDs(rootID string, planned map[string]bool) []string {
	ids := make([]string, 0, len(planned))
	for id := range planned {
		if id != rootID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return append([]string{rootID}, ids...)
}

// identityTypes is the type of each external identity an object carries.
//
// The same call exportIdentities makes, without the writing. It used to return
// only a count; the types come from the same response and cost nothing extra.
func (e *Exporter) identityTypes(ctx context.Context, id string) ([]string, error) {
	result := e.client.Identity.List(ctx, id)
	if result.Err != nil {
		return nil, fmt.Errorf("cannot list external identities of %s: %w", id, result.Err)
	}

	var types []string
	for item, err := range result.Items() {
		if err != nil {
			return nil, fmt.Errorf("cannot read an external identity of %s: %w", id, err)
		}
		types = append(types, item.Type())
	}
	return types, nil
}

// isDevice is whether an object carries c8y_IsDevice.
//
// One object read per planned object, which is the only new cost this plan
// carries. Paid unconditionally rather than only when credentials are asked for,
// because the alternative is an estimate whose answer changes when a checkbox
// moves - and the number of objects a plan covers is capped, so this is bounded
// by the child-object limit rather than by the size of a tenant.
func (e *Exporter) isDevice(ctx context.Context, id string) (bool, error) {
	result := e.client.ManagedObjects.Get(ctx, id, managedobjects.GetOptions{})
	if result.Err != nil {
		return false, fmt.Errorf("cannot read %s: %w", id, result.Err)
	}
	return jsonraw.Has([]byte(result.Data.Raw()), "c8y_IsDevice"), nil
}
