package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/export"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/registration"
)

// Route is one endpoint. The routes are a table rather than a series of
// registrations so that the OpenAPI document can be checked against them: a
// route nobody documented, or a documented route that does not exist, fails a
// test instead of misleading somebody.
type Route struct {
	Method string

	// Path is in OpenAPI form, which is also ServeMux's form: /migrations/{id}.
	Path string

	// Public marks the routes that answer without credentials. Only the
	// platform's probes and this service's own description qualify.
	Public bool

	handler http.HandlerFunc
}

// Routes is every endpoint this service serves.
//
// Paths are relative to /service/<application name>/ once deployed, so
// POST /preflight is really POST /service/devicemigration/preflight.
func (s *Service) Routes() []Route {
	return []Route{
		// The platform's liveness and readiness probes carry no credentials, so
		// health cannot be behind the authorization check.
		{Method: http.MethodGet, Path: "/health", Public: true, handler: s.ms.HealthHandler},
		{Method: http.MethodGet, Path: "/prometheus", Public: true,
			handler: s.ms.PrometheusHandler().ServeHTTP},
		{Method: http.MethodGet, Path: "/openapi.yaml", Public: true, handler: s.handleOpenAPI},
		{Method: http.MethodGet, Path: "/api-json", Public: true, handler: s.handleOpenAPIJSON},

		{Method: http.MethodGet, Path: "/tenants", handler: s.handleTenants},
		{Method: http.MethodPost, Path: "/preflight", handler: s.handlePreflight},
		{Method: http.MethodPost, Path: "/estimates", handler: s.handleEstimate},

		// Establishing a certificate connection to a target this instance is not
		// subscribed to. See remotetarget.go and docs/platform-behaviour.md.
		{Method: http.MethodPost, Path: "/remote-targets", handler: s.handleCreateRemoteTarget},
		{Method: http.MethodGet, Path: "/remote-targets", handler: s.handleListRemoteTargets},
		{Method: http.MethodDelete, Path: "/remote-targets/{id}",
			handler: s.handleDeleteRemoteTarget},

		// Reads over a tenant the caller has no session in, so that a user
		// interface can offer a hierarchy to choose from.
		{Method: http.MethodGet, Path: "/tenants/{tenant}/inventory",
			handler: s.handleListInventory},
		{Method: http.MethodGet, Path: "/tenants/{tenant}/inventory/{id}",
			handler: s.handleGetInventoryObject},
		{Method: http.MethodGet, Path: "/tenants/{tenant}/inventory/{id}/children",
			handler: s.handleListChildren},

		// The platform's own inventory reads, for one named tenant, so that the
		// platform's own components can browse it. Three routes and no wildcard:
		// the router is the allowlist. See proxy.go.
		{Method: http.MethodGet, Path: "/tenants/{tenant}/platform/inventory/managedObjects",
			handler: s.handleProxyList},
		{Method: http.MethodGet, Path: "/tenants/{tenant}/platform/inventory/managedObjects/{id}",
			handler: s.handleProxyObject},
		{Method: http.MethodGet,
			Path:    "/tenants/{tenant}/platform/inventory/managedObjects/{id}/{relationship}",
			handler: s.handleProxyChildren},

		{Method: http.MethodPost, Path: "/migrations", handler: s.handleCreateMigration},
		{Method: http.MethodGet, Path: "/migrations", handler: s.handleListMigrations},
		{Method: http.MethodGet, Path: "/migrations/{id}", handler: s.handleGetMigration},
		{Method: http.MethodPost, Path: "/migrations/{id}/cancel",
			handler: s.handleCancelMigration},
		// POST, not GET, and the method is the security boundary rather than a
		// matter of taste: this hands the credentials over *once* and destroys
		// them as it serves them, so it changes state. A browser session's cookie
		// travels with any request a page can be talked into making, and the
		// cross-site check deliberately lets reads through - so as a GET, a page
		// an operator visited while signed in could spend the one collection
		// without ever reading the answer, and the only way back is to register
		// the fleet again.
		{Method: http.MethodPost, Path: "/migrations/{id}/credentials",
			handler: s.handleCollectCredentials},
		// Removes this service's record of a run. Not the run: see DeleteJob.
		{Method: http.MethodDelete, Path: "/migrations/{id}", handler: s.handleDeleteJob},

		// The one endpoint a caller from outside the owner tenant may reach: a
		// migrated device asking where to reconnect and who it now is. The whole
		// of that exception is in deviceself.go.
		{Method: http.MethodPost, Path: "/devices/self/migration",
			handler: s.handleDeviceSelfMigration},

		// Moving what a tenant trusts, for a certificate fleet. See
		// trustanchor.go and docs/platform-behaviour.md.
		{Method: http.MethodGet, Path: "/tenants/{tenant}/trusted-certificates",
			handler: s.handleListTrustedCertificates},
		{Method: http.MethodPost, Path: "/certificate-cutovers",
			handler: s.handleTrustAnchorCutover},

		{Method: http.MethodPost, Path: "/reverts", handler: s.handleCreateRevert},
		{Method: http.MethodGet, Path: "/reverts", handler: s.handleListReverts},
		{Method: http.MethodGet, Path: "/reverts/{id}", handler: s.handleGetJob},
		{Method: http.MethodPost, Path: "/reverts/{id}/cancel", handler: s.handleCancelJob},
		{Method: http.MethodDelete, Path: "/reverts/{id}", handler: s.handleDeleteJob},

		// The Spring Boot actuator shape, which is what the platform's own
		// tooling talks to.
		{Method: http.MethodGet, Path: "/loggers", handler: s.handleListLoggers},
		{Method: http.MethodGet, Path: "/loggers/{name}", handler: s.handleGetLogger},
		{Method: http.MethodPost, Path: "/loggers/{name}", handler: s.handleSetLogger},
	}
}

