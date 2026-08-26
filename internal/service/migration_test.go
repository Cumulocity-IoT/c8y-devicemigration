package service_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/service"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/testfixture"
)

// waitFor polls until the condition holds. A job runs on its own goroutine, so a
// test that asserted straight away would be asserting against a job that has not
// started yet.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// migrate1 runs one migration through the HTTP API and returns the finished job.
func migrate1(t *testing.T, svc *service.Service, spec service.MigrationRequest) map[string]any {
	t.Helper()

	go svc.Work(t.Context())

	recorder := call(t, svc, request(t, http.MethodPost, "/migrations", spec))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}

	accepted := decode(t, recorder)
	id, _ := accepted["id"].(string)
	if id == "" {
		t.Fatalf("no job id in %s", recorder.Body)
	}
	if accepted["status"] != string(service.StatusQueued) {
		t.Fatalf("status = %v, want QUEUED", accepted["status"])
	}
	if location := recorder.Header().Get("Location"); !strings.HasSuffix(location, "/"+id) {
		t.Fatalf("Location = %q, which does not point at the job", location)
	}

	var job map[string]any
	waitFor(t, "the job to finish", func() bool {
		got := decode(t, call(t, svc, request(t, http.MethodGet, "/migrations/"+id, nil)))
		switch service.JobStatus(fmt.Sprint(got["status"])) {
		case service.StatusSucceeded, service.StatusFailed, service.StatusCancelled:
			job = got
			return true
		}
		return false
	})
	return job
}

func TestAMigrationMovesTheHierarchy(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	job := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
	})

	if job["status"] != string(service.StatusSucceeded) {
		t.Fatalf("status = %v, error %v", job["status"], job["error"])
	}
	if created := len(p.tenants["t-target"].Created()); created != 9 {
		t.Fatalf("the target holds %d objects, want the whole hierarchy of 9", created)
	}
	if job["runId"] == "" || job["runId"] == nil {
		t.Fatal("no run id was recorded, so the run could never be reverted")
	}
	if job["countsVerified"] != true {
		t.Fatalf("a finished run should report verified counts, got %v", job["countsVerified"])
	}

	progress, _ := job["progress"].([]any)
	if len(progress) == 0 {
		t.Fatal("no progress was captured")
	}
	if !strings.Contains(fmt.Sprint(progress...), "Migration complete") {
		t.Fatalf("progress does not read like a finished migration: %v", progress)
	}
}

// The record is the only thing that survives the container, so it has to be in
// the owner tenant and it has to be findable.
func TestTheJobIsRecordedInTheOwnerTenant(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	job := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
	})

	var records int
	for _, body := range p.tenants[bootstrapTenant].Created() {
		if strings.Contains(string(body), service.RecordType) {
			records++
		}
	}
	if records != 1 {
		t.Fatalf("the owner tenant holds %d job records, want 1", records)
	}

	listed := decode(t, call(t, svc, request(t, http.MethodGet, "/migrations", nil)))
	migrations, _ := listed["migrations"].([]any)
	if len(migrations) != 1 {
		t.Fatalf("the listing holds %d migrations: %v", len(migrations), listed)
	}
	first, _ := migrations[0].(map[string]any)
	if first["id"] != job["id"] {
		t.Fatalf("the listing does not contain the job that just ran: %v", listed)
	}
	if first["requestedBy"] != bootstrapTenant+"/admin" {
		t.Fatalf("requestedBy = %v, so the record does not say who asked", first["requestedBy"])
	}
}

// migrate makes its staging directory with os.MkdirTemp("") and keeps it on
// failure, on purpose - which in a container that runs for months is a disk that
// fills up. The service contains it in a per-job directory and removes that
// whatever happened.
// sandbox gives a test its own temporary directory and points the process at it,
// so what a job leaves behind is visible and belongs to this test alone. The
// engine chooses its own staging directory under the process temporary
// directory, which is the only reason this has to go through the environment.
func sandbox(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	t.Setenv("TMPDIR", directory)
	return directory
}

func TestAFinishedJobLeavesNothingOnDisk(t *testing.T) {
	staging := sandbox(t)
	p := twoTenants(t)
	svc := p.start(t)

	job := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
		DataTypes: "all",
	})
	if job["status"] != string(service.StatusSucceeded) {
		t.Fatalf("status = %v, error %v", job["status"], job["error"])
	}

	assertNothingLeftBehind(t, staging)
}

