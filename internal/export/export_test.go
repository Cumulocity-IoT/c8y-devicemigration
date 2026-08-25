package export_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/c8yclient"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/export"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/session"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/testfixture"
)

// run exports the fixture hierarchy and returns the output directory and stderr.
func run(t *testing.T, fixture string, opts export.Options) (string, string, error) {
	t.Helper()

	server := testfixture.New(t, fixture)
	dir := t.TempDir()
	sessionPath := server.WriteSessionFile(t, dir, "fixture.json")

	s, err := session.Read(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	client, err := c8yclient.New(s, "")
	if err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "out")
	opts.OutputDirectory = out

	var stderr bytes.Buffer
	err = export.New(client, &stderr, opts).Run(context.Background())
	return out, stderr.String(), err
}

// runAgainst is run() for a server the caller has already configured, e.g. with
// an inflated record count.
func runAgainst(t *testing.T, server *testfixture.Server, opts export.Options) (string, string, error) {
	t.Helper()

	dir := t.TempDir()
	sessionPath := server.WriteSessionFile(t, dir, "fixture.json")
	s, err := session.Read(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	client, err := c8yclient.New(s, "")
	if err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "out")
	opts.OutputDirectory = out

	// Building the client makes a request of its own - it fetches a token and
	// resolves the tenant - so a caller counting requests has to measure from
	// here rather than from zero.
	requestsBefore = len(server.Requests())

	var stderr bytes.Buffer
	err = export.New(client, &stderr, opts).Run(context.Background())
	return out, stderr.String(), err
}

// requestsBefore is how many requests the server had seen when the last
// runAgainst built its client.
var requestsBefore int

// exportedIDs lists the ids that got an object file, sorted numerically-ish.
func exportedIDs(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "exported_device_*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range matches {
		base := filepath.Base(m)
		base = strings.TrimPrefix(base, "exported_device_")
		ids = append(ids, strings.TrimSuffix(base, ".json"))
	}
	sort.Strings(ids)
	return ids
}

func TestExportsTheWholeHierarchyOnce(t *testing.T) {
	// The fixture has a cycle (1 -> 2 -> 5 -> 2) and a node with two parents
	// (4 under both 2 and 3). Both must terminate and neither may duplicate.
	dir, _, err := run(t, "hierarchy.json", export.Options{RootID: "1", ChildTypes: export.DefaultChildTypes})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := exportedIDs(t, dir)
	want := []string{"1", "10", "11", "2", "3", "4", "5", "6", "7"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("exported %v, want %v", got, want)
	}
}

func TestAdditionsAreNotFollowedByDefault(t *testing.T) {
	// 8 and 9 are reachable only through an addition, so the default child
	// types must not reach them.
	dir, _, err := run(t, "hierarchy.json", export.Options{RootID: "1", ChildTypes: export.DefaultChildTypes})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, id := range []string{"8", "9"} {
		if _, statErr := os.Stat(filepath.Join(dir, "exported_device_"+id+".json")); statErr == nil {
			t.Errorf("device %s was exported but additions were not requested", id)
		}
	}
}

