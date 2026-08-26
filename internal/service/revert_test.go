package service_test

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/c8yclient"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/export"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/migrate"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/reverter"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/service"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/testfixture"
)

// revert1 runs one revert through the HTTP API and returns the finished job.
func revert1(t *testing.T, svc *service.Service, spec service.RevertRequest) map[string]any {
	t.Helper()

	recorder := call(t, svc, request(t, http.MethodPost, "/reverts", spec))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	id := fmt.Sprint(decode(t, recorder)["id"])

	var job map[string]any
	waitFor(t, "the revert to finish", func() bool {
		got := decode(t, call(t, svc, request(t, http.MethodGet, "/reverts/"+id, nil)))
		switch service.JobStatus(fmt.Sprint(got["status"])) {
		case service.StatusSucceeded, service.StatusFailed, service.StatusCancelled:
			job = got
			return true
		}
		return false
	})
	return job
}

// The whole point: a migration can be undone from a container that knows nothing
// about it, because the objects carry the run id.
func TestARevertRemovesWhatAMigrationCreated(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	migration := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
	})
	if migration["status"] != string(service.StatusSucceeded) {
		t.Fatalf("the migration failed: %v", migration["error"])
	}
	created := len(p.tenants["t-target"].Created())

	job := revert1(t, svc, service.RevertRequest{
		TargetTenant: "t-target", RunID: fmt.Sprint(migration["runId"]),
	})
	if job["status"] != string(service.StatusSucceeded) {
		t.Fatalf("status = %v, error %v", job["status"], job["error"])
	}
	if job["kind"] != string(service.KindRevert) {
		t.Fatalf("kind = %v, want REVERT", job["kind"])
	}

	deleted := p.tenants["t-target"].Deleted()
	if len(deleted) != created {
		t.Fatalf("deleted %d of %d objects the migration created", len(deleted), created)
	}
}

// The convenient form: an operator who has just watched a migration go wrong
// should not have to copy a run id out of it.
func TestARevertCanNameTheJobItUndoes(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	migration := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
	})

	// Neither the run id nor the tenant: both come from the migration's record.
	job := revert1(t, svc, service.RevertRequest{JobID: fmt.Sprint(migration["id"])})
	if job["status"] != string(service.StatusSucceeded) {
		t.Fatalf("status = %v, error %v", job["status"], job["error"])
	}
	if job["runId"] != migration["runId"] {
		t.Fatalf("the revert ran as %v, the migration as %v", job["runId"], migration["runId"])
	}
	if len(p.tenants["t-target"].Deleted()) == 0 {
		t.Fatal("nothing was deleted")
	}
}

// A run with no marker cannot be found again, and the refusal has to say why
// rather than reporting a revert that deleted nothing.
func TestARunThatWasNeverMarkedCannotBeUndone(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	migration := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
		NoFlag: true,
	})
	if migration["status"] != string(service.StatusSucceeded) {
		t.Fatalf("the migration failed: %v", migration["error"])
	}

	// An unmarked run still announces a run id, so accepting this would produce
	// a revert that queries for a marker nothing carries, deletes nothing, and
	// reports success - leaving somebody believing the objects are gone.
	recorder := call(t, svc, request(t, http.MethodPost, "/reverts",
		service.RevertRequest{JobID: fmt.Sprint(migration["id"])}))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), "noFlag") {
		t.Fatalf("the refusal should say why it cannot be found: %s", recorder.Body)
	}
	if len(p.tenants["t-target"].Deleted()) != 0 {
		t.Fatal("something was deleted by a refused revert")
	}
}

func TestARevertNeedsSomethingToGoOn(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	for _, spec := range []service.RevertRequest{
		{},                              // nothing at all
		{RunID: "20260825T162735Z_abc"}, // no tenant
		{TargetTenant: "t-target"},      // no run
	} {
		recorder := call(t, svc, request(t, http.MethodPost, "/reverts", spec))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%+v gave status %d, body %s", spec, recorder.Code, recorder.Body)
		}
	}
}

func TestARevertOfAnUnsubscribedTenantSaysWhichOne(t *testing.T) {
	p := twoTenants(t)
	p.Unsubscribe("t-target")
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodPost, "/reverts",
		service.RevertRequest{TargetTenant: "t-target", RunID: "20260825T162735Z_abc"}))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), "t-target") {
		t.Fatalf("body = %s", recorder.Body)
	}
}

func TestRevertingARevertIsRefused(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	migration := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
	})
	revert := revert1(t, svc, service.RevertRequest{JobID: fmt.Sprint(migration["id"])})

	recorder := call(t, svc, request(t, http.MethodPost, "/reverts",
		service.RevertRequest{JobID: fmt.Sprint(revert["id"])}))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	// The reason matters, not just the status: a revert job carries a run id and
	// no migration request, so without this guard it is refused for having no
	// target tenant - which is true but tells nobody anything.
	if !strings.Contains(recorder.Body.String(), "itself a revert") {
		t.Fatalf("refused for the wrong reason: %s", recorder.Body)
	}
}

