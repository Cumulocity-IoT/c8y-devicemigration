package importer_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/c8yclient"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/importer"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/session"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/testfixture"
)

// The differential test for import.
//
// Unlike export, the interesting output is not files on disk: it is what arrived
// at the tenant. So this compares three things - the mapping file byte for
// byte, the stderr line for line, and the created objects, identities and
// relationships semantically.
//
// Request bodies are compared as parsed JSON rather than as bytes on purpose.
// The bash side builds them with jq and this one edits the raw document with
// sjson, so the whitespace differs; whitespace in a request body is not
// behaviour. What must not differ is the content, including 600.0 staying 600.0,
// which internal/export's byte-for-byte test covers directly.

func requireBashAndJqForImport(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed, so the bash implementation cannot be run", tool)
		}
	}
}

// bashImport runs commands/export then commands/import against the fake, and
// returns the export directory, the mapping file, the fake's record directory
// and the import's stderr.
func bashImport(t *testing.T, extra ...string) (mappingPath, rec, stderr string) {
	t.Helper()

	repo := testfixture.RepoRoot()
	work := t.TempDir()
	rec = filepath.Join(work, "rec")
	if err := os.MkdirAll(rec, 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(work, "export")

	env := append(os.Environ(),
		"PATH="+filepath.Join(repo, "tests", "fake")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_C8Y_REC="+rec,
		"FAKE_C8Y_HIERARCHY="+testfixture.FixturePath("hierarchy.json"),
	)

	exportCmd := exec.Command("bash", filepath.Join(repo, "commands", "export"),
		"--rootId", "1", "--output-directory", out)
	exportCmd.Dir = work
	exportCmd.Env = env
	if output, err := exportCmd.CombinedOutput(); err != nil {
		t.Fatalf("bash export failed: %v\n%s", err, output)
	}

	mappingPath = filepath.Join(work, "id_mapping.json")
	argv := append([]string{filepath.Join(repo, "commands", "import"),
		"--input-directory", out, "--mapping", mappingPath, "--force"}, extra...)
	importCmd := exec.Command("bash", argv...)
	importCmd.Dir = work
	importCmd.Env = env

	var errBuf bytes.Buffer
	importCmd.Stderr = &errBuf
	if err := importCmd.Run(); err != nil {
		t.Fatalf("bash import failed: %v\n%s", err, errBuf.String())
	}
	return mappingPath, rec, errBuf.String()
}

// goImport runs the Go export and import against a server replaying the fake's
// object bodies, so both sides start from identical artefacts.
func goImport(t *testing.T, opts importer.Options) (mappingPath string, server *testfixture.Server, stderr string) {
	t.Helper()

	server = testfixture.New(t, "hierarchy.json")
	for id, body := range objectBodiesFromFakeForImport(t, server.IDs()) {
		server.SetObjectBody(id, body)
	}

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
	if err := runGoExportInto(t, client, out); err != nil {
		t.Fatal(err)
	}

	mappingPath = filepath.Join(work, "id_mapping.json")
	opts.InputDirectory = out
	opts.MappingFile = mappingPath
	opts.AllowSameTenant = true

	var errBuf bytes.Buffer
	if err := importer.New(client, &errBuf, opts).Run(context.Background()); err != nil {
		t.Fatalf("go import failed: %v\n%s", err, errBuf.String())
	}
	return mappingPath, server, errBuf.String()
}

func TestDifferentialImportMappingAndOutput(t *testing.T) {
	requireBashAndJqForImport(t)

	// Both sides generate a run id when none is given, and two runs would
	// generate different ones. --noFlag takes marking out of the comparison
	// entirely; the marker itself is compared in its own test below.
	bashMapping, rec, bashErr := bashImport(t, "--noFlag")
	goMapping, server, goErr := goImport(t, importer.Options{NoFlag: true})

	// The mapping is an artefact, so it is compared byte for byte.
	bashBytes, err := os.ReadFile(bashMapping)
	if err != nil {
		t.Fatal(err)
	}
	goBytes, err := os.ReadFile(goMapping)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bashBytes, goBytes) {
		t.Errorf("mapping files differ\nbash:\n%s\ngo:\n%s", bashBytes, goBytes)
	}

	// The closing line names the mapping file, which legitimately differs: the
	// two runs write into their own temporary directories.
	if got, want := normalise(goErr, goMapping), normalise(bashErr, bashMapping); got != want {
		t.Errorf("stderr differs\nbash:\n%s\ngo:\n%s", want, got)
	}

	// The relationships, named by source id on both sides.
	if got, want := goLinks(t, goMapping, server), bashLinks(t, rec, bashMapping); !reflect.DeepEqual(got, want) {
		t.Errorf("relationships differ\nbash: %v\ngo:   %v", want, got)
	}

	// The external identities, named by source id on both sides.
	if got, want := goIdentities(t, goMapping, server), bashIdentities(t, rec, bashMapping); !reflect.DeepEqual(got, want) {
		t.Errorf("external identities differ\nbash: %v\ngo:   %v", want, got)
	}
}

func TestDifferentialImportCreatesTheSameBodies(t *testing.T) {
	requireBashAndJqForImport(t)

	_, rec, _ := bashImport(t, "--noFlag")
	_, server, _ := goImport(t, importer.Options{NoFlag: true})

	bashBodies := bashCreatedBodies(t, rec)
	goBodies := map[string]any{}
	for _, body := range server.Created() {
		var object map[string]any
		if err := json.Unmarshal(body, &object); err != nil {
			t.Fatalf("a created body is not valid JSON: %v\n%s", err, body)
		}
		name, _ := object["name"].(string)
		goBodies[name] = object
	}

	if len(goBodies) != len(bashBodies) {
		t.Fatalf("created %d bodies, bash created %d", len(goBodies), len(bashBodies))
	}
	for name, wanted := range bashBodies {
		got, ok := goBodies[name]
		if !ok {
			t.Errorf("no object named %q was created", name)
			continue
		}
		if !reflect.DeepEqual(got, wanted) {
			t.Errorf("the body for %q differs\nbash: %v\ngo:   %v", name, wanted, got)
		}
	}
}

