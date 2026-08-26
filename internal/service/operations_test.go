package service_test

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/service"
)

// --- metrics -----------------------------------------------------------------

// metrics reads /prometheus, which needs no credentials because the platform's
// scraper has none.
func metrics(t *testing.T, svc *service.Service) string {
	t.Helper()

	r := request(t, http.MethodGet, "/prometheus", nil)
	r.Header.Del("Authorization")

	recorder := call(t, svc, r)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	return recorder.Body.String()
}

// A finished job has to leave a trace an operator can graph: what ran, how it
// ended, and how long it took.
func TestAFinishedJobIsCounted(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	job := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
	})
	if job["status"] != string(service.StatusSucceeded) {
		t.Fatalf("the migration failed: %v", job["error"])
	}

	exposed := metrics(t, svc)
	for _, want := range []string{
		`devicemigration_jobs_total{kind="MIGRATION",status="SUCCEEDED"}`,
		"devicemigration_job_duration_seconds",
		"devicemigration_queue_depth",
		"devicemigration_job_running",
	} {
		if !strings.Contains(exposed, want) {
			t.Errorf("%s is not exposed", want)
		}
	}

	// Nothing is running once the job is done, and a gauge stuck at 1 would tell
	// an operator the service is busy forever.
	if !strings.Contains(exposed, "devicemigration_job_running 0") {
		t.Error("devicemigration_job_running was left set")
	}
}

func TestAFailedJobIsCountedAsSuch(t *testing.T) {
	p := twoTenants(t)
	p.tenants["t-target"].FailCreateAfter(3)
	svc := p.start(t)

	migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
	})

	if exposed := metrics(t, svc); !strings.Contains(exposed,
		`devicemigration_jobs_total{kind="MIGRATION",status="FAILED"}`) {
		t.Error("a failed migration was not counted as failed")
	}
}

func TestTheSubscribedTenantsAreExposed(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	// Read through the endpoint that reports them, which is what refreshes the
	// gauge.
	call(t, svc, request(t, http.MethodGet, "/tenants", nil))

	if exposed := metrics(t, svc); !strings.Contains(exposed, "devicemigration_subscribed_tenants 3") {
		t.Errorf("the tenant count is not exposed as 3:\n%s",
			grepLines(exposed, "devicemigration_subscribed_tenants"))
	}
}

func grepLines(text, needle string) string {
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, needle) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// --- log levels --------------------------------------------------------------

// Turning debug output on without redeploying, which otherwise means losing
// whatever was running.
func TestTheLogLevelCanBeChangedWhileRunning(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	before := decode(t, call(t, svc, request(t, http.MethodGet, "/loggers/ROOT", nil)))
	if before["configuredLevel"] != "INFO" {
		t.Fatalf("the level starts at %v, want INFO", before["configuredLevel"])
	}

	recorder := call(t, svc, request(t, http.MethodPost, "/loggers/ROOT",
		map[string]string{"configuredLevel": "DEBUG"}))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}

	after := decode(t, call(t, svc, request(t, http.MethodGet, "/loggers/ROOT", nil)))
	if after["configuredLevel"] != "DEBUG" || after["effectiveLevel"] != "DEBUG" {
		t.Fatalf("the level did not change: %v", after)
	}

	// A null level resets, which is what `c8y microservices loggers delete`
	// sends.
	recorder = call(t, svc, request(t, http.MethodPost, "/loggers/ROOT",
		map[string]any{"configuredLevel": nil}))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("reset returned %d: %s", recorder.Code, recorder.Body)
	}

	reset := decode(t, call(t, svc, request(t, http.MethodGet, "/loggers/ROOT", nil)))
	if reset["configuredLevel"] != "INFO" {
		t.Fatalf("the level was not reset: %v", reset)
	}
}

func TestTheLoggerListingHasTheActuatorShape(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	body := decode(t, call(t, svc, request(t, http.MethodGet, "/loggers", nil)))

	// The three fields the platform's tooling reads.
	levels, _ := body["levels"].([]any)
	loggers, _ := body["loggers"].(map[string]any)
	if len(levels) == 0 || loggers == nil {
		t.Fatalf("body = %v", body)
	}
	if _, ok := body["groups"]; !ok {
		t.Fatalf("no groups field: %v", body)
	}
	if _, ok := loggers["ROOT"]; !ok {
		t.Fatalf("no ROOT logger: %v", loggers)
	}
}

