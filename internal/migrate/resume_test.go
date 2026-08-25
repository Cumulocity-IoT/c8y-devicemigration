package migrate_test

import (
	"os"
	"strings"
	"testing"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/migrate"
)

// Resume exists so a large migration is survivable. An interrupted run
// otherwise means either a duplicate import or a manual revert; with this it
// means running the same command again.

func TestResumeFinishesARunInterruptedInTheHierarchy(t *testing.T) {
	h := setup(t, migrate.Options{Incremental: true})
	h.target.FailCreateAfter(4)

	if _, err := h.run(t, ""); err == nil {
		t.Fatal("the interrupted run should have failed")
	}
	if got := len(h.target.Created()); got != 4 {
		t.Fatalf("created %d objects before the interruption, want 4", got)
	}

	// the same command again, with the run id it reported
	h.target.FailCreateAfter(1000)
	h.opts.Resume = h.runID(t)

	stderr, err := h.run(t, "")
	if err != nil {
		t.Fatalf("the resume failed: %v\n%s", err, stderr)
	}

	if got := len(h.target.Created()); got != 9 {
		t.Fatalf("after the resume there are %d objects, want 9", got)
	}
	if !strings.Contains(stderr, "Reconciling the hierarchy in the target tenant") {
		t.Errorf("stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "Already imported ") {
		t.Errorf("the objects that already existed were not recognised:\n%s", stderr)
	}
}

func TestResumeCreatesNoDuplicateRelationships(t *testing.T) {
	// Interrupted in the DATA phase on purpose, so the hierarchy and all its
	// relationships already exist when the resume runs. An interruption during
	// object creation would leave nothing linked yet, and the resume would
	// assign everything for the first time - which says nothing about
	// duplicates.
	h := setup(t, migrate.Options{DataTypes: allData, Incremental: true})
	h.target.FailDataAfter(2)
	if _, err := h.run(t, ""); err == nil {
		t.Fatal("expected the first run to fail")
	}
	if got := len(h.target.Links()); got != 10 {
		t.Fatalf("the first run assigned %d relationships, want all 10", got)
	}

	h.target.FailDataAfter(100000)
	h.opts.Resume = h.runID(t)
	if _, err := h.run(t, ""); err != nil {
		t.Fatal(err)
	}

	seen := map[string]int{}
	for _, link := range h.target.Links() {
		seen[link.ParentID+" "+link.ChildType+" "+link.ChildID]++
	}
	for link, count := range seen {
		if count > 1 {
			t.Errorf("relationship %q was assigned %d times", link, count)
		}
	}
	if len(h.target.Links()) != 10 {
		t.Errorf("ended with %d relationships, want 10", len(h.target.Links()))
	}
}

func TestResumingAFinishedRunChangesNothing(t *testing.T) {
	h := setup(t, migrate.Options{DataTypes: allData, Incremental: true})
	if _, err := h.run(t, ""); err != nil {
		t.Fatal(err)
	}
	before := describe(t, h)

	h.opts.Resume = h.runID(t)
	stderr, err := h.run(t, "")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, stderr)
	}

	if after := describe(t, h); after != before {
		t.Errorf("resuming a finished run changed the tenant\nbefore: %s\nafter:  %s", before, after)
	}
	if !strings.Contains(stderr, "data already migrated for") {
		t.Errorf("the finished devices were not skipped:\n%s", stderr)
	}
	if strings.Contains(stderr, "Run stopped while migrating") {
		t.Errorf("a finished run reported an interrupted device:\n%s", stderr)
	}
}

func TestResumeRedoesTheDeviceThatWasInFlight(t *testing.T) {
	// A device whose data was in flight is delivered again in full, because
	// there is no per-record account of what arrived. Naming it is the honest
	// thing to do.
	h := setup(t, migrate.Options{DataTypes: allData, Incremental: true})
	h.target.FailDataAfter(2)

	if _, err := h.run(t, ""); err == nil {
		t.Fatal("the interrupted run should have failed")
	}

	interrupted := ""
	for _, line := range h.journalLines(t) {
		if id, ok := strings.CutPrefix(line, "started "); ok {
			if !h.journalHas(t, "data "+id) {
				interrupted = id
			}
		}
	}
	if interrupted == "" {
		t.Fatalf("no device was left in flight:\n%s", strings.Join(h.journalLines(t), "\n"))
	}

	h.target.FailDataAfter(100000)
	h.opts.Resume = h.runID(t)
	stderr, err := h.run(t, "")
	if err != nil {
		t.Fatalf("the resume failed: %v\n%s", err, stderr)
	}

	if !strings.Contains(stderr,
		"Run stopped while migrating data for ("+interrupted+"), so its records are delivered again") {
		t.Errorf("the interrupted device was not named:\n%s", stderr)
	}
	// every device is journalled by the end, so no data was lost
	data := 0
	for _, line := range h.journalLines(t) {
		if strings.HasPrefix(line, "data ") {
			data++
		}
	}
	if data != 9 {
		t.Errorf("journalled %d devices after the resume, want 9", data)
	}
}

func TestResumeRecreatesAnObjectDeletedFromTheTarget(t *testing.T) {
	// A resume trusts the mapping for what exists, so an object deleted in the
	// meantime would be skipped and never recreated. The run marker is what
	// makes checking cheap.
	h := setup(t, migrate.Options{Incremental: true})
	if _, err := h.run(t, ""); err != nil {
		t.Fatal(err)
	}

	var gone string
	for id := range h.target.Created() {
		gone = id
		break
	}
	h.target.Remove(gone)

	h.opts.Resume = h.runID(t)
	stderr, err := h.run(t, "")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, stderr)
	}

	if !strings.Contains(stderr, "is gone from the target, it will be created again") {
		t.Errorf("the deleted object was not noticed:\n%s", stderr)
	}
	if got := len(h.target.Created()); got != 9 {
		t.Errorf("ended with %d objects, want 9", got)
	}
}

