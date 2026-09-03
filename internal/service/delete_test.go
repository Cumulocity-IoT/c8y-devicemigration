package service_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/service"
)

// Deleting a record is bookkeeping, and every test here is about the difference
// between forgetting a migration and undoing one. The first removes this
// service's account of a run; the second removes what the run created. Confusing
// the two in either direction would be the worst bug this endpoint could have.

// deleteJob asks for one job to be forgotten and returns the ids that went.
func deleteJob(t *testing.T, svc *service.Service, path string) []string {
	t.Helper()

	recorder := call(t, svc, request(t, http.MethodDelete, path, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}

	var ids []string
	for _, id := range decode(t, recorder)["deleted"].([]any) {
		ids = append(ids, fmt.Sprint(id))
	}
	return ids
}

// The reason this exists: a migration that was undone is two records, and a
// listing that lost one of them would show an undo of a migration nobody can
// look up.
func TestDeletingAMigrationTakesTheUndosOfItWithIt(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	migration := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
	})
	migrationID := fmt.Sprint(migration["id"])

	undo := revert1(t, svc, service.RevertRequest{JobID: migrationID})
	undoID := fmt.Sprint(undo["id"])

	went := deleteJob(t, svc, "/migrations/"+migrationID)
	if len(went) != 2 {
		t.Fatalf("deleted %v, want the migration and its undo", went)
	}
	for _, want := range []string{migrationID, undoID} {
		if !contains(went, want) {
			t.Errorf("%s is not among the deleted: %v", want, went)
		}
	}

	if _, err := svc.Record(t.Context(), migrationID); err == nil {
		t.Error("the migration's record is still there")
	}
	if _, err := svc.Record(t.Context(), undoID); err == nil {
		t.Error("the undo's record outlived the migration it undid")
	}
}

// A migration with several undos - a first attempt that failed and a second
// that finished - loses all of them, because they all belong to the run.
func TestEveryUndoOfADeletedMigrationGoes(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	migration := service.Job{
		ID: "20260101T000000Z_migration", Kind: service.KindMigration,
		Status: service.StatusSucceeded, CountsVerified: true,
		RunID:      "20260101T000000Z_migration",
		FinishedAt: "2026-01-01T00:00:00.000Z",
		Request: &service.MigrationRequest{
			SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1"},
	}
	if _, err := svc.CreateRecord(t.Context(), &migration); err != nil {
		t.Fatal(err)
	}

	undos := []string{"20260102T000000Z_first", "20260103T000000Z_second"}
	for _, id := range undos {
		undo := service.Job{
			ID: id, Kind: service.KindRevert, Status: service.StatusFailed,
			FinishedAt: "2026-01-04T00:00:00.000Z",
			RunID:      migration.RunID,
			Revert: &service.RevertRequest{
				TargetTenant: "t-target", RunID: migration.RunID},
		}
		if _, err := svc.CreateRecord(t.Context(), &undo); err != nil {
			t.Fatal(err)
		}
	}

	// And one belonging to a different run, which must survive.
	other := service.Job{
		ID: "20260105T000000Z_other", Kind: service.KindRevert,
		Status: service.StatusSucceeded, FinishedAt: "2026-01-05T00:00:00.000Z",
		RunID:  "20260101T000000Z_elsewhere",
		Revert: &service.RevertRequest{TargetTenant: "t-target", RunID: "20260101T000000Z_elsewhere"},
	}
	if _, err := svc.CreateRecord(t.Context(), &other); err != nil {
		t.Fatal(err)
	}

	went := deleteJob(t, svc, "/migrations/"+migration.ID)
	if len(went) != 3 {
		t.Fatalf("deleted %v, want the migration and both undos", went)
	}
	if _, err := svc.Record(t.Context(), other.ID); err != nil {
		t.Fatalf("an undo of another run was deleted: %v", err)
	}
}

// The whole distinction, said in a test: this endpoint forgets a migration, it
// does not undo one.
func TestDeletingAMigrationLeavesWhatItCreatedInTheTargetTenant(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	migration := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
	})
	created := len(p.tenants["t-target"].Created())
	if created == 0 {
		t.Fatal("the migration created nothing, so this proves nothing")
	}

	deleteJob(t, svc, "/migrations/"+fmt.Sprint(migration["id"]))

	if gone := len(p.tenants["t-target"].Deleted()); gone != 0 {
		t.Fatalf("deleted %d objects from the target tenant; a record delete must touch neither tenant", gone)
	}
}