func TestAnUnknownLoggerIsNotFound(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodGet, "/loggers/com.example.Thing", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
}

func TestAnUnknownLevelIsRefused(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodPost, "/loggers/ROOT",
		map[string]string{"configuredLevel": "CHATTY"}))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
}

// Changing how a running service behaves is a write, and a subtenant user may
// not do it any more than they may migrate.
func TestTheLogLevelCannotBeChangedBySubtenantUsers(t *testing.T) {
	p := twoTenants(t)
	p.AddUser("t-source", "admin", "source-secret", service.RequiredRole)
	svc := p.start(t)

	r := request(t, http.MethodPost, "/loggers/ROOT", map[string]string{"configuredLevel": "DEBUG"})
	authenticate(r, "t-source", "admin", "source-secret")

	if recorder := call(t, svc, r); recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
}

// --- settings and retention --------------------------------------------------

func TestSettingsComeFromTheOwnerTenantsOptions(t *testing.T) {
	p := twoTenants(t)
	p.SetOption(bootstrapTenant, service.SettingsCategory, service.SettingRetentionDays, "7")
	p.SetOption(bootstrapTenant, service.SettingsCategory, service.SettingDefaultDateFrom, "-30d")

	svc := p.start(t)

	settings := svc.Settings()
	if settings.RetentionDays != 7 {
		t.Errorf("RetentionDays = %d, want 7", settings.RetentionDays)
	}
	if settings.DefaultDateFrom != "-30d" {
		t.Errorf("DefaultDateFrom = %q, want -30d", settings.DefaultDateFrom)
	}
}

// Nothing configured means the documented defaults, not an error: a tenant with
// no options is the normal case.
func TestWithNoOptionsTheDefaultsApply(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	settings := svc.Settings()
	if settings.RetentionDays != 0 || settings.DefaultDateFrom != "" {
		t.Fatalf("settings = %+v, want the defaults", settings)
	}
}

// A value somebody typed wrong must not become a retention period. Deleting
// records because "seven" could not be parsed as a number would be unforgivable.
func TestAnUnreadableRetentionSettingIsIgnored(t *testing.T) {
	p := twoTenants(t)
	p.SetOption(bootstrapTenant, service.SettingsCategory, service.SettingRetentionDays, "seven")
	svc := p.start(t)

	if days := svc.Settings().RetentionDays; days != 0 {
		t.Fatalf("RetentionDays = %d, want the default of 0 (keep everything)", days)
	}
}