// The two listings are about different things, and mixing them would make either
// one useless for finding anything.
func TestMigrationsAndRevertsAreListedSeparately(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	migration := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
	})
	revert := revert1(t, svc, service.RevertRequest{JobID: fmt.Sprint(migration["id"])})

	migrations, _ := decode(t, call(t, svc,
		request(t, http.MethodGet, "/migrations", nil)))["migrations"].([]any)
	reverts, _ := decode(t, call(t, svc,
		request(t, http.MethodGet, "/reverts", nil)))["reverts"].([]any)

	if len(migrations) != 1 || len(reverts) != 1 {
		t.Fatalf("%d migrations and %d reverts, want one of each", len(migrations), len(reverts))
	}
	if first, _ := migrations[0].(map[string]any); first["id"] != migration["id"] {
		t.Fatalf("the migrations listing holds %v", first["id"])
	}
	if first, _ := reverts[0].(map[string]any); first["id"] != revert["id"] {
		t.Fatalf("the reverts listing holds %v", first["id"])
	}
}

func TestARevertCannotBeStartedBySubtenantUsers(t *testing.T) {
	p := twoTenants(t)
	p.AddUser("t-source", "admin", "source-secret", service.RequiredRole)
	svc := p.start(t)

	r := request(t, http.MethodPost, "/reverts",
		service.RevertRequest{TargetTenant: "t-target", RunID: "20260825T162735Z_abc"})
	authenticate(r, "t-source", "admin", "source-secret")

	if recorder := call(t, svc, r); recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
}

// --- what happens to a job whose container stopped existing ------------------

// A microservice cannot resume: whatever the engine was part way through is
// over, and the container that replaces it knows nothing about it. What it can
// do is stop the record claiming to be running, and keep the run id so somebody
// can look at the target tenant.
func TestRestartingClosesTheRecordsOfJobsThatWereRunning(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	running := service.Job{
		ID: "20260825T162735Z_running", Kind: service.KindMigration,
		Status: service.StatusRunning, RunID: "20260825T162735Z_marker",
		Request: &service.MigrationRequest{
			SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
		},
	}
	recordID, err := svc.CreateRecord(t.Context(), &running)
	if err != nil {
		t.Fatal(err)
	}
	running.RecordID = recordID

	// What a fresh container does before accepting anything new.
	if err := svc.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}

	closed, err := svc.Record(t.Context(), running.ID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.Status != service.StatusInterrupted {
		t.Fatalf("status = %q, want INTERRUPTED", closed.Status)
	}
	if closed.CountsVerified {
		t.Fatal("an interrupted job cannot vouch for its own counts")
	}
	if closed.RunID != running.RunID {
		t.Fatalf("the run id was lost, so nobody can find what it created: %q", closed.RunID)
	}
	if !strings.Contains(closed.Error, "restarted") {
		t.Fatalf("error = %q, which does not say what happened", closed.Error)
	}
	if closed.FinishedAt == "" {
		t.Fatal("an interrupted job has no end time")
	}
}