// Handler is the microservice's HTTP surface.
//
// The SDK's RegisterHealthEndpoints is deliberately not used: besides /health
// and /prometheus it adds /env and /logfile, which print the container's
// environment and log to any caller the gateway authenticated - a wider audience
// than everything else here, which is restricted to the owner tenant. Health and
// metrics are registered from the same handlers by hand instead.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, route := range s.Routes() {
		mux.HandleFunc(route.Method+" "+route.Path, route.handler)
	}
	return mux
}

// handleOpenAPI serves this service's own description, so a deployed instance
// can be asked what it accepts rather than the answer living only in a
// repository somebody has to find.
func (s *Service) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "openapi.yaml", buildTime, strings.NewReader(OpenAPI))
}

// handleOpenAPIJSON serves the same description as JSON, at the path the
// manifest's openApiSpec names.
//
// This is what the platform's API documentation application reads, and it reads
// it with the browser's own session - so like the probes, it cannot be behind
// this service's authorization check. Nothing here is secret: it is the same
// document that lives in the repository.
func (s *Service) handleOpenAPIJSON(w http.ResponseWriter, r *http.Request) {
	if OpenAPIJSONError != nil {
		writeError(w, r, fmt.Errorf("the OpenAPI document could not be converted to JSON: %w",
			OpenAPIJSONError))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "api.json", buildTime, bytes.NewReader(OpenAPIJSON))
}

// handleCreateMigration accepts a migration and returns immediately.
//
// 202 rather than 200, because a migration is not done when this returns and
// cannot be: the platform caps a request at fifteen minutes, and a hierarchy
// with data takes longer than that often enough to matter. The Location header
// is where the outcome will appear.
func (s *Service) handleCreateMigration(w http.ResponseWriter, r *http.Request) {
	caller, err := s.Authorize(r.Context(), r)
	if err != nil {
		writeError(w, r, err)
		return
	}

	var request MigrationRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, r, err)
		return
	}

	job, err := s.Enqueue(r.Context(), request, caller)
	if err != nil {
		writeError(w, r, err)
		return
	}

	w.Header().Set("Location", strings.TrimSuffix(r.URL.Path, "/")+"/"+job.ID)
	writeJSON(w, r, http.StatusAccepted, job)
}