// The case that matters: migrate keeps its staging directory when a run fails,
// on purpose, and nothing in the engine ever collects it. In a container that
// runs for months that is a disk filling with abandoned copies of customer data.
func TestAFailedJobAlsoLeavesNothingOnDisk(t *testing.T) {
	staging := sandbox(t)
	p := twoTenants(t)

	// Fails part way through creating the hierarchy, which is when there is
	// most to leave behind.
	p.tenants["t-target"].FailCreateAfter(3)
	svc := p.start(t)

	job := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
		DataTypes: "all",
	})
	if job["status"] != string(service.StatusFailed) {
		t.Fatalf("status = %v, want FAILED", job["status"])
	}
	if job["error"] == "" || job["error"] == nil {
		t.Fatal("a failed job must record why")
	}
	// Objects were created before the failure, so the run id is the only way to
	// find them again.
	if job["runId"] == "" || job["runId"] == nil {
		t.Fatal("a partly finished run must still record its run id")
	}

	assertNothingLeftBehind(t, staging)
}

// assertNothingLeftBehind looks for exported data rather than for directories
// with a particular name. Names are the engine's business and could change; what
// must never be left on a container's disk is a copy of a customer's devices.
func assertNothingLeftBehind(t *testing.T, directory string) {
	t.Helper()

	var found []string
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			// A directory removed while being walked is the expected case, not
			// a failure.
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if entry.IsDir() {
			return nil
		}
		switch name := entry.Name(); {
		case strings.HasPrefix(name, "exported_device_"),
			strings.HasSuffix(name, "_identities.json"),
			strings.Contains(name, "_child_"),
			strings.HasSuffix(name, ".jsonl"),
			strings.HasPrefix(name, "id_mapping.json"):
			found = append(found, strings.TrimPrefix(path, directory))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		sort.Strings(found)
		t.Fatalf("the job left %d exported files behind:\n%s", len(found), strings.Join(found, "\n"))
	}
}

// The queue is bounded, and a full queue must be refused rather than accepted
// and started an hour later. With no worker running, nothing drains it.
func TestTheQueueIsBoundedAndSaysSo(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	spec := service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
	}
	for i := range service.QueueDepth {
		recorder := call(t, svc, request(t, http.MethodPost, "/migrations", spec))
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("job %d was refused: %d %s", i, recorder.Code, recorder.Body)
		}
	}

	recorder := call(t, svc, request(t, http.MethodPost, "/migrations", spec))
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body %s", recorder.Code, recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), "queued") {
		t.Fatalf("body = %s", recorder.Body)
	}
}

func TestARootIdThatIsAPathIsRefusedBeforeAnythingIsRecorded(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodPost, "/migrations",
		service.MigrationRequest{
			SourceTenant: "t-source", TargetTenant: "t-target", RootID: "./1399229",
		}))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	if len(p.tenants[bootstrapTenant].Created()) != 0 {
		t.Fatal("a refused request still wrote a job record")
	}
}

func TestAMigrationNeedsARootId(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodPost, "/migrations",
		service.MigrationRequest{SourceTenant: "t-source", TargetTenant: "t-target"}))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
}

func TestAnUnknownJobIsNotFound(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodGet, "/migrations/nosuchjob", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
}

// Only owner-tenant admins may migrate. Worth its own test at this endpoint
// rather than trusting the one on /tenants: this is the endpoint that writes.
func TestMigrationsCannotBeStartedBySubtenantUsers(t *testing.T) {
	p := twoTenants(t)
	p.AddUser("t-source", "admin", "source-secret", service.RequiredRole)
	svc := p.start(t)

	r := request(t, http.MethodPost, "/migrations", service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
	})
	authenticate(r, "t-source", "admin", "source-secret")

	recorder := call(t, svc, r)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	if len(p.tenants[bootstrapTenant].Created()) != 0 {
		t.Fatal("a refused caller still got a job record written")
	}
}

// The point of the whole exercise: the engine embedded in a microservice must do
// exactly what the engine on a laptop does. Same fixture, same request, once
// through HTTP and once by calling migrate directly - the two target tenants
// must end up identical.
func TestAMigrationThroughTheAPIMatchesTheEngineDirectly(t *testing.T) {
	viaAPI := runViaAPI(t)
	direct := runDirectly(t)

	if viaAPI.objects != direct.objects {
		t.Fatalf("objects differ\nvia the API:\n%s\ndirectly:\n%s", viaAPI.objects, direct.objects)
	}
	if viaAPI.links != direct.links {
		t.Fatalf("relationships differ\nvia the API:\n%s\ndirectly:\n%s", viaAPI.links, direct.links)
	}
	if viaAPI.identities != direct.identities {
		t.Fatalf("identities differ\nvia the API:\n%s\ndirectly:\n%s",
			viaAPI.identities, direct.identities)
	}
	if viaAPI.records != direct.records {
		t.Fatalf("data records differ\nvia the API:\n%s\ndirectly:\n%s", viaAPI.records, direct.records)
	}
}

