package service

import (
	"context"
	"net/http"
	"time"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/export"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/registration"
)

// How big is this migration, before anybody commits to it.
//
// The engine has always known - it walks the hierarchy and counts every device's
// records before writing anything, and refuses when a limit is breached. What it
// did with the answer was render a table and throw the totals away. This returns
// them, so a caller can say "9 objects and 1.2 million measurements, about 25
// minutes" while somebody is still deciding.

// EstimateRequest is a migration that has not been asked for yet. The fields are
// the migration's own, so a caller estimates exactly what it will then start.
type EstimateRequest struct {
	SourceTenant string `json:"sourceTenant"`
	RootID       string `json:"rootId"`

	ChildTypes string `json:"childTypes,omitempty"`
	NoChildren bool   `json:"noChildren,omitempty"`

	DataTypes string `json:"dataTypes,omitempty"`
	DateFrom  string `json:"dateFrom,omitempty"`
	DateTo    string `json:"dateTo,omitempty"`
}

// EstimateResponse is what that migration would move.
type EstimateResponse struct {
	Tenant string `json:"tenant"`
	RootID string `json:"rootId"`

	Objects    int `json:"objects"`
	Identities int `json:"identities"`

	// Credentials is how many of those objects would be given a device user if
	// the migration asked for credentials.
	//
	// Fewer than Objects, always: groups have no device user, and a device whose
	// identities leave no unambiguous choice is skipped rather than guessed at.
	// Decided by registration.Registrable, the same rule the run applies, so the
	// number in the dialogue cannot disagree with what happens.
	Credentials int `json:"credentials"`

	// Records is the total per kind, for the kinds that were asked for.
	Records map[string]int `json:"records,omitempty"`

	// Busiest is the highest single-device count per kind, because the limits are
	// per device: a hierarchy well under the total can still be refused.
	Busiest map[string]int `json:"busiest,omitempty"`

	// Window is the range each kind was counted over, resolved the way a run
	// would resolve it - including the configured default.
	Window map[string]EstimateWindow `json:"window,omitempty"`

	// Breaches is what would stop this migration. Empty means it would be
	// allowed.
	Breaches []EstimateBreach `json:"breaches"`

	// WithinLimits is the same thing as an empty Breaches, said plainly, because
	// it is what a caller branches on.
	WithinLimits bool `json:"withinLimits"`

	// Limits is what the engine enforces, so nobody has to hardcode it.
	Limits EstimateLimits `json:"limits"`
}

// EstimateWindow is one kind's time range.
type EstimateWindow struct {
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// EstimateBreach is one limit this migration would breach.
type EstimateBreach struct {
	// Kind is the record kind, or "objects" when the hierarchy itself is too
	// large.
	Kind string `json:"kind"`

	// ObjectID is the worst offender, absent for the object count.
	ObjectID string `json:"objectId,omitempty"`

	// Objects is how many objects breach this limit.
	Objects int `json:"objects"`

	Count   int `json:"count"`
	Allowed int `json:"allowed"`
}

// EstimateLimits is what the engine will refuse.
type EstimateLimits struct {
	MaxChildObjects int            `json:"maxChildObjects"`
	PerDevice       map[string]int `json:"perDevice"`
}

func (s *Service) handleEstimate(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Authorize(r.Context(), r); err != nil {
		writeError(w, r, err)
		return
	}

	var request EstimateRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, r, err)
		return
	}

	response, err := s.Estimate(r.Context(), request)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, response)
}

// Estimate counts what a migration would move.
//
// Synchronous, unlike a migration: the whole thing is a hierarchy walk and a
// count per device per kind, which is seconds even on the widest hierarchy the
// limits allow - and well inside the fifteen minutes the platform gives a
// request. It creates nothing, so a caller may abandon it, which is why the
// request's context is passed straight through.
func (s *Service) Estimate(ctx context.Context, request EstimateRequest) (*EstimateResponse, error) {
	if request.SourceTenant == "" {
		return nil, &BadRequestError{Reason: "sourceTenant is required"}
	}

	options, err := s.planOptions(request)
	if err != nil {
		return nil, err
	}

	tenant, err := s.tenant(request.SourceTenant)
	if err != nil {
		return nil, err
	}

	plan, err := export.MakePlan(ctx, tenant.client, options)
	if err != nil {
		// The engine's refusals are the caller's mistakes - a root id that is a
		// path, an id that does not exist - so they are reported as such rather
		// than as a failure of this service.
		return nil, &BadRequestError{Reason: err.Error()}
	}

	return estimateFrom(tenant.session.Tenant, options, plan), nil
}

