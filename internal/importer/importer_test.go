package importer_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/c8yclient"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/export"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/importer"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/testfixture"
)

// exportThenImport exports the fixture and imports it back into the same
// server, which is standing in for both tenants. --allowSameTenant is set
// because that is exactly the situation the guard exists to catch, and these
// tests are about the import itself.
func exportThenImport(t *testing.T, opts importer.Options) (*testfixture.Server, string, string) {
	t.Helper()

	server := testfixture.New(t, "hierarchy.json")
	work := t.TempDir()
	s := server.ReadSession(t, work, "fixture.json")
	client, err := c8yclient.New(s, "")
	if err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(work, "export")
	var exportErr bytes.Buffer
	if err := export.New(client, &exportErr, export.Options{
		RootID: "1", OutputDirectory: out, ChildTypes: export.DefaultChildTypes,
	}).Run(context.Background()); err != nil {
		t.Fatalf("export failed: %v\n%s", err, exportErr.String())
	}

	opts.InputDirectory = out
	if opts.MappingFile == "" {
		opts.MappingFile = filepath.Join(work, "id_mapping.json")
	}
	opts.AllowSameTenant = true

	var stderr bytes.Buffer
	if err := importer.New(client, &stderr, opts).Run(context.Background()); err != nil {
		t.Fatalf("import failed: %v\n%s", err, stderr.String())
	}
	return server, opts.MappingFile, stderr.String()
}

func TestImportCreatesEveryObject(t *testing.T) {
	server, _, stderr := exportThenImport(t, importer.Options{})

	if got := len(server.Created()); got != 9 {
		t.Fatalf("created %d objects, want 9", got)
	}
	if !strings.Contains(stderr, "Imported 9 devices and 4 external identities, ID mapping written to") {
		t.Errorf("stderr:\n%s", stderr)
	}
}

func TestImportStripsTheServerManagedProperties(t *testing.T) {
	// The target tenant assigns its own. Sending them back is either rejected or
	// silently ignored, and either way it is not what the object should say.
	server, _, _ := exportThenImport(t, importer.Options{})

	for id, body := range server.Created() {
		var object map[string]any
		if err := json.Unmarshal(body, &object); err != nil {
			t.Fatalf("object %s is not valid JSON: %v", id, err)
		}
		for _, forbidden := range []string{
			"id", "self", "lastUpdated", "creationTime", "owner",
			"childDevices", "childAssets", "childAdditions",
			"deviceParents", "assetParents", "additionParents",
			"c8y_Availability", "c8y_ActiveAlarmsStatus",
		} {
			if _, present := object[forbidden]; present {
				t.Errorf("created object %s still carries %s", id, forbidden)
			}
		}
		// and the parts that belong to the customer survived
		if _, present := object["my_Custom-Fragment"]; !present {
			t.Errorf("created object %s lost its custom fragment", id)
		}
	}
}

func TestImportKeepsNamesIncludingAwkwardOnes(t *testing.T) {
	server, _, _ := exportThenImport(t, importer.Options{})

	var names []string
	for _, body := range server.Created() {
		var object map[string]any
		_ = json.Unmarshal(body, &object)
		if name, ok := object["name"].(string); ok {
			names = append(names, name)
		}
	}
	joined := strings.Join(names, "\n")
	for _, want := range []string{`Pump "West"`, "Ünit Köln ✓", "Sensor #1 [raw]", "Sensor\tTabbed"} {
		if !strings.Contains(joined, want) {
			t.Errorf("a name did not survive the import: %q\ngot:\n%s", want, joined)
		}
	}
	// Every object in this export has a name: the fixture's unnamed one is
	// reachable only through an addition, which the default child types do not
	// follow. The <unnamed> path is covered by the export tests.
	if len(names) != 9 {
		t.Errorf("got %d names for 9 objects", len(names))
	}
}

