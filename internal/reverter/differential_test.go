package reverter_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/c8yclient"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/export"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/importer"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/reverter"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/testfixture"
)

// The differential test for revert.
//
// Revert produces no artefacts at all, so the comparison is the stderr - the
// plan, the per-object outcome and the summary - plus which objects the tenant
// ended up without.
//
// The plan names target ids, which the two runs allocate independently. They
// happen to agree today, because both count from 1001, but relying on that would
// make this test fragile for no reason: ids are normalised out and compared
// separately through the mapping.

func requireBashAndJq(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed, so the bash implementation cannot be run", tool)
		}
	}
}

// bashRevert migrates the fixture through the bash commands and then reverts it.
func bashRevert(t *testing.T, runID string, revertArgs ...string) (stderr string, deletes []string) {
	t.Helper()

	repo := testfixture.RepoRoot()
	work := t.TempDir()
	rec := filepath.Join(work, "rec")
	if err := os.MkdirAll(rec, 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(work, "export")
	mapping := filepath.Join(work, "id_mapping.json")

	env := append(os.Environ(),
		"PATH="+filepath.Join(repo, "tests", "fake")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_C8Y_REC="+rec,
		"FAKE_C8Y_HIERARCHY="+testfixture.FixturePath("hierarchy.json"),
	)

	run := func(name string, argv ...string) string {
		cmd := exec.Command("bash", append([]string{filepath.Join(repo, "commands", name)}, argv...)...)
		cmd.Dir = work
		cmd.Env = env
		var errBuf bytes.Buffer
		cmd.Stderr = &errBuf
		// revert exits non-zero when something survives, which some cases want
		_ = cmd.Run()
		return errBuf.String()
	}

	if output := run("export", "--rootId", "1", "--output-directory", out); !strings.Contains(output, "Exported 9") {
		t.Fatalf("bash export did not run: %s", output)
	}
	importArgs := []string{"--input-directory", out, "--mapping", mapping, "--force"}
	if runID != "" {
		importArgs = append(importArgs, "--runId", runID)
	}
	if output := run("import", importArgs...); !strings.Contains(output, "Imported 9 devices") {
		t.Fatalf("bash import did not run: %s", output)
	}

	argv := append([]string{"--force"}, revertArgs...)
	if runID == "" {
		argv = append(argv, "--mapping", mapping)
	}
	stderr = run("revert", argv...)

	// what the fake was asked to delete
	if raw, err := os.ReadFile(filepath.Join(rec, "deletes.log")); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if line != "" {
				deletes = append(deletes, line)
			}
		}
	}
	sort.Strings(deletes)
	return stderr, deletes
}

// goRevert does the same through the Go implementation.
func goRevert(t *testing.T, runID string, opts reverter.Options) (stderr string, remaining int) {
	t.Helper()

	server := testfixture.New(t, "hierarchy.json")
	for id, body := range objectBodiesFromFake(t, server.IDs()) {
		server.SetObjectBody(id, body)
	}

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

	mapping := filepath.Join(work, "id_mapping.json")
	if err := importer.New(client, &discard, importer.Options{
		InputDirectory: out, MappingFile: mapping,
		RunID: runID, AllowSameTenant: true,
	}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	if opts.MappingFile == "" && opts.RunID == "" {
		opts.MappingFile = mapping
	}
	opts.Force = true

	var errBuf bytes.Buffer
	_ = reverter.New(client, &errBuf, strings.NewReader(""), opts).Run(context.Background())
	return errBuf.String(), len(server.Created())
}

// normaliseIDs replaces target ids, which the two runs allocate independently.
func normaliseIDs(text string) string {
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		if text[i] >= '0' && text[i] <= '9' {
			start := i
			for i < len(text) && text[i] >= '0' && text[i] <= '9' {
				i++
			}
			// only the four-digit-and-up ids the tenant allocates; the fixture's
			// own source ids are one or two digits and are worth comparing
			if i-start >= 4 {
				b.WriteString("<id>")
			} else {
				b.WriteString(text[start:i])
			}
			i--
			continue
		}
		b.WriteByte(text[i])
	}
	return b.String()
}

func TestDifferentialRevertFromMapping(t *testing.T) {
	requireBashAndJq(t)

	bashOut, bashDeletes := bashRevert(t, "")
	goOut, remaining := goRevert(t, "", reverter.Options{})

	if got, want := normaliseIDs(goOut), normaliseIDs(bashOut); got != want {
		t.Errorf("stderr differs\nbash:\n%s\ngo:\n%s", want, got)
	}
	if len(bashDeletes) != 9 {
		t.Errorf("bash deleted %d objects, want 9", len(bashDeletes))
	}
	if remaining != 0 {
		t.Errorf("%d objects survived the Go revert", remaining)
	}
}

func TestDifferentialRevertByRunID(t *testing.T) {
	requireBashAndJq(t)

	const runID = "20260825T101530Z_a1b2c3"
	bashOut, _ := bashRevert(t, runID, "--runId", runID)
	goOut, remaining := goRevert(t, runID, reverter.Options{RunID: runID})

	if got, want := normaliseIDs(goOut), normaliseIDs(bashOut); got != want {
		t.Errorf("stderr differs\nbash:\n%s\ngo:\n%s", want, got)
	}
	if remaining != 0 {
		t.Errorf("%d objects survived the Go revert", remaining)
	}
}

// objectBodiesFromFake replays the fake's own responses, so both sides start
// from identical export artefacts.
func objectBodiesFromFake(t *testing.T, ids []string) map[string][]byte {
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