// planOptions resolves a request the same way a migration resolves its own, so
// an estimate describes the run that follows it and not a different one.
func (s *Service) planOptions(request EstimateRequest) (export.PlanOptions, error) {
	shaped := MigrationRequest{
		SourceTenant: request.SourceTenant,
		// A target is not needed to count what would leave the source, but
		// resolveOptions insists on the fields it validates, so this stands in
		// for one.
		TargetTenant: request.SourceTenant,
		RootID:       request.RootID,
		ChildTypes:   request.ChildTypes,
		NoChildren:   request.NoChildren,
		DataTypes:    request.DataTypes,
		DateFrom:     request.DateFrom,
		DateTo:       request.DateTo,
	}
	if shaped.DateFrom == "" {
		// The same default a run would apply, or the estimate promises a
		// different window from the one that gets migrated.
		shaped.DateFrom = s.Settings().DefaultDateFrom
	}

	// Reusing the migration's own resolution means one definition of what
	// "all" means, what the default child types are, and which combinations are
	// refused.
	migrateOptions, err := s.resolveOptions(shaped, "")
	if err != nil {
		return export.PlanOptions{}, err
	}

	return export.PlanOptions{
		RootID:     migrateOptions.RootID,
		ChildTypes: migrateOptions.ChildTypes,
		DataTypes:  migrateOptions.DataTypes,
		Windows:    migrateOptions.Windows,
	}, nil
}

// estimateFrom renders a plan as the API's answer.
func estimateFrom(tenant string, options export.PlanOptions, plan *export.Plan) *EstimateResponse {
	response := &EstimateResponse{
		Tenant:       tenant,
		RootID:       options.RootID,
		Objects:      len(plan.ObjectIDs),
		Identities:   plan.Identities,
		Credentials:  registrableDevices(plan),
		Breaches:     []EstimateBreach{},
		WithinLimits: len(plan.Breaches) == 0,
		Limits: EstimateLimits{
			MaxChildObjects: plan.Limits.MaxChildObjects,
			PerDevice:       map[string]int{},
		},
	}
	for kind, limit := range plan.Limits.PerDevice {
		response.Limits.PerDevice[string(kind)] = limit
	}

	if len(options.DataTypes) > 0 {
		response.Records = map[string]int{}
		response.Busiest = map[string]int{}
		response.Window = map[string]EstimateWindow{}

		for _, kind := range options.DataTypes {
			response.Records[string(kind)] = plan.Totals[kind]

			busiest := 0
			for _, device := range plan.Devices {
				if count := device.Records[kind]; count > busiest {
					busiest = count
				}
			}
			response.Busiest[string(kind)] = busiest

			window := options.Windows[kind]
			response.Window[string(kind)] = EstimateWindow{
				From: formatWindow(window.From),
				To:   formatWindow(window.To),
			}
		}
	}

	for _, breach := range plan.Breaches {
		kind := string(breach.Kind)
		if kind == "" {
			kind = "objects"
		}
		response.Breaches = append(response.Breaches, EstimateBreach{
			Kind:     kind,
			ObjectID: breach.ObjectID,
			Objects:  breach.Devices,
			Count:    breach.Count,
			Allowed:  breach.Allowed,
		})
	}
	return response
}

// registrableDevices is how many of a plan's objects would get a device user.
func registrableDevices(plan *export.Plan) int {
	return len(registrableRecords(plan))
}

// registrableRecords is the devices of a plan that would be registered, shaped
// as the rows the registration phase would build for them.
//
// One function for two questions - how many devices would get a credential, and
// which identities a target would have to be checked for - because they are the
// same question and an estimate that disagreed with a pre-flight would be worse
// than either on its own.
//
// The default identity type, because that is what a migration through this
// service uses: only the command line exposes a choice, and predicting a run
// nobody asked for would be a count and a refusal about a different migration.
// The rows carry no password and no name beyond the object's own - they exist to
// be looked up, not to be uploaded.
func registrableRecords(plan *export.Plan) []registration.Record {
	var records []registration.Record

	for _, device := range plan.Devices {
		types := device.IdentityTypes()
		if !registration.Registrable(device.IsDevice, types, registration.DefaultIdentityType) {
			continue
		}
		// Registrable has just established that this picks one, so the index is
		// in range. Both come from registration so the choice cannot drift from
		// the one the run makes.
		chosen := device.Identities[registration.SelectIdentity(types, registration.DefaultIdentityType)]
		records = append(records, registration.Record{
			SourceID:     device.ObjectID,
			Name:         device.Name,
			ExternalID:   chosen.ExternalID,
			ExternalType: chosen.Type,
		})
	}
	return records
}

// formatWindow renders a bound, or nothing when there is none - an absent bound
// and the zero time are different statements.
func formatWindow(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