// An undo whose migration has aged out of the records is a row of its own in the
// listing, so it needs a delete of its own - and it must take nothing else.
func TestDeletingARevertLeavesTheMigrationItUndid(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	migration := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
	})
	migrationID := fmt.Sprint(migration["id"])
	undo := revert1(t, svc, service.RevertRequest{JobID: migrationID})

	went := deleteJob(t, svc, "/reverts/"+fmt.Sprint(undo["id"]))
	if len(went) != 1 {
		t.Fatalf("deleted %v, want only the undo", went)
	}
	if _, err := svc.Record(t.Context(), migrationID); err != nil {
		t.Fatalf("deleting an undo removed the migration it undid: %v", err)
	}
}

// A running job is still writing its record. Deleting it would leave the job
// re-creating nothing and the operator with no account of what it did.
func TestAnUnfinishedJobCannotBeDeleted(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	for _, unfinished := range []service.Job{
		{ID: "20260101T000000Z_running", Kind: service.KindMigration,
			Status: service.StatusRunning,
			Request: &service.MigrationRequest{
				SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1"}},
		{ID: "20260101T000000Z_queued", Kind: service.KindMigration,
			Status: service.StatusQueued,
			Request: &service.MigrationRequest{
				SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1"}},
	} {
		if _, err := svc.CreateRecord(t.Context(), &unfinished); err != nil {
			t.Fatal(err)
		}

		recorder := call(t, svc, request(t, http.MethodDelete, "/migrations/"+unfinished.ID, nil))
		if recorder.Code != http.StatusConflict {
			t.Fatalf("status = %d for a %s job, want 409; body %s",
				recorder.Code, unfinished.Status, recorder.Body)
		}
		if _, err := svc.Record(t.Context(), unfinished.ID); err != nil {
			t.Fatalf("the record of a %s job was deleted anyway: %v", unfinished.Status, err)
		}
	}
}

// A migration whose undo is still going stays too: deleting it would leave the
// undo running against a run nobody can look up.
func TestAMigrationWithARunningUndoCannotBeDeleted(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	migration := service.Job{
		ID: "20260101T000000Z_migration", Kind: service.KindMigration,
		Status: service.StatusSucceeded, CountsVerified: true,
		RunID:      "20260101T000000Z_migration",
		FinishedAt: "2026-01-01T00:00:00.000Z",
		Request: &service.MigrationRequest{
			SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1"},
	}
	undo := service.Job{
		ID: "20260102T000000Z_undo", Kind: service.KindRevert,
		Status: service.StatusRunning, RunID: migration.RunID,
		Revert: &service.RevertRequest{TargetTenant: "t-target", RunID: migration.RunID},
	}
	for _, job := range []*service.Job{&migration, &undo} {
		if _, err := svc.CreateRecord(t.Context(), job); err != nil {
			t.Fatal(err)
		}
	}

	recorder := call(t, svc, request(t, http.MethodDelete, "/migrations/"+migration.ID, nil))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body %s", recorder.Code, recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), undo.ID) {
		t.Errorf("the refusal does not name the undo holding it up: %s", recorder.Body)
	}
	if _, err := svc.Record(t.Context(), migration.ID); err != nil {
		t.Fatalf("the migration was deleted despite the running undo: %v", err)
	}
}

func TestDeletingAJobNobodyHasHeardOfIsNotFound(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodDelete, "/migrations/20990101T000000Z_nope", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body %s", recorder.Code, recorder.Body)
	}
}

// The listing is what the screen reads, so this is the observable end of it.
func TestADeletedMigrationLeavesTheListing(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	migration := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
	})
	id := fmt.Sprint(migration["id"])
	deleteJob(t, svc, "/migrations/"+id)

	listing := call(t, svc, request(t, http.MethodGet, "/migrations", nil))
	if strings.Contains(listing.Body.String(), id) {
		t.Fatalf("the deleted migration is still in the listing: %s", listing.Body)
	}
}