// Every run gets its own run id, and it becomes part of the marker fragment
// stamped on each created object. Two runs therefore differ in exactly that one
// name, so it is normalised away - the point of the comparison is everything
// else.
var markerFragment = regexp.MustCompile(`c8y_DeviceMigration_[0-9A-Za-z]+_[0-9a-f]+`)

func normalise(value string) string {
	return markerFragment.ReplaceAllString(value, "c8y_DeviceMigration_<runId>")
}

// outcome is the state of a target tenant after a migration, rendered so two of
// them can be compared as strings.
type outcome struct {
	objects    string
	links      string
	identities string
	records    string
}

func describe(t *testing.T, target *testfixture.Server) outcome {
	t.Helper()

	var objects []string
	for id, body := range target.Created() {
		objects = append(objects, id+" "+string(body))
	}

	var links []string
	for _, link := range target.Links() {
		links = append(links, fmt.Sprintf("%s %s %s", link.ParentID, link.ChildType, link.ChildID))
	}

	var identities []string
	for _, identity := range target.CreatedIdentities() {
		identities = append(identities, fmt.Sprint(identity))
	}

	var records []string
	for _, record := range target.CreatedRecords() {
		records = append(records, fmt.Sprintf("%s %s %s", record.Kind, record.DeviceID, record.Body))
	}

	for _, list := range [][]string{objects, links, identities, records} {
		sort.Strings(list)
	}
	return outcome{
		objects:    normalise(strings.Join(objects, "\n")),
		links:      strings.Join(links, "\n"),
		identities: strings.Join(identities, "\n"),
		records:    normalise(strings.Join(records, "\n")),
	}
}

func runViaAPI(t *testing.T) outcome {
	t.Helper()

	p := twoTenants(t)
	svc := p.start(t)

	job := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
		DataTypes: "all",
	})
	if job["status"] != string(service.StatusSucceeded) {
		t.Fatalf("the migration through the API failed: %v", job["error"])
	}
	return describe(t, p.tenants["t-target"])
}

func runDirectly(t *testing.T) outcome {
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
	err = migrate.New(sourceClient, targetClient, sourceSession, targetSession,
		&progress, nil, migrate.Options{
			RootID:      "1",
			MappingFile: filepath.Join(work, "id_mapping.json"),
			ChildTypes:  export.DefaultChildTypes,
			DataTypes:   dataTypes,
			Windows:     windows,
			Incremental: true,
			Force:       true,
		}).Run(context.Background())
	if err != nil {
		t.Fatalf("the direct migration failed: %v\n%s", err, progress.String())
	}
	return describe(t, target)
}

// A tenant holds far more than job records, and the platform ignores type and
// fragmentType whenever a query is present - which ListAll always adds. Asking
// by type therefore returns everything, a trap this repository has fallen into
// twice. Migrating into the owner tenant is the cheapest way to put unrelated
// managed objects next to the records.
func TestTheJobListingIgnoresEverythingElseInTheTenant(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	job := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: bootstrapTenant, RootID: "1",
	})
	if job["status"] != string(service.StatusSucceeded) {
		t.Fatalf("status = %v, error %v", job["status"], job["error"])
	}

	// 9 migrated objects and 1 job record now live in the same tenant.
	if created := len(p.tenants[bootstrapTenant].Created()); created != 10 {
		t.Fatalf("the owner tenant holds %d objects, want 9 migrated plus 1 record", created)
	}

	listed := decode(t, call(t, svc, request(t, http.MethodGet, "/migrations", nil)))
	migrations, _ := listed["migrations"].([]any)
	if len(migrations) != 1 {
		t.Fatalf("the listing returned %d entries, want only the one job record", len(migrations))
	}
}

