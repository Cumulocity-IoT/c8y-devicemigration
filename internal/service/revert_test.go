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

// A revert through the API must delete exactly what the engine deletes when it
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

// A job that reports itself finished has to be finished on the record too.
//
// The API answers from the running job while it is still in memory. If the
// worker published SUCCEEDED there before writing the record, a caller that
// polled until the job was done and then acted on it - which is exactly what the
// UI does, and what these tests do - could read the previous record: still
// RUNNING, no run id. So the record is written before anything says the job is
// over.
//
// The platform is slowed down so the window is wide enough to be certain rather
// than lucky.
func TestAFinishedJobIsFinishedOnTheRecordToo(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	// Writes are slow and reads are not, which is what makes the ordering
	// visible: a caller can be served the record while the write that finishes
	// the job is still in flight.
	p.SlowDownWrites(100 * time.Millisecond)

	migration := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1", NoChildren: true,
	})
	if migration["status"] != string(service.StatusSucceeded) {
		t.Fatalf("the migration failed: %v", migration["error"])
	}

	// Straight away, with no waiting: the point is that no waiting is needed.
	record, err := svc.Record(t.Context(), fmt.Sprint(migration["id"]))
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != service.StatusSucceeded {
		t.Errorf("the API said the job succeeded, the record says %q", record.Status)
	}
	if record.RunID == "" {
		t.Error("the record carries no run id, so nothing could undo this migration")
	}
}

// The same thing, as somebody doing it: watch a migration finish, undo it. The
// undo must not be refused with "has no run id" because the record lags behind
// what the API reported.
func TestAMigrationCanBeRevertedTheMomentItReportsSuccess(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)
	p.SlowDownWrites(100 * time.Millisecond)

	migration := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1", NoChildren: true,
	})
	if migration["status"] != string(service.StatusSucceeded) {
		t.Fatalf("the migration failed: %v", migration["error"])
	}

	job := revert1(t, svc, service.RevertRequest{JobID: fmt.Sprint(migration["id"])})
	if job["status"] != string(service.StatusSucceeded) {
		t.Fatalf("the revert failed: %v", job["error"])
	}
}

// A progress write must not outlive the job it reports.
//
// The record is refreshed while a job runs, so a long migration can be followed.
// Those writes carry a snapshot of the job as it was, and the last word has to
// belong to the write that finishes it - a progress write landing afterwards
// would put RUNNING back, without a run id, on a job that had succeeded.
//
// Progress every millisecond and writes that take fifty guarantees one is in
// flight when the job ends, which is what makes this a test rather than a hope.
func TestAProgressWriteCannotOutliveTheJobItReports(t *testing.T) {
	p := twoTenants(t)
	svc := p.startWith(t, service.Options{ProgressInterval: time.Millisecond})
	p.SlowDownWrites(50 * time.Millisecond)

	migration := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1", NoChildren: true,
	})
	if migration["status"] != string(service.StatusSucceeded) {
		t.Fatalf("the migration failed: %v", migration["error"])
	}

	// Read repeatedly: a late write would arrive after the first read.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		record, err := svc.Record(t.Context(), fmt.Sprint(migration["id"]))
		if err != nil {
			t.Fatal(err)
		}
		if record.Status != service.StatusSucceeded {
			t.Fatalf("the record went back to %q after the job had finished", record.Status)
		}
		if record.RunID == "" {
			t.Fatal("the record lost its run id after the job had finished")
		}
	}
}