func TestAdditionsAreFollowedWhenAsked(t *testing.T) {
	dir, _, err := run(t, "hierarchy.json", export.Options{
		RootID:     "1",
		ChildTypes: []export.ChildType{export.ChildAsset, export.ChildDevice, export.ChildAddition},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(exportedIDs(t, dir)) != 11 {
		t.Fatalf("exported %v, want all 11", exportedIDs(t, dir))
	}
}

func TestNoChildrenExportsOnlyTheRoot(t *testing.T) {
	dir, _, err := run(t, "hierarchy.json", export.Options{RootID: "1"} /* no child types: --no-children */)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := exportedIDs(t, dir); len(got) != 1 || got[0] != "1" {
		t.Fatalf("exported %v, want just the root", got)
	}
	// and no child index files at all
	matches, _ := filepath.Glob(filepath.Join(dir, "*_child_*.json"))
	if len(matches) != 0 {
		t.Fatalf("wrote child index files with no children requested: %v", matches)
	}
}

func TestChildIndexFilesMatchJqFormatting(t *testing.T) {
	// These files are compared byte for byte against the bash export, so the
	// two-space indent and the trailing newline are part of the contract.
	dir, _, err := run(t, "hierarchy.json", export.Options{RootID: "1", ChildTypes: export.DefaultChildTypes})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "1_child_asset.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := "[\n  \"2\",\n  \"3\"\n]\n"
	if string(raw) != want {
		t.Fatalf("got %q, want %q", raw, want)
	}
}

func TestIdentityFilesKeepTypeBeforeExternalID(t *testing.T) {
	// jq emits the keys in the order the map literal gives them, which is type
	// first even though the c8y --select order is the reverse.
	dir, _, err := run(t, "hierarchy.json", export.Options{RootID: "1", ChildTypes: export.DefaultChildTypes})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "2_identities.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := "[\n" +
		"  {\n    \"type\": \"c8y_Serial\",\n    \"externalId\": \"LINE-A-SN\"\n  },\n" +
		"  {\n    \"type\": \"c8y_ExternalId\",\n    \"externalId\": \"line-a@example\"\n  }\n" +
		"]\n"
	if string(raw) != want {
		t.Fatalf("got:\n%q\nwant:\n%q", raw, want)
	}
}

func TestIdentityFileIsAbsentWhenThereAreNone(t *testing.T) {
	// Its presence has to mean something, so it is not written empty.
	dir, _, err := run(t, "hierarchy.json", export.Options{RootID: "1", ChildTypes: export.DefaultChildTypes})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "3_identities.json")); statErr == nil {
		t.Error("device 3 has no identities but got an identity file")
	}
}

func TestObjectFileIsTheResponseVerbatim(t *testing.T) {
	// Nothing is stripped or reformatted on the way out: import decides what to
	// strip, and re-serialising would rewrite numbers and drop explicit nulls.
	dir, _, err := run(t, "hierarchy.json", export.Options{RootID: "1", ChildTypes: export.DefaultChildTypes})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "exported_device_1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("object file is not valid JSON: %v", err)
	}
	// the server managed properties an import has to strip are still present
	for _, key := range []string{"id", "self", "lastUpdated", "creationTime", "owner", "childAssets"} {
		if _, ok := object[key]; !ok {
			t.Errorf("%s was stripped by the export, which is import's job", key)
		}
	}
	if object["name"] != "Plant Berlin" {
		t.Errorf("name = %v", object["name"])
	}
}

func TestUnnamedDeviceIsReportedAsUnnamed(t *testing.T) {
	// Device 8 has no name at all.
	_, stderr, err := run(t, "hierarchy.json", export.Options{
		RootID:     "8",
		ChildTypes: []export.ChildType{export.ChildAsset, export.ChildDevice, export.ChildAddition},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stderr, "Exported <unnamed> (8)") {
		t.Fatalf("stderr does not report the unnamed device:\n%s", stderr)
	}
}

func TestProgressLineCountsChildrenAndIdentities(t *testing.T) {
	_, stderr, err := run(t, "hierarchy.json", export.Options{RootID: "1", ChildTypes: export.DefaultChildTypes})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{
		"Exported Plant Berlin (1), 2 asset children, 1 device child, 1 identity",
		"Exported Line A (2), 2 asset children, 2 identities",
		"Exported 9 managed objects and 4 external identities to ",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr is missing %q:\n%s", want, stderr)
		}
	}
}

func TestChildLimitIsRefusedAndNothingIsWritten(t *testing.T) {
	// The wide fixture is a root with 30 leaf children, over the limit of 25.
	dir, _, err := run(t, "hierarchy-wide.json", export.Options{RootID: "1", ChildTypes: export.DefaultChildTypes})
	if err == nil {
		t.Fatal("an oversized hierarchy must be refused")
	}
	for _, want := range []string{
		"Export limits exceeded:",
		"child objects",
		"30",
		"25",
		"Nothing was exported",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal is missing %q:\n%s", want, err.Error())
		}
	}
	// and the refusal really did leave nothing behind
	if _, statErr := os.Stat(dir); statErr == nil {
		t.Error("the output directory was created despite the refusal")
	}
}