// A job that was still queued never ran, and saying so is different from saying
// it was interrupted part way through something.
func TestRestartingClosesQueuedJobsDifferently(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	queued := service.Job{
		ID: "20260825T162735Z_queued", Kind: service.KindMigration,
		Status:  service.StatusQueued,
		Request: &service.MigrationRequest{SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1"},
	}
	if _, err := svc.CreateRecord(t.Context(), &queued); err != nil {
		t.Fatal(err)
	}

	if err := svc.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}

	closed, err := svc.Record(t.Context(), queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.Status != service.StatusInterrupted {
		t.Fatalf("status = %q, want INTERRUPTED", closed.Status)
	}
	if !strings.Contains(closed.Error, "nothing was done") {
		t.Fatalf("error = %q, which does not say that it never started", closed.Error)
	}
}

// And a job that finished is left exactly as it was.
func TestRestartingLeavesFinishedJobsAlone(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	done := service.Job{
		ID: "20260825T162735Z_done", Kind: service.KindMigration,
		Status: service.StatusSucceeded, CountsVerified: true,
		FinishedAt: "2026-08-25T16:27:35.000Z",
		Request:    &service.MigrationRequest{SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1"},
	}
	if _, err := svc.CreateRecord(t.Context(), &done); err != nil {
		t.Fatal(err)
	}

	if err := svc.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}

	after, err := svc.Record(t.Context(), done.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != service.StatusSucceeded || !after.CountsVerified ||
		after.FinishedAt != done.FinishedAt {
		t.Fatalf("a finished job was rewritten: %+v", after)
	}
}

// A record written before jobs had kinds is a migration - that is all there was.
func TestARecordWithoutAKindIsAMigration(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	old := service.Job{
		ID: "20260825T162735Z_old", Status: service.StatusSucceeded,
		Request: &service.MigrationRequest{SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1"},
	}
	if _, err := svc.CreateRecord(t.Context(), &old); err != nil {
		t.Fatal(err)
	}

	migrations, _ := decode(t, call(t, svc,
		request(t, http.MethodGet, "/migrations", nil)))["migrations"].([]any)
	if len(migrations) != 1 {
		t.Fatalf("the migrations listing holds %d entries, so an older record is hidden",
			len(migrations))
	}

	reverts, _ := decode(t, call(t, svc,
		request(t, http.MethodGet, "/reverts", nil)))["reverts"].([]any)
	if len(reverts) != 0 {
		t.Fatalf("an older record was listed as a revert: %v", reverts)
	}
}

// The same guarantee the migration differential gives, for the destructive half:
// a revert through the API must delete exactly what the engine deletes when it
// is driven directly. This is the test that would notice if the service ever
// started shaping a revert differently - by mapping file instead of run id, say,
// or with the wrong tenant.
func TestARevertThroughTheAPIMatchesTheEngineDirectly(t *testing.T) {
	viaAPI := revertViaAPI(t)
	direct := revertDirectly(t)

	if viaAPI != direct {
		t.Fatalf("what was deleted differs\nvia the API:\n%s\ndirectly:\n%s", viaAPI, direct)
	}
}

// revertViaAPI migrates and then undoes it through HTTP, returning what the
// target tenant was asked to delete.
func revertViaAPI(t *testing.T) string {
	t.Helper()

	p := twoTenants(t)
	svc := p.start(t)

	migration := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
		DataTypes: "all",
	})
	if migration["status"] != string(service.StatusSucceeded) {
		t.Fatalf("the migration failed: %v", migration["error"])
	}

	job := revert1(t, svc, service.RevertRequest{JobID: fmt.Sprint(migration["id"])})
	if job["status"] != string(service.StatusSucceeded) {
		t.Fatalf("the revert failed: %v", job["error"])
	}

	return deletions(t, p.tenants["t-target"])
}

// revertDirectly does the same by calling the engine, with no service involved.
func revertDirectly(t *testing.T) string {
	t.Helper()

	source := testfixture.New(t, "hierarchy.json")
	target := testfixture.New(t, "empty.json")
	work := t.TempDir()

	sourceSession := source.ReadSession(t, work, "source.json")
	targetSession := target.ReadSession(t, work, "target.json")

	sourceClient, err := c8yclient.New(sourceSession, "")
	if err != nil {
		t.Fatal(err)
	}
	targetClient, err := c8yclient.New(targetSession, "")
	if err != nil {
		t.Fatal(err)
	}

	dataTypes, err := export.ResolveDataTypes("all")
	if err != nil {
		t.Fatal(err)
	}
	windows, err := export.ResolveWindows("", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	var progress strings.Builder
	mapping := filepath.Join(work, "id_mapping.json")
	err = migrate.New(sourceClient, targetClient, sourceSession, targetSession,
		&progress, nil, migrate.Options{
			RootID: "1", MappingFile: mapping,
			ChildTypes: export.DefaultChildTypes, DataTypes: dataTypes, Windows: windows,
			Incremental: true, Force: true,
		}).Run(context.Background())
	if err != nil {
		t.Fatalf("the direct migration failed: %v\n%s", err, progress.String())
	}

	// By run id, exactly as the service does it - the run id is the one the
	// engine announced.
	runID := runIDFrom(t, progress.String())
	if err := reverter.New(targetClient, &progress, nil, reverter.Options{
		RunID: runID, Force: true,
	}).Run(context.Background()); err != nil {
		t.Fatalf("the direct revert failed: %v\n%s", err, progress.String())
	}

	return deletions(t, target)
}

// runIDFrom reads the run id out of the engine's output, which is the only place
// it exists.
func runIDFrom(t *testing.T, output string) string {
	t.Helper()

	for _, line := range strings.Split(output, "\n") {
		if match := regexp.MustCompile(`^Run id (\S+),`).FindStringSubmatch(line); match != nil {
			return match[1]
		}
	}
	t.Fatalf("no run id in:\n%s", output)
	return ""
}

// deletions is what a tenant was asked to delete, as a comparable string.
func deletions(t *testing.T, target *testfixture.Server) string {
	t.Helper()

	deleted := append([]string(nil), target.Deleted()...)
	if len(deleted) == 0 {
		t.Fatal("nothing was deleted, so this comparison would prove nothing")
	}
	sort.Strings(deleted)
	return strings.Join(deleted, "\n")
}