// MigrationsResponse is the list of what this service has been asked to do.
type MigrationsResponse struct {
	Migrations []*Job `json:"migrations"`
}

func (s *Service) handleListMigrations(w http.ResponseWriter, r *http.Request) {
	caller, err := s.Authorize(r.Context(), r)
	if err != nil {
		writeError(w, r, err)
		return
	}

	jobs, err := s.OfKind(r.Context(), KindMigration)
	if err != nil {
		writeError(w, r, err)
		return
	}

	// A listing that writes, on purpose: a record left saying RUNNING by a
	// container that no longer exists is settled here, so the screen showing it
	// stops offering a Cancel that cannot work. See SettleStale.
	s.SettleStale(r.Context(), jobs)
	hideOthersCredentials(caller, jobs...)
	writeJSON(w, r, http.StatusOK, MigrationsResponse{Migrations: jobs})
}

// RevertsResponse is what this service has been asked to undo.
type RevertsResponse struct {
	Reverts []*Job `json:"reverts"`
}

// handleCreateRevert undoes what a migration created.
//
// The one destructive thing here, and the only one that needs no more than a run
// id: every object a migration created carries a marker fragment naming the run,
// so the engine finds them by asking the target tenant. Nothing has to have
// survived on disk, which is what makes this possible at all from a container
// that has been replaced since.
func (s *Service) handleCreateRevert(w http.ResponseWriter, r *http.Request) {
	caller, err := s.Authorize(r.Context(), r)
	if err != nil {
		writeError(w, r, err)
		return
	}

	var request RevertRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, r, err)
		return
	}

	job, err := s.EnqueueRevert(r.Context(), request, caller)
	if err != nil {
		writeError(w, r, err)
		return
	}

	w.Header().Set("Location", strings.TrimSuffix(r.URL.Path, "/")+"/"+job.ID)
	writeJSON(w, r, http.StatusAccepted, job)
}

func (s *Service) handleListReverts(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Authorize(r.Context(), r); err != nil {
		writeError(w, r, err)
		return
	}

	jobs, err := s.OfKind(r.Context(), KindRevert)
	if err != nil {
		writeError(w, r, err)
		return
	}

	// As for migrations: a revert is a job, and one left running by a replaced
	// container is the case that brought this about.
	s.SettleStale(r.Context(), jobs)
	writeJSON(w, r, http.StatusOK, RevertsResponse{Reverts: jobs})
}

