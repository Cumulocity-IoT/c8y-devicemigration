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

		{Method: http.MethodPost, Path: "/migrations", handler: s.handleCreateMigration},
		{Method: http.MethodGet, Path: "/migrations", handler: s.handleListMigrations},
		{Method: http.MethodGet, Path: "/migrations/{id}", handler: s.handleGetMigration},
		{Method: http.MethodPost, Path: "/migrations/{id}/cancel",
			handler: s.handleCancelMigration},
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
	if _, err := s.Authorize(r.Context(), r); err != nil {
		writeError(w, r, err)
		return
	}

	jobs, err := s.Records(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, MigrationsResponse{Migrations: jobs})
}

func (s *Service) handleGetMigration(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Authorize(r.Context(), r); err != nil {
		writeError(w, r, err)
		return
	}

	// Read from the running job when there is one, because its record is only
	// as fresh as the last progress write, and from the tenant otherwise.
	if running := s.Running(); running != nil && running.ID == r.PathValue("id") {
		writeJSON(w, r, http.StatusOK, running)
		return
	}

	job, err := s.Record(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, job)
}

func (s *Service) handleCancelMigration(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Authorize(r.Context(), r); err != nil {
		writeError(w, r, err)
		return
	}

	if err := s.Cancel(r.PathValue("id")); err != nil {
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

// TenantsResponse says which tenants this microservice can act on.
type TenantsResponse struct {
	OwnerTenant string   `json:"ownerTenant"`
	Tenants     []string `json:"tenants"`
}

func (s *Service) handleTenants(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Authorize(r.Context(), r); err != nil {
		writeError(w, r, err)
		return
	}

	writeJSON(w, r, http.StatusOK, TenantsResponse{
		OwnerTenant: s.ownerTenant,
		Tenants:     s.Tenants(r.Context()),
	})
}

// PreflightRequest names the two tenants a migration would run between.
type PreflightRequest struct {
	SourceTenant string `json:"sourceTenant"`
	TargetTenant string `json:"targetTenant"`
}

// PreflightResponse reports what the platform said about each side, so a
// misconfigured subscription is visible before anything is created.
type PreflightResponse struct {
	Host   string        `json:"host"`
	Source PreflightSide `json:"source"`
	Target PreflightSide `json:"target"`
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
	if request.SourceTenant == "" || request.TargetTenant == "" {
		return nil, &BadRequestError{Reason: "sourceTenant and targetTenant are both required"}
	}

	source, err := s.tenant(request.SourceTenant)
	if err != nil {
		return nil, err
	}
	target, err := s.tenant(request.TargetTenant)
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

	return &PreflightResponse{
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
	}, nil
}

// BadRequestError is a caller mistake: a missing field, or two tenants that are
// one tenant.
type BadRequestError struct {
	Reason string
}

func (e *BadRequestError) Error() string { return e.Reason }

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
	var full *QueueFullError

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
	case errors.As(err, &full):
		status, kind = http.StatusTooManyRequests, "devicemigration/busy"
	}

	if status == http.StatusInternalServerError {
		// The message still goes to the caller, but the log is where the detail
		// of an unexpected failure belongs.
		slog.ErrorContext(r.Context(), "request failed", "path", r.URL.Path, "error", err)
	}

	writeJSON(w, r, status, errorBody{Error: kind, Message: err.Error()})
}