func TestRecordsAreKeptForeverUnlessARetentionPeriodIsSet(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	old := service.Job{
		ID: "20200101T000000Z_ancient", Kind: service.KindMigration,
		Status: service.StatusSucceeded, CountsVerified: true,
		FinishedAt: "2020-01-01T00:00:00.000Z",
		Request:    &service.MigrationRequest{SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1"},
	}
	if _, err := svc.CreateRecord(t.Context(), &old); err != nil {
		t.Fatal(err)
	}

	pruned, err := svc.Prune(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 0 {
		t.Fatalf("pruned %d records with no retention period configured", pruned)
	}
	if _, err := svc.Record(t.Context(), old.ID); err != nil {
		t.Fatalf("the record was removed anyway: %v", err)
	}
}

func TestExpiredRecordsArePruned(t *testing.T) {
	p := twoTenants(t)
	p.SetOption(bootstrapTenant, service.SettingsCategory, service.SettingRetentionDays, "30")
	svc := p.start(t)

	ancient := service.Job{
		ID: "20200101T000000Z_ancient", Kind: service.KindMigration,
		Status: service.StatusSucceeded, FinishedAt: "2020-01-01T00:00:00.000Z",
		Request: &service.MigrationRequest{SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1"},
	}
	if _, err := svc.CreateRecord(t.Context(), &ancient); err != nil {
		t.Fatal(err)
	}

	// And one that finished just now, which must survive.
	recent := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
	})

	pruned, err := svc.Prune(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 1 {
		t.Fatalf("pruned %d records, want only the expired one", pruned)
	}

	if _, err := svc.Record(t.Context(), ancient.ID); err == nil {
		t.Fatal("the expired record is still there")
	}
	if _, err := svc.Record(t.Context(), fmt.Sprint(recent["id"])); err != nil {
		t.Fatalf("a record that finished just now was pruned: %v", err)
	}
}

// A job that has not finished has no end time to measure, and pruning it would
// delete the only account of something still in progress.
func TestUnfinishedRecordsAreNeverPruned(t *testing.T) {
	p := twoTenants(t)
	p.SetOption(bootstrapTenant, service.SettingsCategory, service.SettingRetentionDays, "1")
	svc := p.start(t)

	for _, unfinished := range []service.Job{
		{
			ID: "20200101T000000Z_running", Kind: service.KindMigration,
			Status: service.StatusRunning, RunID: "20200101T000000Z_marker",
			Request: &service.MigrationRequest{
				SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1"},
		},
		{
			// Running, but carrying an old end time - written by a version that
			// set one too early, or by a restart that half-closed it. The status
			// is what decides, not the timestamp, or a job still in progress
			// loses the only account of itself.
			ID: "20200101T000000Z_stale", Kind: service.KindMigration,
			Status: service.StatusRunning, RunID: "20200101T000000Z_marker2",
			FinishedAt: "2020-01-01T00:00:00.000Z",
			Request: &service.MigrationRequest{
				SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1"},
		},
	} {
		if _, err := svc.CreateRecord(t.Context(), &unfinished); err != nil {
			t.Fatal(err)
		}
	}

	if pruned, err := svc.Prune(t.Context()); err != nil || pruned != 0 {
		t.Fatalf("pruned %d records, err %v", pruned, err)
	}
	for _, id := range []string{"20200101T000000Z_running", "20200101T000000Z_stale"} {
		if _, err := svc.Record(t.Context(), id); err != nil {
			t.Fatalf("the record of unfinished job %s was pruned: %v", id, err)
		}
	}
}

// The configured window applies to a migration that does not name one, and a
// migration that does keeps its own.
func TestTheDefaultDataWindowIsUsedWhenAMigrationDoesNotSayOtherwise(t *testing.T) {
	p := twoTenants(t)
	p.SetOption(bootstrapTenant, service.SettingsCategory, service.SettingDefaultDateFrom, "-1d")
	svc := p.start(t)

	job := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
		DataTypes: "measurements",
	})
	if job["status"] != string(service.StatusSucceeded) {
		t.Fatalf("status = %v, error %v", job["status"], job["error"])
	}

	// The engine sends a dateFrom for measurements whatever happens - its own
	// default is the last fourteen days - so the question is which window
	// arrived, not whether one did.
	pattern := regexp.MustCompile(`dateFrom=([^&]+)`)
	found := 0
	for _, query := range p.tenants["t-source"].Queries() {
		if !strings.HasPrefix(query, "/measurement/measurements?") {
			continue
		}

		match := pattern.FindStringSubmatch(query)
		if match == nil {
			t.Fatalf("no window on %s", query)
		}
		raw, err := url.QueryUnescape(match[1])
		if err != nil {
			t.Fatal(err)
		}
		from, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			t.Fatalf("dateFrom %q is not a timestamp: %v", raw, err)
		}

		// -1d, not the engine's -14d.
		age := time.Since(from)
		if age > 48*time.Hour {
			t.Fatalf("the window reaches back %v, so the configured -1d was ignored", age)
		}
		found++
	}
	if found == 0 {
		t.Fatal("no measurements were asked for")
	}
}

// --- looking a job up --------------------------------------------------------

// Polling a job's status is the most frequent call this service gets. Reading
// every record and filtering here would mean a tenant with a few hundred jobs
// pays for all of them on every poll, so the id goes into the query.
func TestAJobIsLookedUpByAskingThePlatformForIt(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	job := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
	})
	id := fmt.Sprint(job["id"])

	// The job has finished, so this is answered from the record rather than from
	// memory.
	recorder := call(t, svc, request(t, http.MethodGet, "/migrations/"+id, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}

	asked := false
	for _, query := range p.tenants[bootstrapTenant].Queries() {
		if strings.Contains(query, "inventory/managedObjects?") && strings.Contains(query, id) {
			asked = true
		}
	}
	if !asked {
		t.Fatal("the job id never reached the platform, so every record was read and " +
			"filtered here instead")
	}
}