func (s *Service) handleGetMigration(w http.ResponseWriter, r *http.Request) {
	caller, err := s.Authorize(r.Context(), r)
	if err != nil {
		writeError(w, r, err)
		return
	}

	// Read from the running job when there is one, because its record is only
	// as fresh as the last progress write, and from the tenant otherwise.
	if running := s.Running(); running != nil && running.ID == r.PathValue("id") {
		hideOthersCredentials(caller, running)
		writeJSON(w, r, http.StatusOK, running)
		return
	}

	job, err := s.Record(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	hideOthersCredentials(caller, job)
	writeJSON(w, r, http.StatusOK, job)
}

// hideOthersCredentials clears the collection offer on jobs this caller may not
// collect.
//
// The flag drives a download action on a screen, so leaving it set for everybody
// would put a button in front of readers who can only be refused by it - and
// worse, would read as "somebody has not collected these yet" to a person who
// cannot do anything about that. The same rule decides both, in collectableBy,
// so the offer and the answer cannot disagree.
func hideOthersCredentials(caller *Caller, jobs ...*Job) {
	for _, job := range jobs {
		if job != nil && collectableBy(job, caller) != nil {
			job.CredentialsAvailable = false
		}
	}
}

// handleCollectCredentials hands over the device credentials a migration issued,
// exactly once, and only to whoever started it.
//
// Served as text/csv rather than JSON, because what the caller wants is the file
// - the same columns the command line writes - and a browser download is the
// point. No-store, because a list of working device passwords must not sit in a
// proxy or a browser cache after the one collection that was allowed.
func (s *Service) handleCollectCredentials(w http.ResponseWriter, r *http.Request) {
	caller, err := s.Authorize(r.Context(), r)
	if err != nil {
		writeError(w, r, err)
		return
	}

	jobID := r.PathValue("id")

	// The job has to exist and belong to this tenant before its credentials are
	// discussed, so an unknown id is a 404 rather than a Gone that hints at what
	// might once have been there.
	job, err := s.Record(r.Context(), jobID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := collectableBy(job, caller); err != nil {
		writeError(w, r, err)
		return
	}

	body, err := s.credentials.take(jobID)
	if err != nil {
		writeError(w, r, &GoneError{Reason: "the credentials for this migration are not " +
			"available: they are handed over once, and a restart of this service loses them. " +
			"Re-register the devices to issue new ones, which rotates their passwords"})
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", "credentials-"+jobID+".csv"))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// collectableBy says whether this caller may collect this migration's
// credentials, and why not when they may not.
//
// Holding ROLE_DEVICE_MIGRATION_ADMIN on the owner tenant is what lets somebody
// *start* a migration; it is deliberately not enough to take the passwords of
// one they did not. The list is handed over once, so a colleague collecting it
// first is not a lesser version of reading it - it is the operator who asked for
// it losing it, with no way to get it back that does not rotate the whole fleet.
//
// A record that does not say who started it is refused rather than opened up.
// Nobody can prove they are the caller it does not name, and "the record has
// forgotten" is not a reason to hand a fleet's passwords to the next person who
// asks.
func collectableBy(job *Job, caller *Caller) error {
	if caller == nil {
		return &ForbiddenError{Reason: "the credentials of a migration are handed only to " +
			"the caller who started it, and this request has no caller"}
	}
	if job.RequestedBy == "" {
		return &ForbiddenError{Reason: "this migration's record does not say who started it, " +
			"so its credentials cannot be handed to anyone. Re-register the devices to issue " +
			"new ones, which rotates their passwords"}
	}
	if job.RequestedBy != caller.Name() {
		return &ForbiddenError{Reason: fmt.Sprintf(
			"the credentials of a migration are handed only to the caller who started it, "+
				"and this one was started by %s. They are handed over once, so taking them "+
				"on somebody else's behalf would leave them without any", job.RequestedBy)}
	}
	return nil
}

// handleGetJob and handleCancelJob serve reverts. A job is a job once it exists,
// so these are the migration handlers under names that do not claim otherwise.
func (s *Service) handleGetJob(w http.ResponseWriter, r *http.Request) {
	s.handleGetMigration(w, r)
}

func (s *Service) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	s.handleCancelMigration(w, r)
}

func (s *Service) handleCancelMigration(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Authorize(r.Context(), r); err != nil {
		writeError(w, r, err)
		return
	}

	if err := s.Cancel(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, r, err)
		return
	}

	// 202: the cancellation has been asked for. The job stops at its next API
	// call, and its record says what it managed to do.
	writeJSON(w, r, http.StatusAccepted, map[string]string{
		"id":      r.PathValue("id"),
		"message": "cancelling; the job record will say what reached the target tenant",
	})
}

// DeletionResponse names the jobs whose records were removed.
//
// A list rather than a count, and the same shape whichever kind was asked for:
// deleting a migration takes its undos with it, and a caller that has just been
// told "3" has to go and read the listing to learn which three.
type DeletionResponse struct {
	Deleted []string `json:"deleted"`
}

// handleDeleteJob forgets a job, and the undos that belong to it.
//
// One handler behind both /migrations/{id} and /reverts/{id}, because a record
// is a record: the kind decides whether there are undos to take along, and the
// record itself says which kind it is. Registered under both paths so that the
// delete sits where the thing being deleted is read.
func (s *Service) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Authorize(r.Context(), r); err != nil {
		writeError(w, r, err)
		return
	}

	deleted, err := s.DeleteJob(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}

	// 200 with what went, not 204: the cascade means the caller asked about one
	// job and may have removed three, and a screen that reloads afterwards
	// should be able to say so.
	writeJSON(w, r, http.StatusOK, DeletionResponse{Deleted: deleted})
}

// TenantsResponse says which tenants this microservice can act on.
type TenantsResponse struct {
	OwnerTenant string              `json:"ownerTenant"`
	Tenants     []TenantDescription `json:"tenants"`
}

// TenantDescription is a tenant as a person would recognise it.
//
// An id like t11106993 identifies a tenant and describes nothing; the name and
// the domain are what somebody choosing between two of them actually reads. Both
// come from the tenant's own /tenant/currentTenant, read with its service user -
// so a tenant that cannot be reached still appears, with its id and nothing else,
// rather than disappearing from the list.
type TenantDescription struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Domain string `json:"domain,omitempty"`
}