// A record left saying RUNNING by a container that no longer exists.
//
// This is what happens when the microservice is redeployed while a job is going,
// and it is what a person then sees: a row saying Running, and a Cancel button
// that answered "running job ... does not exist" - true, and no use to anybody.
// Nothing is running, so the row is settled instead: by the listing, so it
// settles itself, and by Cancel, so pressing it does what it looks like it does.
func staleRunning(t *testing.T, svc *service.Service, kind service.JobKind) *service.Job {
	t.Helper()

	job := service.Job{
		ID: "20260827T092609Z_053ed3", Kind: kind, Status: service.StatusRunning,
		RunID:   "20260827T092609Z_abcdef",
		Request: &service.MigrationRequest{SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1"},
		Revert:  &service.RevertRequest{TargetTenant: "t-target", RunID: "20260827T092609Z_abcdef"},
	}
	if _, err := svc.CreateRecord(t.Context(), &job); err != nil {
		t.Fatal(err)
	}
	return &job
}

func TestAJobLeftRunningByAReplacedContainerSettlesWhenListed(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)
	stale := staleRunning(t, svc, service.KindRevert)

	// The listing a screen reads, and nothing else.
	listed := decode(t, call(t, svc, request(t, http.MethodGet, "/reverts", nil)))
	if listed["reverts"] == nil {
		t.Fatalf("no reverts came back: %v", listed)
	}

	settled, err := svc.Record(t.Context(), stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != service.StatusInterrupted {
		t.Errorf("status = %q, want INTERRUPTED", settled.Status)
	}
	if settled.CountsVerified {
		t.Error("an interrupted job's counts cannot be believed, and it says they can")
	}
	if !strings.Contains(settled.Error, "replaced") {
		t.Errorf("the record does not say what happened: %q", settled.Error)
	}
	if settled.RunID != stale.RunID {
		t.Errorf("run id = %q, and without it nothing can be undone", settled.RunID)
	}
}

func TestCancellingAJobLeftRunningSettlesItInsteadOfRefusing(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)
	stale := staleRunning(t, svc, service.KindRevert)

	recorder := call(t, svc, request(t, http.MethodPost, "/reverts/"+stale.ID+"/cancel", nil))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("cancel answered %d, body %s", recorder.Code, recorder.Body)
	}

	settled, err := svc.Record(t.Context(), stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != service.StatusInterrupted {
		t.Errorf("status = %q, want INTERRUPTED", settled.Status)
	}
}

// A job that is genuinely over is a different thing: the state is wrong for the
// request, and saying so is more use than pretending to cancel something.
func TestCancellingAFinishedJobIsRefusedAsAConflict(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	done := service.Job{
		ID: "20260827T090000Z_done", Kind: service.KindMigration, Status: service.StatusSucceeded,
		Request: &service.MigrationRequest{SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1"},
	}
	if _, err := svc.CreateRecord(t.Context(), &done); err != nil {
		t.Fatal(err)
	}

	recorder := call(t, svc, request(t, http.MethodPost, "/migrations/"+done.ID+"/cancel", nil))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("cancel answered %d, want 409: %s", recorder.Code, recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), "already SUCCEEDED") {
		t.Errorf("the refusal does not say why: %s", recorder.Body)
	}
}

// A job that never existed is still a 404. Settling records must not become a
// way to invent them.
func TestCancellingAJobNobodyHasHeardOfIsStillNotFound(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodPost, "/migrations/no-such-job/cancel", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("cancel answered %d, want 404: %s", recorder.Code, recorder.Body)
	}
}

// What is still going to happen is not stale either, however often the list is
// read: the job that is running now, and the ones waiting behind it.
//
// The distinction SettleStale relies on is that this container knows which job is
// running. A queued one it cannot tell from a leftover at all, which is why it
// leaves QUEUED records alone and startup - where nothing is queued yet - is what
// closes those.
func TestListingDoesNotSettleWhatIsStillGoingToHappen(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	// Slow enough that the first job is still going while the assertions run.
	p.SlowDown(30 * time.Millisecond)
	go svc.Work(t.Context())

	first := accept(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1", DataTypes: "all",
	})
	second := accept(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "2",
	})

	// Until the record says RUNNING there is nothing here for SettleStale to be
	// wrong about - a record still saying QUEUED is left alone whatever happens.
	waitFor(t, "the first job's record to say it is running", func() bool {
		record, err := svc.Record(t.Context(), first)
		return err == nil && record.Status == service.StatusRunning
	})

	// The listing a screen reads, which is what settles stale records.
	call(t, svc, request(t, http.MethodGet, "/migrations", nil))

	for what, id := range map[string]string{"the running job": first, "the queued job": second} {
		record, err := svc.Record(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if record.Status == service.StatusInterrupted {
			t.Errorf("%s was closed as interrupted by a listing: %s", what, record.Error)
		}
	}
}

// accept posts a migration and returns its id, without waiting for it.
func accept(t *testing.T, svc *service.Service, spec service.MigrationRequest) string {
	t.Helper()

	recorder := call(t, svc, request(t, http.MethodPost, "/migrations", spec))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("the migration was not accepted: %s", recorder.Body)
	}
	return fmt.Sprint(decode(t, recorder)["id"])
}