// A cancelled run cannot be trusted about what it did: the engine has no
// cancellation checks, so a cancelled import counts the records it never sent as
// failures. The job has to say its numbers are not to be believed.
func TestACancelledJobSaysItsCountsAreNotVerified(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	// Slow enough that the migration is still going when the cancel arrives.
	p.SlowDown(30 * time.Millisecond)

	go svc.Work(t.Context())

	recorder := call(t, svc, request(t, http.MethodPost, "/migrations",
		service.MigrationRequest{
			SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
			DataTypes: "all",
		}))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	id := fmt.Sprint(decode(t, recorder)["id"])

	waitFor(t, "the job to start running", func() bool {
		running := svc.Running()
		return running != nil && running.ID == id
	})

	cancelled := call(t, svc, request(t, http.MethodPost, "/migrations/"+id+"/cancel", nil))
	if cancelled.Code != http.StatusAccepted {
		t.Fatalf("cancel returned %d: %s", cancelled.Code, cancelled.Body)
	}

	var job map[string]any
	waitFor(t, "the job to stop", func() bool {
		got := decode(t, call(t, svc, request(t, http.MethodGet, "/migrations/"+id, nil)))
		if service.JobStatus(fmt.Sprint(got["status"])) == service.StatusCancelled {
			job = got
			return true
		}
		return false
	})

	if job["countsVerified"] != false {
		t.Fatalf("countsVerified = %v, and a cancelled run's counts are not verifiable",
			job["countsVerified"])
	}
	if !strings.Contains(fmt.Sprint(job["error"]), "not known") {
		t.Fatalf("a cancelled job should say what is unknown about it: %v", job["error"])
	}
}

func TestCancellingAJobThatIsNotRunningIsNotFound(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodPost, "/migrations/nosuchjob/cancel", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
}

// childTypes and noChildren mean opposite things, and the CLI marks the two
// flags mutually exclusive. The API has to refuse the combination rather than
// silently letting one win.
func TestChildTypesAndNoChildrenCannotBothBeGiven(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodPost, "/migrations",
		service.MigrationRequest{
			SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
			ChildTypes: "asset", NoChildren: true,
		}))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
}

func TestNoChildrenMigratesOnlyTheRoot(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	job := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
		NoChildren: true,
	})
	if job["status"] != string(service.StatusSucceeded) {
		t.Fatalf("status = %v, error %v", job["status"], job["error"])
	}
	if created := len(p.tenants["t-target"].Created()); created != 1 {
		t.Fatalf("the target holds %d objects, want only the root", created)
	}
}

// Filtering has to happen in the query the platform runs, not in this process.
// The platform ignores type and fragmentType whenever a query is present, and
// ListAll always adds one for paging - so a listing that asked by type would
// page through every managed object in the tenant and filter them here. On a
// tenant with a million devices that is the difference between one request and
// thousands. This repository has been caught by that twice, which is why it is
// asserted rather than assumed.
func TestTheJobListingMakesThePlatformDoTheFiltering(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	if recorder := call(t, svc, request(t, http.MethodGet, "/migrations", nil)); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}

	var inventoryQueries []string
	for _, query := range p.tenants[bootstrapTenant].Queries() {
		if strings.HasPrefix(query, "/inventory/managedObjects?") {
			inventoryQueries = append(inventoryQueries, query)
		}
	}
	if len(inventoryQueries) == 0 {
		t.Fatal("the listing never asked the inventory anything")
	}
	for _, query := range inventoryQueries {
		if !strings.Contains(query, "has%28"+service.RecordFragment) &&
			!strings.Contains(query, "has("+service.RecordFragment) {
			t.Fatalf("the listing does not filter in the query, so it reads the whole tenant:\n%s", query)
		}
	}
}

// What Enqueue returns is a snapshot, not the live job. Handing back the live
// job made a freshly accepted migration report itself as already running,
// because the response was serialised while the worker was starting on the same
// struct - a flaky test found it, and the race detector did not, since the two
// accesses rarely overlap. Asserting it after the worker has demonstrably moved
// on makes it deterministic.
func TestWhatEnqueueReturnsDoesNotChangeUnderTheCaller(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)
	p.SlowDown(20 * time.Millisecond)

	go svc.Work(t.Context())

	accepted, err := svc.Enqueue(t.Context(), service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the worker to pick the job up", func() bool {
		running := svc.Running()
		return running != nil && running.ID == accepted.ID && running.Status == service.StatusRunning
	})

	if accepted.Status != service.StatusQueued {
		t.Fatalf("the accepted job now reads %q, so the caller was handed live state",
			accepted.Status)
	}
	if len(accepted.Progress) != 0 {
		t.Fatalf("the accepted job has grown %d progress lines", len(accepted.Progress))
	}

	// The same contract for the running job: what a status request is handed
	// must not keep changing after it is answered.
	observed := svc.Running()
	if observed == nil {
		t.Fatal("the job stopped running between the two checks")
	}
	p.SlowDown(0)
	waitFor(t, "the job to finish", func() bool { return svc.Running() == nil })

	if observed.Status != service.StatusRunning {
		t.Fatalf("the observed job now reads %q, so Running hands out live state",
			observed.Status)
	}
}