func TestWithNoMarkerTheMappingIsTakenAtItsWord(t *testing.T) {
	// --noFlag stamps nothing, so there is no query that could tell a deleted
	// object from a live one.
	h := setup(t, migrate.Options{Incremental: true, NoFlag: true})
	if _, err := h.run(t, ""); err != nil {
		t.Fatal(err)
	}

	var gone string
	for id := range h.target.Created() {
		gone = id
		break
	}
	h.target.Remove(gone)

	h.opts.Resume = h.runID(t)
	stderr, err := h.run(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr, "is gone from the target") {
		t.Errorf("an unmarked run cannot know an object was deleted:\n%s", stderr)
	}
	if got := len(h.target.Created()); got != 8 {
		t.Errorf("ended with %d objects, want the 8 that survived", got)
	}
}

func TestAnUnmarkedIncrementalRunStillReportsItsID(t *testing.T) {
	// Otherwise the run could never be resumed at all.
	h := setup(t, migrate.Options{Incremental: true, NoFlag: true})
	stderr, err := h.run(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "created objects are not marked") {
		t.Errorf("stderr:\n%s", stderr)
	}
	if h.runID(t) == "" {
		t.Error("the journal records no run id")
	}
}

// --- refusing rather than guessing -----------------------------------------

func TestAResumeWithNoJournalIsRefused(t *testing.T) {
	h := setup(t, migrate.Options{Resume: "SOME_RUN"})

	_, err := h.run(t, "")
	if err == nil {
		t.Fatal("a resume with no journal must be refused")
	}
	for _, want := range []string{
		"there is nothing to resume",
		"Only an --incremental migration writes one",
		"could deliver the same records twice",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal is missing %q:\n%s", want, err.Error())
		}
	}
	if len(h.target.Created()) != 0 {
		t.Error("the refusal still created objects")
	}
}

func TestAResumeForADifferentRunIsRefused(t *testing.T) {
	h := setup(t, migrate.Options{Incremental: true})
	if _, err := h.run(t, ""); err != nil {
		t.Fatal(err)
	}

	h.opts.Resume = "DIFFERENT_RUN"
	_, err := h.run(t, "")
	if err == nil {
		t.Fatal("a mismatched run id must be refused")
	}
	if !strings.Contains(err.Error(), "does not belong to run DIFFERENT_RUN") {
		t.Errorf("message: %v", err)
	}
	if !strings.Contains(err.Error(), "It records: runId ") {
		t.Errorf("the refusal does not say which run it is:\n%v", err)
	}
}

func TestAResumeIDThatCannotBeAFragmentNameIsRefused(t *testing.T) {
	for _, bad := range []string{"bad id", "bad-id", "bad:id"} {
		h := setup(t, migrate.Options{Resume: bad})
		_, err := h.run(t, "")
		if err == nil {
			t.Errorf("%q should be refused", bad)
			continue
		}
		if !strings.Contains(err.Error(), "letters, digits and underscores only") {
			t.Errorf("unhelpful message for %q: %v", bad, err)
		}
	}
}

func TestARefusedResumeStagesNothing(t *testing.T) {
	before := stagingDirectories(t)

	h := setup(t, migrate.Options{Resume: "SOME_RUN"})
	if _, err := h.run(t, ""); err == nil {
		t.Fatal("expected a refusal")
	}

	if after := stagingDirectories(t); after != before {
		t.Errorf("a refused resume left a staging directory behind (%d -> %d)", before, after)
	}
}

// --- what a failure says ----------------------------------------------------

func TestAFailurePartWayThroughSaysHowToRevert(t *testing.T) {
	h := setup(t, migrate.Options{})
	h.target.FailCreateAfter(4)

	stderr, err := h.run(t, "")
	if err == nil {
		t.Fatal("expected a failure")
	}
	for _, want := range []string{
		"Staging directory kept at ",
		"so a retry does not have to read the source tenant again",
		"The migration stopped part way through. Undo what it created with:",
		"revert --mapping ",
		"or, if the mapping is lost:",
		"revert --runId ",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr is missing %q:\n%s", want, stderr)
		}
	}
	cleanupStaging(t, stderr)
}

func TestAFailureBeforeAnythingWasCreatedOffersNoRevert(t *testing.T) {
	h := setup(t, migrate.Options{})
	h.target.FailCreateAfter(0) // refuse the very first object

	stderr, err := h.run(t, "")
	if err == nil {
		t.Fatal("expected a failure")
	}
	if strings.Contains(stderr, "stopped part way through") {
		t.Errorf("nothing was created, so there is nothing to undo:\n%s", stderr)
	}
	if strings.Contains(stderr, "revert --mapping") {
		t.Errorf("a revert was offered for an empty mapping:\n%s", stderr)
	}
	cleanupStaging(t, stderr)
}

// cleanupStaging removes a staging directory a failing run deliberately kept.
func cleanupStaging(t *testing.T, stderr string) {
	t.Helper()
	for _, line := range strings.Split(stderr, "\n") {
		if rest, ok := strings.CutPrefix(line, "Staging directory kept at "); ok {
			path, _, _ := strings.Cut(rest, ",")
			_ = os.RemoveAll(strings.TrimSpace(path))
		}
	}
}