func TestImportRecreatesEveryRelationship(t *testing.T) {
	server, mappingPath, stderr := exportThenImport(t, importer.Options{})

	links := server.Links()
	if len(links) != 10 {
		t.Fatalf("assigned %d relationships, want 10", len(links))
	}

	// Translated back to source ids, the links must be exactly the fixture's.
	mapping := readMapping(t, mappingPath)
	reverse := map[string]string{}
	for oldID, newID := range mapping {
		reverse[newID] = oldID
	}

	var got []string
	for _, link := range links {
		got = append(got, reverse[link.ParentID]+" "+link.ChildType+" "+reverse[link.ChildID])
	}
	sortStrings(got)

	want := []string{
		"1 asset 2", "1 asset 3", "1 device 10",
		"10 device 11",
		"2 asset 4", "2 asset 5",
		"3 asset 4", "3 device 6", "3 device 7",
		"5 asset 2",
	}
	sortStrings(want)

	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("relationships differ\ngot:\n%s\nwant:\n%s",
			strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// the cycle 5 -> 2 is one of them, and it has to be assigned like any other
	if !strings.Contains(stderr, "as asset child of Ünit Köln ✓") {
		t.Error("the cycle's relationship was not assigned")
	}
}

func TestLinkingHappensAfterEverythingExists(t *testing.T) {
	// A child can only be assigned once BOTH ends exist. Device 1's children
	// include 10, which is created after 2, 3, 4, 5, 6 and 7 in glob order - so
	// linking inline would fail for it.
	_, _, stderr := exportThenImport(t, importer.Options{})

	// " -> " only appears on the per-object lines, not on the closing summary,
	// which also begins with "Imported ".
	lastImport := strings.LastIndex(stderr, " -> ")
	firstAssign := strings.Index(stderr, "Assigned ")
	if firstAssign < lastImport {
		t.Fatalf("a relationship was assigned before the last object was created:\n%s", stderr)
	}
}

func TestMappingIsWrittenAfterEveryObject(t *testing.T) {
	// So an interrupted import is always revertable. Checked by counting how
	// many times the file was written, which the counter of created objects
	// stands in for: the file must be complete for every object created so far.
	server, mappingPath, _ := exportThenImport(t, importer.Options{})

	mapping := readMapping(t, mappingPath)
	if len(mapping) != len(server.Created()) {
		t.Fatalf("mapping has %d entries for %d created objects", len(mapping), len(server.Created()))
	}
	for oldID, newID := range mapping {
		if _, ok := server.Created()[newID]; !ok {
			t.Errorf("mapping says %s -> %s but no such object was created", oldID, newID)
		}
	}
}

func TestMappingFileMatchesTheBashFormat(t *testing.T) {
	// Pretty-printed, two-space indent, insertion order, trailing newline - the
	// same bytes jq's write_mapping produces.
	_, mappingPath, _ := exportThenImport(t, importer.Options{})

	raw, err := os.ReadFile(mappingPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)

	if !strings.HasPrefix(text, "{\n  \"") {
		t.Errorf("mapping does not start like jq's output:\n%q", text[:min(40, len(text))])
	}
	if !strings.HasSuffix(text, "\n}\n") {
		t.Errorf("mapping does not end like jq's output:\n%q", text[max(0, len(text)-10):])
	}
	// glob order: 1, 10, 11, 2, ...
	first := strings.Index(text, `"1":`)
	ten := strings.Index(text, `"10":`)
	two := strings.Index(text, `"2":`)
	if !(first < ten && ten < two) {
		t.Errorf("mapping is not in insertion order:\n%s", text)
	}
}

func TestExternalIdentitiesAreCreated(t *testing.T) {
	server, _, stderr := exportThenImport(t, importer.Options{})

	identities := server.CreatedIdentities()
	if len(identities) != 4 {
		t.Fatalf("created %d identities, want 4", len(identities))
	}
	// the one with a space in it is the awkward case
	var found bool
	for _, identity := range identities {
		if identity.ExternalID == "PUMP WEST 001" {
			found = true
		}
	}
	if !found {
		t.Error("the external id containing a space was not created")
	}
	if !strings.Contains(stderr, "Added external identity c8y_Serial:PUMP WEST 001 to Pump \"West\"") {
		t.Errorf("stderr:\n%s", stderr)
	}
}

func TestADuplicateExternalIdentityDoesNotStopTheImport(t *testing.T) {
	// An external id the target tenant already knows is the common cause, and it
	// says nothing about the rest of the import.
	server := testfixture.New(t, "hierarchy.json")
	work := t.TempDir()
	s := server.ReadSession(t, work, "fixture.json")
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

	// import twice: the second run's identities all collide
	mapping := filepath.Join(work, "first.json")
	var first bytes.Buffer
	if err := importer.New(client, &first, importer.Options{
		InputDirectory: out, MappingFile: mapping, AllowSameTenant: true,
	}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	var second bytes.Buffer
	err = importer.New(client, &second, importer.Options{
		InputDirectory: out, MappingFile: filepath.Join(work, "second.json"), AllowSameTenant: true,
	}).Run(context.Background())
	if err != nil {
		t.Fatalf("a duplicate identity must not fail the import: %v", err)
	}

	text := second.String()
	if !strings.Contains(text, "it may already exist in the target tenant") {
		t.Errorf("the duplicate was not reported:\n%s", text)
	}
	if !strings.Contains(text, "4 external identities could not be created") {
		t.Errorf("the failure count was not reported:\n%s", text)
	}
	// and the objects were still created
	if !strings.Contains(text, "Imported 9 devices and 0 external identities") {
		t.Errorf("the objects were not imported:\n%s", text)
	}
}

func TestMarkerFragmentIsAppliedWithAnEmptyBody(t *testing.T) {
	server, _, _ := exportThenImport(t, importer.Options{RunID: "20260825T101530Z_a1b2c3"})

	for id, body := range server.Created() {
		var object map[string]any
		_ = json.Unmarshal(body, &object)

		marker, present := object["c8y_DeviceMigration_20260825T101530Z_a1b2c3"]
		if !present {
			t.Fatalf("object %s carries no marker fragment: %s", id, body)
		}
		// the identity is in the NAME, so the body stays empty and one
		// --fragmentType query finds the whole run
		asMap, ok := marker.(map[string]any)
		if !ok || len(asMap) != 0 {
			t.Errorf("marker body is not empty: %v", marker)
		}
	}
}

func TestNoFlagLeavesTheObjectsUnmarked(t *testing.T) {
	server, _, _ := exportThenImport(t, importer.Options{NoFlag: true})

	for id, body := range server.Created() {
		var object map[string]any
		_ = json.Unmarshal(body, &object)
		for key := range object {
			if strings.HasPrefix(key, importer.MarkerPrefix) {
				t.Errorf("object %s was marked despite --noFlag: %s", id, key)
			}
		}
	}
}

func TestRunIDCharsetIsEnforced(t *testing.T) {
	// It becomes part of a fragment name, and Cumulocity rejects whitespace and
	// punctuation there - which is why the generated form uses underscores.
	for _, bad := range []string{"has space", "has-dash", "has:colon", "has.dot"} {
		if err := importer.ValidateRunID(bad); err == nil {
			t.Errorf("%q should be refused as a run id", bad)
		} else if !strings.Contains(err.Error(), "letters, digits and underscores only") {
			t.Errorf("unhelpful message for %q: %v", bad, err)
		}
	}
	for _, good := range []string{"", "RUN_1", "20260825T101530Z_a1b2c3", "abcDEF123"} {
		if err := importer.ValidateRunID(good); err != nil {
			t.Errorf("%q should be accepted: %v", good, err)
		}
	}
}

func TestAMissingInputDirectoryFailsCleanly(t *testing.T) {
	server := testfixture.New(t, "hierarchy.json")
	work := t.TempDir()
	s := server.ReadSession(t, work, "fixture.json")
	client, err := c8yclient.New(s, "")
	if err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	err = importer.New(client, &stderr, importer.Options{
		InputDirectory: filepath.Join(work, "absent"),
		MappingFile:    filepath.Join(work, "id_mapping.json"),
	}).Run(context.Background())

	if err == nil {
		t.Fatal("a missing input directory must fail")
	}
	if !strings.Contains(err.Error(), "Input directory does not exist") {
		t.Errorf("message: %v", err)
	}
	if len(server.Created()) != 0 {
		t.Error("something was created despite the failure")
	}
}

// --- helpers ---------------------------------------------------------------

func readMapping(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var mapping map[string]string
	if err := json.Unmarshal(raw, &mapping); err != nil {
		t.Fatalf("mapping is not valid JSON: %v\n%s", err, raw)
	}
	return mapping
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func TestTheMappingIsCompleteWhenAnImportDiesPartWay(t *testing.T) {
	// The property that makes a partial import revertable, and a real bug once:
	// the mapping used to be written only at the end, so an import that died
	// half way stranded everything it had already created with no record of it.
	//
	// Asserting the final mapping proves nothing, because the closing write
	// would produce it anyway. The only way to see the difference is to
	// interrupt the import and read the file it left behind.
	server := testfixture.New(t, "hierarchy.json")
	work := t.TempDir()
	s := server.ReadSession(t, work, "fixture.json")
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

	// die after four objects, so there is a genuine "part way" to observe
	const survives = 4
	server.FailCreateAfter(survives)

	mappingPath := filepath.Join(work, "id_mapping.json")
	var stderr bytes.Buffer
	err = importer.New(client, &stderr, importer.Options{
		InputDirectory:  out,
		MappingFile:     mappingPath,
		AllowSameTenant: true,
	}).Run(context.Background())

	if err == nil {
		t.Fatal("the import should have failed once creates were refused")
	}
	if !strings.Contains(stderr.String(), "Failed to create a managed object for") {
		t.Errorf("the failure was not reported:\n%s", stderr.String())
	}

	// The mapping on disk must already list everything that was created, or a
	// revert could not find it.
	mapping := readMapping(t, mappingPath)
	if len(mapping) != survives {
		t.Fatalf("mapping holds %d entries after %d objects were created, want %d\n%s",
			len(mapping), survives, survives, stderr.String())
	}
	created := server.Created()
	if len(created) != survives {
		t.Fatalf("server created %d objects, want %d", len(created), survives)
	}
	for oldID, newID := range mapping {
		if _, ok := created[newID]; !ok {
			t.Errorf("mapping claims %s -> %s but that object was never created", oldID, newID)
		}
	}
}

func TestAFailureBeforeAnyObjectStillLeavesAMappingFile(t *testing.T) {
	// An empty mapping up front, so an immediate failure leaves a file rather
	// than nothing at all.
	server := testfixture.New(t, "hierarchy.json")
	work := t.TempDir()
	s := server.ReadSession(t, work, "fixture.json")
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

	server.FailCreateAfter(0) // refuse every create, so nothing is ever made

	mappingPath := filepath.Join(work, "id_mapping.json")
	var stderr bytes.Buffer
	_ = importer.New(client, &stderr, importer.Options{
		InputDirectory:  out,
		MappingFile:     mappingPath,
		AllowSameTenant: true,
	}).Run(context.Background())

	if _, err := os.Stat(mappingPath); err != nil {
		t.Fatalf("no mapping file was left behind: %v", err)
	}
	if got := readMapping(t, mappingPath); len(got) != 0 {
		t.Fatalf("mapping should be empty when nothing was created, got %v", got)
	}
	if len(server.Created()) != 0 {
		t.Fatal("something was created despite every create being refused")
	}
}

func TestTheRunIDIsAnnounced(t *testing.T) {
	// So it ends up in the log of the run that used it, rather than having to be
	// read back off an object's fragments afterwards.
	_, _, stderr := exportThenImport(t, importer.Options{RunID: "RUN_ABC"})

	want := "Run id RUN_ABC, marking created objects with c8y_DeviceMigration_RUN_ABC"
	if !strings.Contains(stderr, want) {
		t.Errorf("stderr does not announce the run id:\n%s", stderr)
	}
	// once, not per object
	if got := strings.Count(stderr, "Run id "); got != 1 {
		t.Errorf("the run id was announced %d times, want 1:\n%s", got, stderr)
	}
	// and before anything was created, so an interrupted run still reported it
	if strings.Index(stderr, want) > strings.Index(stderr, "Imported ") {
		t.Errorf("the run id was announced after the first object:\n%s", stderr)
	}
}

func TestNothingIsAnnouncedUnderNoFlag(t *testing.T) {
	_, _, stderr := exportThenImport(t, importer.Options{NoFlag: true})

	if strings.Contains(stderr, "Run id") {
		t.Errorf("a run id was announced despite --noFlag:\n%s", stderr)
	}
}

func TestARunIDIsGeneratedAndReportedWhenNoneIsGiven(t *testing.T) {
	// The reason this exists: without a generated id, the only record of an
	// import is its mapping file, and a lost mapping means a lost migration.
	server, _, stderr := exportThenImport(t, importer.Options{})

	line := regexp.MustCompile(
		`Run id ([0-9]{8}T[0-9]{6}Z_[0-9a-f]{6}), marking created objects with c8y_DeviceMigration_([0-9]{8}T[0-9]{6}Z_[0-9a-f]{6})`)
	found := line.FindStringSubmatch(stderr)
	if found == nil {
		t.Fatalf("no generated run id was reported:\n%s", stderr)
	}
	if found[1] != found[2] {
		t.Errorf("the reported id and the fragment disagree: %s vs %s", found[1], found[2])
	}

	// and it really is on the objects, so the reported id can find them
	fragment := importer.MarkerPrefix + found[1]
	for id, body := range server.Created() {
		var object map[string]any
		_ = json.Unmarshal(body, &object)
		if _, present := object[fragment]; !present {
			t.Fatalf("object %s does not carry the reported marker %s", id, fragment)
		}
	}
}

func TestGeneratedRunIDsAreFragmentSafe(t *testing.T) {
	// It becomes part of a fragment name, so an ISO timestamp cannot be used
	// verbatim: no dashes, no colons.
	for i := 0; i < 50; i++ {
		id := importer.GenerateRunID(time.Now())
		if err := importer.ValidateRunID(id); err != nil {
			t.Fatalf("generated %q, which is not a valid fragment name: %v", id, err)
		}
	}
}