func (s *Service) handleTenants(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Authorize(r.Context(), r); err != nil {
		writeError(w, r, err)
		return
	}

	writeJSON(w, r, http.StatusOK, TenantsResponse{
		OwnerTenant: s.ownerTenant,
		Tenants:     s.TenantDescriptions(r.Context()),
	})
}

// PreflightRequest names the two tenants a migration would run between.
type PreflightRequest struct {
	SourceTenant string `json:"sourceTenant"`
	TargetTenant string `json:"targetTenant,omitempty"`

	// RemoteTargetID checks a remote target instead of a subscribed one. Exactly
	// one of TargetTenant and RemoteTargetID is given.
	RemoteTargetID string `json:"remoteTargetId,omitempty"`

	// RootID is the hierarchy the migration would move, and it is needed only by
	// the credential check below - the tenant checks are about the two tenants
	// and nothing else.
	RootID string `json:"rootId,omitempty"`

	// ChildTypes and NoChildren are the migration's own, and they belong here for
	// the same reason RootID does: which relationships are followed decides which
	// devices the migration covers, so a check that assumed the default would
	// answer about a different set of devices from the one being migrated - in
	// either direction, since the default neither includes everything nor
	// excludes nothing.
	ChildTypes string `json:"childTypes,omitempty"`
	NoChildren bool   `json:"noChildren,omitempty"`

	// ProvisionCredentials asks the one question that depends on what is being
	// moved: whether a device user can be created for each of these devices in
	// this target. It costs a hierarchy walk and a lookup per device, so it is
	// asked for rather than always done.
	//
	// Requires RootID. A caller that set this and got an answer without naming a
	// hierarchy would believe a check had passed that was never run.
	ProvisionCredentials bool `json:"provisionCredentials,omitempty"`
}

// PreflightResponse reports what the platform said about each side, so a
// misconfigured subscription is visible before anything is created.
type PreflightResponse struct {
	Host   string        `json:"host"`
	Source PreflightSide `json:"source"`
	Target PreflightSide `json:"target"`

	// CredentialsRefused is true when this migration cannot provision
	// credentials. Said plainly because it is what a caller branches on, the way
	// an estimate's withinLimits is - and false by default, so a caller that did
	// not ask the question is not told the answer is no.
	CredentialsRefused bool `json:"credentialsRefused,omitempty"`

	// Conflicts is why, one entry per device. Reported as data rather than as a
	// refusal of the whole pre-flight because the two tenants did check out: it
	// is one migration option that is unavailable, and a caller that drops it can
	// go ahead.
	Conflicts []PreflightConflict `json:"conflicts,omitempty"`
}

// PreflightConflict is one device whose external identity the target tenant
// already holds.
//
// Enough to find it on either side and no more. The external id is here because
// it is what somebody has to compare in the two tenants to decide what the
// duplicate means; it is the same identifier the inventory already shows them.
type PreflightConflict struct {
	SourceID     string `json:"sourceId"`
	Name         string `json:"name,omitempty"`
	ExternalID   string `json:"externalId"`
	ExternalType string `json:"externalType"`
	TargetID     string `json:"targetId,omitempty"`
}

// PreflightSide is one tenant: the tenant asked for, and the tenant the platform
// answered with when the service user authenticated.
type PreflightSide struct {
	Tenant         string `json:"tenant"`
	ReportedTenant string `json:"reportedTenant"`
	ServiceUser    string `json:"serviceUser"`
}