func TestDifferentialImportWithARunMarker(t *testing.T) {
	requireBashAndJqForImport(t)

	const runID = "20260825T101530Z_a1b2c3"
	bashMapping, rec, bashErr := bashImport(t, "--runId", runID)
	goMapping, server, goErr := goImport(t, importer.Options{RunID: runID})

	if got, want := normalise(goErr, goMapping), normalise(bashErr, bashMapping); got != want {
		t.Errorf("stderr differs\nbash:\n%s\ngo:\n%s", want, got)
	}

	// Both must stamp the same fragment, with an empty body, on everything.
	fragment := importer.MarkerPrefix + runID
	for _, body := range server.Created() {
		var object map[string]any
		_ = json.Unmarshal(body, &object)
		marker, ok := object[fragment]
		if !ok {
			t.Fatalf("the Go import did not stamp %s: %s", fragment, body)
		}
		if asMap, isMap := marker.(map[string]any); !isMap || len(asMap) != 0 {
			t.Errorf("marker body is not empty: %v", marker)
		}
	}
	for name, object := range bashCreatedBodies(t, rec) {
		asMap := object.(map[string]any)
		if _, ok := asMap[fragment]; !ok {
			t.Fatalf("the bash import did not stamp %s on %q", fragment, name)
		}
	}
}

// normalise replaces the mapping path, which is the only thing that
// legitimately differs between the two runs.
func normalise(text, mappingPath string) string {
	return strings.ReplaceAll(text, mappingPath, "<mapping>")
}

// --- reading each side's record ---------------------------------------------

func objectBodiesFromFakeForImport(t *testing.T, ids []string) map[string][]byte {
	t.Helper()

	repo := testfixture.RepoRoot()
	work := t.TempDir()
	rec := filepath.Join(work, "rec")
	if err := os.MkdirAll(rec, 0o755); err != nil {
		t.Fatal(err)
	}

	bodies := map[string][]byte{}
	for _, id := range ids {
		target := filepath.Join(work, "object-"+id+".json")
		cmd := exec.Command(filepath.Join(repo, "tests", "fake", "c8y"),
			"inventory", "get", "--id", id, "--outputFileRaw", target, "--nullInput")
		cmd.Env = append(os.Environ(),
			"FAKE_C8Y_REC="+rec,
			"FAKE_C8Y_HIERARCHY="+testfixture.FixturePath("hierarchy.json"),
		)
		cmd.Stdin = strings.NewReader("")
		if err := cmd.Run(); err != nil {
			t.Fatalf("fake c8y could not produce object %s: %v", id, err)
		}
		body, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		bodies[id] = body
	}
	return bodies
}

// bashCreatedBodies reads the request bodies the fake recorded, keyed by name so
// the two sides can be matched without depending on ids.
func bashCreatedBodies(t *testing.T, rec string) map[string]any {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(rec, "payloads", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	bodies := map[string]any{}
	for _, match := range matches {
		raw, err := os.ReadFile(match)
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]any
		if err := json.Unmarshal(raw, &object); err != nil {
			t.Fatalf("%s is not valid JSON: %v", match, err)
		}
		name, _ := object["name"].(string)
		bodies[name] = object
	}
	return bodies
}

// bashLinks reads links.log and names both ends by source id.
func bashLinks(t *testing.T, rec, mappingPath string) []string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(rec, "links.log"))
	if err != nil {
		t.Fatal(err)
	}
	reverse := reverseMapping(t, mappingPath)

	var links []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			t.Fatalf("unexpected links.log line: %q", line)
		}
		links = append(links, reverse[fields[0]]+" "+fields[1]+" "+reverse[fields[2]])
	}
	sort.Strings(links)
	return links
}

func goLinks(t *testing.T, mappingPath string, server *testfixture.Server) []string {
	t.Helper()
	reverse := reverseMapping(t, mappingPath)

	var links []string
	for _, link := range server.Links() {
		links = append(links, reverse[link.ParentID]+" "+link.ChildType+" "+reverse[link.ChildID])
	}
	sort.Strings(links)
	return links
}

// bashIdentities reads identities.log and names the device by source id.
func bashIdentities(t *testing.T, rec, mappingPath string) []string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(rec, "identities.log"))
	if err != nil {
		t.Fatal(err)
	}
	reverse := reverseMapping(t, mappingPath)

	var identities []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		// "<deviceId> <type> <externalId>", and an external id may hold spaces
		fields := strings.SplitN(line, " ", 3)
		if len(fields) != 3 {
			t.Fatalf("unexpected identities.log line: %q", line)
		}
		identities = append(identities, reverse[fields[0]]+" "+fields[1]+" "+fields[2])
	}
	sort.Strings(identities)
	return identities
}

func goIdentities(t *testing.T, mappingPath string, server *testfixture.Server) []string {
	t.Helper()
	reverse := reverseMapping(t, mappingPath)

	var identities []string
	for _, identity := range server.CreatedIdentities() {
		identities = append(identities,
			reverse[identity.DeviceID]+" "+identity.Type+" "+identity.ExternalID)
	}
	sort.Strings(identities)
	return identities
}

func reverseMapping(t *testing.T, mappingPath string) map[string]string {
	t.Helper()
	reverse := map[string]string{}
	for oldID, newID := range readMapping(t, mappingPath) {
		reverse[newID] = oldID
	}
	return reverse
}
