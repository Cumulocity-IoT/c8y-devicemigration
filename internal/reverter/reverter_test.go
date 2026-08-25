package reverter_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/c8yclient"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/export"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/importer"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/reverter"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/session"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/testfixture"
)

// migrated exports the fixture and imports it back into the same server, then
// hands back everything a revert needs. --allowSameTenant, because one server
// stands in for both tenants here.
func migrated(t *testing.T, runID string) (*testfixture.Server, *api.Client, string) {
	t.Helper()

	server := testfixture.New(t, "hierarchy.json")
	work := t.TempDir()
	sessionPath := server.WriteSessionFile(t, work, "fixture.json")
	s, err := session.Read(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	client, err := c8yclient.New(s, "")
	if err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(work, "export")
	var discard bytes.Buffer
	if err := export.New(client, &discard, export.Options{
		RootID: "1", OutputDirectory: out, ChildTypes: export.DefaultChildTypes,
	}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	mappingPath := filepath.Join(work, "id_mapping.json")
	if err := importer.New(client, &discard, importer.Options{
		InputDirectory: out, MappingFile: mappingPath,
		RunID: runID, AllowSameTenant: true,
	}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	return server, client, mappingPath
}

func revert(t *testing.T, client *api.Client, stdin string, opts reverter.Options) (string, error) {
	t.Helper()
	var stderr bytes.Buffer
	err := reverter.New(client, &stderr, strings.NewReader(stdin), opts).Run(context.Background())
	return stderr.String(), err
}

// --- the plan ---------------------------------------------------------------

func TestThePlanListsEverythingBeforeAnythingIsDeleted(t *testing.T) {
	server, client, mapping := migrated(t, "")

	// A declined confirmation, so the plan is all that happens.
	out, err := revert(t, client, "no\n", reverter.Options{MappingFile: mapping})
	if !errors.Is(err, reverter.ErrIncomplete) {
		t.Fatalf("a declined revert should be incomplete, got %v", err)
	}

	for _, want := range []string{
		"The following managed objects will be deleted from the active session:",
		"9 managed objects will be deleted, 0 are already absent.",
		"Their external identities and parent/child relationships go with them.",
		"Deleting a group also propagates to its subgroups, which may remove",
		"objects that this import did not create.",
		"Nothing was deleted",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan is missing %q:\n%s", want, out)
		}
	}
	if len(server.Created()) != 9 {
		t.Errorf("a declined revert deleted something: %d objects left", len(server.Created()))
	}
	if len(server.Deleted()) != 0 {
		t.Errorf("a declined revert still sent deletes: %v", server.Deleted())
	}
}

func TestThePlanNamesTheSourceObject(t *testing.T) {
	_, client, mapping := migrated(t, "")

	out, _ := revert(t, client, "no\n", reverter.Options{MappingFile: mapping})
	if !strings.Contains(out, "imported from 1)") {
		t.Errorf("the plan does not say where an object came from:\n%s", out)
	}
	// and the awkward names survive into the plan
	if !strings.Contains(out, `delete Pump "West" (`) {
		t.Errorf("a quoted name is mangled in the plan:\n%s", out)
	}
}

func TestObjectsAlreadyGoneAreListedSeparately(t *testing.T) {
	server, client, mapping := migrated(t, "")

	// Delete two behind the revert's back, which is what a rerun after a
	// partial revert looks like.
	var gone []string
	for id := range server.Created() {
		gone = append(gone, id)
		if len(gone) == 2 {
			break
		}
	}
	server.Remove(gone...)

	out, _ := revert(t, client, "no\n", reverter.Options{MappingFile: mapping})
	for _, want := range []string{
		"Already absent, nothing to do for these:",
		"7 managed objects will be deleted, 2 are already absent.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan is missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "  skip   ") {
		t.Errorf("absent objects are not listed as skipped:\n%s", out)
	}
}

// --- deleting ---------------------------------------------------------------

func TestForceDeletesEverythingInThePlan(t *testing.T) {
	server, client, mapping := migrated(t, "")

	out, err := revert(t, client, "", reverter.Options{MappingFile: mapping, Force: true})
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if got := len(server.Created()); got != 0 {
		t.Fatalf("%d objects survived the revert", got)
	}
	if !strings.Contains(out, "Deleted 9 of 9 managed objects, 0 are still present") {
		t.Errorf("summary:\n%s", out)
	}
	if !strings.Contains(out, "Deleted Plant Berlin (") {
		t.Errorf("per-object outcome is missing:\n%s", out)
	}
	// nothing was asked, because --force was given
	if strings.Contains(out, "Type \"yes\"") {
		t.Errorf("--force still asked for confirmation:\n%s", out)
	}
}

func TestYesAtThePromptDeletes(t *testing.T) {
	server, client, mapping := migrated(t, "")

	out, err := revert(t, client, "yes\n", reverter.Options{MappingFile: mapping})
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if got := len(server.Created()); got != 0 {
		t.Fatalf("%d objects survived", got)
	}
	if !strings.Contains(out, "Type \"yes\" to delete these managed objects:") {
		t.Errorf("no prompt was shown:\n%s", out)
	}
}

func TestAnythingButYesDeclines(t *testing.T) {
	for _, answer := range []string{"no\n", "y\n", "YES\n", "\n", ""} {
		server, client, mapping := migrated(t, "")
		out, err := revert(t, client, answer, reverter.Options{MappingFile: mapping})
		if !errors.Is(err, reverter.ErrIncomplete) {
			t.Errorf("answer %q should decline, got %v", answer, err)
		}
		if len(server.Created()) != 9 {
			t.Errorf("answer %q deleted something", answer)
		}
		_ = out
	}
}

func TestPropagationIsReportedAsDeletedNotFailed(t *testing.T) {
	// Cumulocity removes a group's subgroups with it, so an object can be gone
	// before its own request is sent. The outcome is established by looking, not
	// by trusting the response, which is exactly so this reads correctly.
	server, client, mapping := migrated(t, "")

	created := server.Created()
	var ids []string
	for id := range created {
		ids = append(ids, id)
	}
	sortStrings(ids)
	// the first object takes two others with it
	server.Cascade(ids[0], ids[1], ids[2])

	out, err := revert(t, client, "", reverter.Options{MappingFile: mapping, Force: true})
	if err != nil {
		t.Fatalf("propagation must not make a revert incomplete: %v\n%s", err, out)
	}
	if got := len(server.Created()); got != 0 {
		t.Fatalf("%d objects survived", got)
	}
	if !strings.Contains(out, "Deleted 9 of 9 managed objects, 0 are still present") {
		t.Errorf("propagated deletions were not counted as deleted:\n%s", out)
	}
	if strings.Contains(out, "Still present:") {
		t.Errorf("something was wrongly reported as surviving:\n%s", out)
	}
}

func TestAnObjectThatSurvivesMakesTheRevertIncomplete(t *testing.T) {
	server, client, mapping := migrated(t, "")

	// One object refuses to go, which is what a permissions problem looks like.
	var stubborn string
	for id := range server.Created() {
		stubborn = id
		break
	}
	server.RefuseDelete(stubborn)

	out, err := revert(t, client, "", reverter.Options{MappingFile: mapping, Force: true})
	if !errors.Is(err, reverter.ErrIncomplete) {
		t.Fatalf("a surviving object must make the revert incomplete, got %v", err)
	}
	if !strings.Contains(out, "Deleted 8 of 9 managed objects, 1 are still present") {
		t.Errorf("summary:\n%s", out)
	}
	if !strings.Contains(out, "Still present: ") {
		t.Errorf("the survivor was not named:\n%s", out)
	}
	if !strings.Contains(out, "The revert is incomplete, rerun it to retry the remaining objects") {
		t.Errorf("the retry advice is missing:\n%s", out)
	}
}

// --- reverting by run marker ------------------------------------------------

func TestRunIDFindsTheObjectsOfThatRun(t *testing.T) {
	server, client, _ := migrated(t, "RUN_ONE")

	out, err := revert(t, client, "", reverter.Options{RunID: "RUN_ONE", Force: true})
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if got := len(server.Created()); got != 0 {
		t.Fatalf("%d objects survived", got)
	}
	// No source id to report: the marker body is empty by design.
	if strings.Contains(out, "imported from") {
		t.Errorf("a marker plan cannot know where an object came from:\n%s", out)
	}
}

func TestRunIDLeavesAnotherRunAlone(t *testing.T) {
	// The whole point of putting the run identity in the fragment name.
	server, client, mapping := migrated(t, "RUN_ONE")

	// A second run of the same export into the same tenant, marked differently.
	work := t.TempDir()
	out := filepath.Join(work, "export")
	var discard bytes.Buffer
	if err := export.New(client, &discard, export.Options{
		RootID: "1", OutputDirectory: out, ChildTypes: export.DefaultChildTypes,
	}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := importer.New(client, &discard, importer.Options{
		InputDirectory: out, MappingFile: filepath.Join(work, "second.json"),
		RunID: "RUN_TWO", AllowSameTenant: true,
	}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(server.Created()); got != 18 {
		t.Fatalf("expected 18 objects across two runs, got %d", got)
	}
	_ = mapping

	text, err := revert(t, client, "", reverter.Options{RunID: "RUN_ONE", Force: true})
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, text)
	}
	if got := len(server.Created()); got != 9 {
		t.Fatalf("reverting one run left %d objects, want the other run's 9", got)
	}
	if !strings.Contains(text, "Deleted 9 of 9 managed objects") {
		t.Errorf("summary:\n%s", text)
	}
}

func TestAnUnknownRunIDHasNothingToDo(t *testing.T) {
	server, client, _ := migrated(t, "RUN_ONE")

	out, err := revert(t, client, "", reverter.Options{RunID: "RUN_ABSENT", Force: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "No managed object carries c8y_DeviceMigration_RUN_ABSENT, nothing to revert") {
		t.Errorf("output:\n%s", out)
	}
	if len(server.Created()) != 9 {
		t.Error("something was deleted for an unknown run")
	}
}

func TestARunIDThatCannotBeAFragmentNameIsRefused(t *testing.T) {
	_, client, _ := migrated(t, "")

	for _, bad := range []string{"bad id", "bad-id", "bad:id"} {
		_, err := revert(t, client, "", reverter.Options{RunID: bad, Force: true})
		if err == nil {
			t.Errorf("%q should be refused", bad)
			continue
		}
		if !strings.Contains(err.Error(), "may only contain letters, digits and underscores") {
			t.Errorf("unhelpful message for %q: %v", bad, err)
		}
	}
}

func TestUnmarkedObjectsCannotBeFoundByRunID(t *testing.T) {
	// An import run without a run id marks nothing, so there is nothing to find.
	// Worth saying out loud rather than discovering during an incident.
	server, client, _ := migrated(t, "")

	out, err := revert(t, client, "", reverter.Options{RunID: "RUN_ONE", Force: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "nothing to revert") {
		t.Errorf("output:\n%s", out)
	}
	if len(server.Created()) != 9 {
		t.Error("unmarked objects were deleted by a marker revert")
	}
}

// --- the mapping file -------------------------------------------------------

func TestAMissingMappingFileFailsCleanly(t *testing.T) {
	_, client, _ := migrated(t, "")

	_, err := revert(t, client, "", reverter.Options{
		MappingFile: filepath.Join(t.TempDir(), "absent.json"), Force: true})
	if err == nil {
		t.Fatal("a missing mapping must fail")
	}
	if !strings.Contains(err.Error(), "Mapping file does not exist") {
		t.Errorf("message: %v", err)
	}
}

func TestAMappingThatIsNotAnObjectIsRefused(t *testing.T) {
	_, client, _ := migrated(t, "")

	path := filepath.Join(t.TempDir(), "wrong.json")
	if err := os.WriteFile(path, []byte("[1,2,3]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := revert(t, client, "", reverter.Options{MappingFile: path, Force: true})
	if err == nil {
		t.Fatal("a JSON array is not a mapping")
	}
	if !strings.Contains(err.Error(), "Mapping file is not a JSON object") {
		t.Errorf("message: %v", err)
	}
}

func TestAnEmptyMappingHasNothingToDo(t *testing.T) {
	_, client, _ := migrated(t, "")

	path := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := revert(t, client, "", reverter.Options{MappingFile: path, Force: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "The mapping file lists no objects, nothing to revert") {
		t.Errorf("output:\n%s", out)
	}
}

func TestAMappingWhoseObjectsAreAllGoneHasNothingToDelete(t *testing.T) {
	server, client, mapping := migrated(t, "")

	var all []string
	for id := range server.Created() {
		all = append(all, id)
	}
	server.Remove(all...)

	out, err := revert(t, client, "", reverter.Options{MappingFile: mapping, Force: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Nothing to delete") {
		t.Errorf("output:\n%s", out)
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func TestTheMarkerQueryReallyFilters(t *testing.T) {
	// A revert that plans to delete a whole tenant is the worst bug this tool
	// could have, and it looks exactly like a filter that never reached the
	// server. So assert on the request, not just on the outcome.
	server, client, _ := migrated(t, "RUN_ONE")

	var stderr bytes.Buffer
	_ = reverter.New(client, &stderr, strings.NewReader(""), reverter.Options{
		RunID: "RUN_ONE", Force: true,
	}).Run(context.Background())

	var found string
	for _, query := range server.Queries() {
		if strings.HasPrefix(query, "/inventory/managedObjects?") {
			found = query
			break
		}
	}
	if found == "" {
		t.Fatalf("no inventory query was sent at all:\n%v", server.Queries())
	}
	t.Logf("marker query: %s", found)

	if !strings.Contains(found, "c8y_DeviceMigration_RUN_ONE") {
		t.Fatalf("the marker never reached the server, so this query selects the whole tenant:\n%s", found)
	}

	// The condition has to be INSIDE the query. Cumulocity ignores
	// fragmentType whenever a query parameter is present, and paging adds one -
	// so a fragmentType filter would be silently dropped and the search would
	// return the whole tenant.
	if strings.Contains(found, "fragmentType=") {
		t.Errorf("fragmentType is sent alongside a query, where the platform ignores it:\n%s", found)
	}
	if !strings.Contains(found, "has%28c8y_DeviceMigration_RUN_ONE%29") {
		t.Errorf("the marker is not expressed as has(...) inside the query:\n%s", found)
	}
}
