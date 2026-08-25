package migrate_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/migrate"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/testfixture"
)

// The differential test for migrate.
//
// The bash implementation drives itself through the fake c8y and both sides of
// the migration are the same fake tenant, so what can be compared is the shape
// of the run: the phase lines, the per-device progress, the journal and the
// closing hints. Ids are normalised out, because the two runs allocate their own
// and the run id is a timestamp.

func requireBashAndJq(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed, so the bash implementation cannot be run", tool)
		}
	}
}

// bashMigrate runs commands/migrate against the fake, with both sessions in a
// temp session home.
func bashMigrate(t *testing.T, args ...string) (stderr string, journal []string) {
	t.Helper()

	repo := testfixture.RepoRoot()
	work := t.TempDir()
	rec := filepath.Join(work, "rec")
	home := filepath.Join(work, "sessions")
	for _, dir := range []string{rec, home} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeBashSession(t, home, "source.json", "https://source.example.com", "t_source")
	writeBashSession(t, home, "target.json", "https://target.example.com", "t_target")

	argv := append([]string{filepath.Join(repo, "commands", "migrate"),
		"--rootId", "1",
		"--sourceSession", "source.json",
		"--targetSession", "target.json",
		"--force"}, args...)

	cmd := exec.Command("bash", argv...)
	cmd.Dir = work
	cmd.Env = append(os.Environ(),
		"PATH="+filepath.Join(repo, "tests", "fake")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_C8Y_REC="+rec,
		"FAKE_C8Y_HIERARCHY="+testfixture.FixturePath("hierarchy.json"),
		"FAKE_C8Y_SESSION_HOME="+home,
	)

	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("bash migrate failed: %v\n%s", err, errBuf.String())
	}

	if raw, err := os.ReadFile(filepath.Join(work, "id_mapping.json.journal")); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if line != "" {
				journal = append(journal, line)
			}
		}
	}
	return errBuf.String(), journal
}

func writeBashSession(t *testing.T, dir, name, host, tenant string) {
	t.Helper()
	body := `{"host":"` + host + `","tenant":"` + tenant +
		`","password":"{encrypted}xxx","token":"{encrypted}yyy"}`
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// normaliseRun removes everything the two runs legitimately allocate
// differently: target ids, run ids and paths.
func normaliseRun(text string) string {
	text = regexp.MustCompile(`[0-9]{8}T[0-9]{6}Z_[0-9a-f]{6}`).ReplaceAllString(text, "<runId>")
	text = regexp.MustCompile(`\b[0-9]{4,}\b`).ReplaceAllString(text, "<id>")
	// Any path, and the bare relative default the bash command uses: the two
	// runs write into their own directories, and one of them is given an
	// absolute path by the test harness.
	text = regexp.MustCompile(`[^ \t\n]*id_mapping\.json`).ReplaceAllString(text, "<mapping>")
	text = regexp.MustCompile(`[^ \t\n]*(source|target|fixture)\.json`).ReplaceAllString(text, "<session>")
	text = regexp.MustCompile(`[^ \t\n]*staging\.[A-Za-z0-9]+`).ReplaceAllString(text, "<staging>")
	// the two tools name themselves differently in the revert hint
	text = strings.ReplaceAll(text, "c8y devicemigration revert", "revert")
	text = strings.ReplaceAll(text, "c8y-devicemigration revert", "revert")
	return text
}

// phaseLines keeps the lines that describe the shape of the run, dropping the
// per-object detail the export and import tests already compare.
func phaseLines(text string) string {
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "Exporting "),
			strings.HasPrefix(line, "Importing the hierarchy"),
			strings.HasPrefix(line, "Importing into "),
			strings.HasPrefix(line, "Reconciling "),
			strings.HasPrefix(line, "Migrating data for "),
			strings.HasPrefix(line, "Device "),
			strings.HasPrefix(line, "Run id "),
			strings.HasPrefix(line, "Migration complete"),
			strings.HasPrefix(line, "  revert "),
			strings.HasPrefix(line, "  or, if the mapping is lost:"):
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func TestDifferentialMigrateChained(t *testing.T) {
	requireBashAndJq(t)

	bashErr, _ := bashMigrate(t)

	h := setup(t, migrate.Options{})
	goErr, err := h.run(t, "")
	if err != nil {
		t.Fatalf("go migrate failed: %v\n%s", err, goErr)
	}

	if got, want := phaseLines(normaliseRun(goErr)), phaseLines(normaliseRun(bashErr)); got != want {
		t.Errorf("the shape of the run differs\nbash:\n%s\ngo:\n%s", want, got)
	}
}

func TestDifferentialMigrateIncremental(t *testing.T) {
	requireBashAndJq(t)

	bashErr, bashJournal := bashMigrate(t, "--incremental", "--dataTypes", "measurements,events,alarms")

	h := setup(t, migrate.Options{DataTypes: allData, Incremental: true})
	goErr, err := h.run(t, "")
	if err != nil {
		t.Fatalf("go migrate failed: %v\n%s", err, goErr)
	}

	if got, want := phaseLines(normaliseRun(goErr)), phaseLines(normaliseRun(bashErr)); got != want {
		t.Errorf("the shape of the run differs\nbash:\n%s\ngo:\n%s", want, got)
	}

	// The journal is an artefact, so its shape is compared too.
	goJournal := h.journalLines(t)
	if got, want := normaliseRun(strings.Join(goJournal, "\n")),
		normaliseRun(strings.Join(bashJournal, "\n")); got != want {
		t.Errorf("journals differ\nbash:\n%s\ngo:\n%s", want, got)
	}
}