func TestIgnoreLimitsExportsAnyway(t *testing.T) {
	dir, stderr, err := run(t, "hierarchy-wide.json", export.Options{
		RootID: "1", ChildTypes: export.DefaultChildTypes, IgnoreLimits: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stderr, "Exporting anyway because --ignoreLimits was given.") {
		t.Errorf("stderr does not say the limits were ignored:\n%s", stderr)
	}
	if !strings.Contains(stderr, "WARNING: --ignoreLimits is set") {
		t.Errorf("stderr does not warn about --ignoreLimits:\n%s", stderr)
	}
	if got := len(exportedIDs(t, dir)); got != 31 {
		t.Fatalf("exported %d objects, want 31", got)
	}
}

func TestTwoParentNodeIsFetchedOnce(t *testing.T) {
	// Device 4 is a child of both 2 and 3. Exporting it twice would be
	// wasteful; writing it twice would be a bug.
	server := testfixture.New(t, "hierarchy.json")
	dir := t.TempDir()
	sessionPath := server.WriteSessionFile(t, dir, "fixture.json")
	s, err := session.Read(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	client, err := c8yclient.New(s, "")
	if err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	if err := export.New(client, &stderr, export.Options{
		RootID:          "1",
		OutputDirectory: filepath.Join(dir, "out"),
		ChildTypes:      export.DefaultChildTypes,
	}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The limits pre-flight lists children but never fetches an object, so the
	// object itself is read exactly once however many parents point at it.
	if got := server.CountExact("/inventory/managedObjects/4"); got != 1 {
		t.Fatalf("device 4 was fetched %d times, want 1", got)
	}
	if got := strings.Count(stderr.String(), "Exported Pump"); got != 1 {
		t.Fatalf("device 4 was reported %d times, want 1", got)
	}
}

func TestObjectBodyIsPassedThroughByteForByte(t *testing.T) {
	// The reason export writes response bytes rather than re-encoding them.
	//
	// Go's encoding/json renders float64(600) as "600", so a round trip through
	// map[string]any would rewrite 600.0 and lose the distinction between an
	// absent field and an explicit null. tests/fixtures/export-9object holds
	// both, captured from a real tenant, so this is a real customer's data being
	// silently changed - not a hypothetical.
	//
	// The differential test cannot catch this on its own: the fake pretty-prints
	// with the same two-space indent Go would use, so re-encoding is invisible
	// there. This asserts the property directly.
	const body = `{
  "id": "1",
  "name": "Precision",
  "rounded": 600.0,
  "big": 12345678901234567890,
  "explicitNull": null,
  "nested": {"trailing": 1800.0, "alsoNull": null},
  "@odd_Fragment": {"expr": "countEvents(\"37218800\",\"Piece_Ok\")"}
}`

	server := testfixture.New(t, "hierarchy.json")
	server.SetObjectBody("1", []byte(body))

	dir := t.TempDir()
	sessionPath := server.WriteSessionFile(t, dir, "fixture.json")
	s, err := session.Read(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	client, err := c8yclient.New(s, "")
	if err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "out")
	var stderr bytes.Buffer
	if err := export.New(client, &stderr, export.Options{
		RootID: "1", OutputDirectory: out,
	}).Run(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	written, err := os.ReadFile(filepath.Join(out, "exported_device_1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != body {
		t.Errorf("the object body was rewritten on the way to disk\nserved:\n%s\nwritten:\n%s",
			body, written)
	}

	// and spell out the specific losses, so a failure says what broke
	for _, fragile := range []string{"600.0", "1800.0", "12345678901234567890", `"explicitNull": null`} {
		if !strings.Contains(string(written), fragile) {
			t.Errorf("%s did not survive the export", fragile)
		}
	}
}

func TestExportUsesTheActiveSessionWhenNoneIsNamed(t *testing.T) {
	// The point of the change: run `eval "$(c8y sessions set ...)"` and then
	// export without naming a session. Modelled here by setting the same
	// variables that activation exports, with the credentials in the clear.
	server := testfixture.New(t, "hierarchy.json")

	for _, name := range []string{
		"C8Y_BASEURL", "C8Y_URL", "C8Y_HOST", "C8Y_TENANT",
		"C8Y_USERNAME", "C8Y_USER", "C8Y_PASSWORD", "C8Y_TOKEN", "C8Y_SESSION",
	} {
		t.Setenv(name, "")
		_ = os.Unsetenv(name)
	}
	t.Setenv("C8Y_HOST", server.URL)
	t.Setenv("C8Y_TENANT", "t-fixture")
	t.Setenv("C8Y_USER", "fixture")
	t.Setenv("C8Y_PASSWORD", "fixture")

	// No --session, and deliberately no C8Y_PASSPHRASE: an activated session
	// exports its credentials already decrypted.
	s, origin, err := session.Current("", "")
	if err != nil {
		t.Fatalf("the active session was not picked up: %v", err)
	}
	if origin != session.OriginEnvironment {
		t.Fatalf("origin = %v, want the active session", origin)
	}

	client, err := c8yclient.New(s, "")
	if err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "out")
	var stderr bytes.Buffer
	if err := export.New(client, &stderr, export.Options{
		RootID: "1", OutputDirectory: out, ChildTypes: export.DefaultChildTypes,
	}).Run(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := len(exportedIDs(t, out)); got != 9 {
		t.Fatalf("exported %d objects via the active session, want 9", got)
	}
	if !strings.Contains(stderr.String(), "Exported 9 managed objects and 4 external identities") {
		t.Fatalf("stderr:\n%s", stderr.String())
	}
}
