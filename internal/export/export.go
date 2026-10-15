package export

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/inventory/managedobjects"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/inventory/managedobjects/child"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/inventory/managedobjects/childadditions"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/inventory/managedobjects/childassets"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/inventory/managedobjects/childdevices"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/pagination"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/jsonmodels"
)

// Exporter walks a hierarchy and writes it out.
type Exporter struct {
	client *api.Client
	stderr io.Writer
	opts   Options

	artefacts artefacts

	// visited during the real pass. A device reachable through two parents is
	// exported once, and a cycle terminates.
	visited map[string]bool

	objectCount   int
	identityCount int

	measurementCount int
	eventCount       int
	alarmCount       int
	binaryCount      int
}

func New(client *api.Client, stderr io.Writer, opts Options) *Exporter {
	return &Exporter{
		client:  client,
		stderr:  stderr,
		opts:    opts,
		visited: map[string]bool{},
	}
}

// Run checks the limits, then writes the hierarchy.
func (e *Exporter) Run(ctx context.Context) error {
	// Before the limits and before any request: the id ends up in every file
	// name this writes, so an unsafe one has to be refused rather than
	// discovered when the first write fails.
	if err := ValidateRootID(e.opts.RootID); err != nil {
		return err
	}

	if e.opts.IgnoreLimits {
		e.warnIgnoringLimits()
	}

	// Before anything is written, so a refusal leaves nothing behind.
	if err := e.checkLimits(ctx); err != nil {
		return err
	}

	dir := e.opts.OutputDirectory
	if _, statErr := os.Stat(dir); os.IsNotExist(statErr) {
		// remembered so a refused export can remove a directory it created,
		// while leaving a pre-existing one in place
		e.artefacts.createdOutputDirectory = dir
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	if err := e.exportRecursive(ctx, e.opts.RootID, dir); err != nil {
		e.artefacts.revert()
		return err
	}

	e.reportTotals(dir)
	return nil
}

// exportRecursive writes one device and then descends.
//
// The order is deliberate: object, identities, then all child index files for
// all requested child types, then the progress line, and only then the
// children. That way a device's line is complete before its children start
// reporting, so the output reads top down.
func (e *Exporter) exportRecursive(ctx context.Context, id, dir string) error {
	if e.visited[id] {
		return nil
	}
	e.visited[id] = true

	object, err := e.getObject(ctx, id)
	if err != nil {
		return err
	}

	path := objectPath(dir, id)
	if err := writeRaw(path, object.Bytes()); err != nil {
		return err
	}
	e.artefacts.record(path)

	name := displayName(object.Name())

	identities := 0
	if !e.opts.DataOnly {
		identities, err = e.exportIdentities(ctx, id, dir)
		if err != nil {
			return err
		}
	}

	var summary strings.Builder
	var pending []string

	for _, childType := range e.opts.ChildTypes {
		children, err := e.listChildren(ctx, id, childType)
		if err != nil {
			return err
		}
		if len(children) == 0 {
			continue
		}

		indexPath := childIndexPath(dir, id, childType)
		if err := writeJSONIndented(indexPath, children); err != nil {
			return err
		}
		e.artefacts.record(indexPath)

		fmt.Fprintf(&summary, ", %d %s %s",
			len(children), childType, plural(len(children), "child", "children"))
		pending = append(pending, children...)
	}

	if identities > 0 {
		fmt.Fprintf(&summary, ", %d %s", identities, plural(identities, "identity", "identities"))
	}

	if !e.opts.MetadataOnly {
		dataSummary, dataErr := e.exportDeviceData(ctx, id, dir)
		if dataErr != nil {
			return dataErr
		}
		summary.WriteString(dataSummary)
	}

	e.objectCount++
	fmt.Fprintf(e.stderr, "Exported %s (%s)%s\n", name, id, summary.String())

	for _, childID := range pending {
		if err := e.exportRecursive(ctx, childID, dir); err != nil {
			return err
		}
	}
	return nil
}

func (e *Exporter) getObject(ctx context.Context, id string) (jsonmodels.ManagedObject, error) {
	result := e.client.ManagedObjects.Get(ctx, id, managedobjects.GetOptions{})
	if result.Err != nil {
		return jsonmodels.ManagedObject{}, fmt.Errorf("cannot read managed object %s: %w", id, result.Err)
	}
	return result.Data, nil
}

// exportIdentities writes the identity file and returns how many it held. The
// file is written only when the device actually has identities, so its presence
// means something.
func (e *Exporter) exportIdentities(ctx context.Context, id, dir string) (int, error) {
	result := e.client.Identity.List(ctx, id)
	if result.Err != nil {
		return 0, fmt.Errorf("cannot list external identities of %s: %w", id, result.Err)
	}

	var identities []Identity
	for item, err := range result.Items() {
		if err != nil {
			return 0, fmt.Errorf("cannot read an external identity of %s: %w", id, err)
		}
		identities = append(identities, Identity{Type: item.Type(), ExternalID: item.ExternalID()})
	}
	if len(identities) == 0 {
		return 0, nil
	}

	path := identitiesPath(dir, id)
	if err := writeJSONIndented(path, identities); err != nil {
		return 0, err
	}
	e.artefacts.record(path)
	e.identityCount += len(identities)
	return len(identities), nil
}

// listChildren returns the ids of one relationship kind, following every page.
//
// The three kinds are separate endpoints with separate services, but they share
// one iterator type, so only the call itself differs.
func (e *Exporter) listChildren(ctx context.Context, id string, childType ChildType) ([]string, error) {
	opts := child.ListOptions{
		PaginationOptions: pagination.PaginationOptions{PageSize: 2000},
	}

	var iterator *pagination.Iterator[jsonmodels.ManagedObject]
	switch childType {
	case ChildAsset:
		iterator = e.client.ManagedObjects.ChildAssets.ListAll(ctx, id, childassets.ListOptions(opts))
	case ChildDevice:
		iterator = e.client.ManagedObjects.ChildDevices.ListAll(ctx, id, childdevices.ListOptions(opts))
	case ChildAddition:
		iterator = e.client.ManagedObjects.ChildAdditions.ListAll(ctx, id, childadditions.ListOptions(opts))
	default:
		return nil, fmt.Errorf("unknown child type %q", childType)
	}

	var ids []string
	for c, err := range iterator.Items() {
		if err != nil {
			return nil, fmt.Errorf("cannot list %s children of %s: %w", childType, id, err)
		}
		ids = append(ids, c.ID())
	}
	if err := iterator.Err(); err != nil {
		return nil, fmt.Errorf("cannot list %s children of %s: %w", childType, id, err)
	}
	return ids, nil
}

func (e *Exporter) reportTotals(destination string) {
	if e.opts.DataOnly {
		// A single device's totals can only restate the per-device line above,
		// so they are left out. Across several devices they still add something.
		if e.objectCount <= 1 {
			return
		}
		line := fmt.Sprintf("Exported %d %s, %d %s and %d %s",
			e.measurementCount, plural(e.measurementCount, "measurement", "measurements"),
			e.eventCount, plural(e.eventCount, "event", "events"),
			e.alarmCount, plural(e.alarmCount, "alarm", "alarms"))
		if e.binaryCount > 0 {
			line += fmt.Sprintf(" including %d %s",
				e.binaryCount, plural(e.binaryCount, "attachment", "attachments"))
		}
		fmt.Fprintf(e.stderr, "%s to %s\n", line, destination)
		return
	}

	line := fmt.Sprintf("Exported %d %s and %d %s",
		e.objectCount, plural(e.objectCount, "managed object", "managed objects"),
		e.identityCount, plural(e.identityCount, "external identity", "external identities"))

	if len(e.opts.DataTypes) > 0 && !e.opts.MetadataOnly {
		line += fmt.Sprintf(", with %d %s, %d %s and %d %s",
			e.measurementCount, plural(e.measurementCount, "measurement", "measurements"),
			e.eventCount, plural(e.eventCount, "event", "events"),
			e.alarmCount, plural(e.alarmCount, "alarm", "alarms"))
		if e.binaryCount > 0 {
			line += fmt.Sprintf(" including %d %s",
				e.binaryCount, plural(e.binaryCount, "attachment", "attachments"))
		}
	}

	fmt.Fprintf(e.stderr, "%s to %s\n", line, destination)
}