func (s *Service) handlePreflight(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Authorize(r.Context(), r); err != nil {
		writeError(w, r, err)
		return
	}

	var request PreflightRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, r, err)
		return
	}

	response, err := s.Preflight(r.Context(), request)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, response)
}

// Preflight resolves both tenants, refuses a migration into the tenant it came
// from, and proves both service users against the platform.
//
// The same-tenant refusal has to live here: migrate deliberately does not check
// it, because the CLI checks it in its own pre-flight before calling migrate.
// Without this, a source and target naming one tenant would duplicate a
// hierarchy inside itself.
//
// Comparing the two tenants is enough on its own here, unlike in the CLI where
// two session files can name the same tenant on different hosts: both sides come
// from this application's subscription list, so they are ids in one namespace on
// one platform. Every migration passes through here, so nothing downstream
// repeats the check.
func (s *Service) Preflight(ctx context.Context, request PreflightRequest) (*PreflightResponse, error) {
	if request.SourceTenant == "" {
		return nil, &BadRequestError{Reason: "sourceTenant is required"}
	}
	if (request.TargetTenant == "") == (request.RemoteTargetID == "") {
		return nil, &BadRequestError{Reason: "give exactly one of targetTenant or remoteTargetId"}
	}

	source, err := s.tenant(request.SourceTenant)
	if err != nil {
		return nil, err
	}
	target, err := s.targetClient(request.RemoteTargetID, request.TargetTenant)
	if err != nil {
		return nil, err
	}

	if source.session.SameTenantAs(target.session) {
		return nil, &BadRequestError{Reason: fmt.Sprintf(
			"source and target are the same tenant (%s), which would duplicate the "+
				"hierarchy inside itself", source.session.Tenant)}
	}

	sourceReported, err := probe(ctx, source)
	if err != nil {
		return nil, err
	}
	targetReported, err := probe(ctx, target)
	if err != nil {
		return nil, err
	}

	response := &PreflightResponse{
		Host: s.baseURL,
		Source: PreflightSide{
			Tenant:         source.session.Tenant,
			ReportedTenant: sourceReported,
			ServiceUser:    source.session.Username,
		},
		Target: PreflightSide{
			Tenant:         target.session.Tenant,
			ReportedTenant: targetReported,
			ServiceUser:    target.session.Username,
		},
	}

	if request.ProvisionCredentials {
		conflicts, err := s.credentialConflicts(ctx, request, source, target)
		if err != nil {
			return nil, err
		}
		response.Conflicts = conflicts
		response.CredentialsRefused = len(conflicts) > 0
	}
	return response, nil
}

// credentialConflicts reports the devices a credential-provisioning migration
// would be refused over.
//
// The same two rules the run applies, reached through the same functions: which
// devices would be registered is registration's, and whether the target already
// holds an identity is registration's. A second copy of either here would be a
// pre-flight that passes and a migration that then fails, which is worse than no
// pre-flight at all.
func (s *Service) credentialConflicts(ctx context.Context, request PreflightRequest,
	source, target *tenantClient) ([]PreflightConflict, error) {

	if request.RootID == "" {
		return nil, &BadRequestError{Reason: "rootId is required to check whether credentials " +
			"can be provisioned: the check is about the devices being migrated, not about " +
			"the two tenants"}
	}
	if request.RemoteTargetID != "" {
		// The same refusal Enqueue gives, here rather than after a hierarchy
		// walk that could not change the answer.
		return nil, &BadRequestError{Reason: "credentials cannot be provisioned into a remote " +
			"target, so there is nothing to check. Migrate with certificateFleet, or " +
			"subscribe the target to this service"}
	}

	// The migration's own scope, resolved its own way - the same defaults, the
	// same refusals - so the devices checked here are the devices that would be
	// registered. No data types: which records would move has no bearing on which
	// devices get a credential, and counting them would make this as slow as an
	// estimate for an answer it does not use.
	options, err := s.planOptions(EstimateRequest{
		SourceTenant: request.SourceTenant,
		RootID:       request.RootID,
		ChildTypes:   request.ChildTypes,
		NoChildren:   request.NoChildren,
	})
	if err != nil {
		return nil, err
	}

	plan, err := export.MakePlan(ctx, source.client, options)
	if err != nil {
		return nil, &BadRequestError{Reason: err.Error()}
	}

	conflicts, err := registration.FindConflicts(ctx, target.client, registrableRecords(plan))
	if err != nil {
		return nil, err
	}

	reported := make([]PreflightConflict, 0, len(conflicts))
	for _, conflict := range conflicts {
		reported = append(reported, PreflightConflict{
			SourceID:     conflict.SourceID,
			Name:         conflict.Name,
			ExternalID:   conflict.ExternalID,
			ExternalType: conflict.ExternalType,
			TargetID:     conflict.TargetID,
		})
	}
	return reported, nil
}

// BadRequestError is a caller mistake: a missing field, or two tenants that are
// one tenant.
type BadRequestError struct {
	Reason string
}

func (e *BadRequestError) Error() string { return e.Reason }

// RateLimitedError means the caller is asking faster than anything needs to.
//
// Its own type rather than QueueFullError, which also answers 429: that one says
// this service is busy with somebody else's migration, and a device told that
// would wait for a queue it is not in. This one says the caller itself is the
// reason, and the answer would not have changed anyway.
type RateLimitedError struct {
	Reason string
}

func (e *RateLimitedError) Error() string { return e.Reason }

// GoneError means the thing existed and deliberately does not any more.
//
// Distinct from NotFoundError, and the distinction is the whole point for
// credentials: "there is no such migration" and "its credentials have already
// been collected" need different answers, or a caller who missed the download
// cannot tell a mistake from a race with a colleague.
type GoneError struct {
	Reason string
}

func (e *GoneError) Error() string { return e.Reason }

// --- plumbing ---------------------------------------------------------------

func decodeJSON(r *http.Request, into any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return &BadRequestError{Reason: fmt.Sprintf("could not read the request body: %v", err)}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, r *http.Request, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status line is already out, so this can only be logged.
		slog.WarnContext(r.Context(), "could not write the response", "error", err)
	}
}

// errorBody matches the shape the platform uses for its own errors, so a client
// that already understands Cumulocity errors understands these too.
type errorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, kind := http.StatusInternalServerError, "server/internalError"

	var forbidden *ForbiddenError
	var unsubscribed *UnsubscribedTenantError
	var bad *BadRequestError
	var notFound *NotFoundError
	var conflict *ConflictError
	var full *QueueFullError
	var gone *GoneError
	var limited *RateLimitedError

	switch {
	case errors.Is(err, ErrUnauthenticated):
		status, kind = http.StatusUnauthorized, "security/Unauthorized"
	case errors.As(err, &forbidden):
		status, kind = http.StatusForbidden, "security/Forbidden"
	case errors.As(err, &unsubscribed):
		status, kind = http.StatusBadRequest, "devicemigration/tenantNotSubscribed"
	case errors.As(err, &bad):
		status, kind = http.StatusBadRequest, "devicemigration/badRequest"
	case errors.As(err, &notFound):
		status, kind = http.StatusNotFound, "devicemigration/notFound"
	case errors.As(err, &gone):
		status, kind = http.StatusGone, "devicemigration/credentialsGone"
	case errors.As(err, &conflict):
		status, kind = http.StatusConflict, "devicemigration/conflict"
	case errors.As(err, &full):
		status, kind = http.StatusTooManyRequests, "devicemigration/busy"
	case errors.As(err, &limited):
		status, kind = http.StatusTooManyRequests, "devicemigration/tooManyRequests"
	}

	if status == http.StatusInternalServerError {
		// The message still goes to the caller, but the log is where the detail
		// of an unexpected failure belongs.
		slog.ErrorContext(r.Context(), "request failed", "path", r.URL.Path, "error", err)
	}

	writeJSON(w, r, status, errorBody{Error: kind, Message: err.Error()})
}
