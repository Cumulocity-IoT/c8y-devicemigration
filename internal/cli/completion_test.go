package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// complete drives cobra's own completion entry point, which is what a shell
// calls on every tab press.
func complete(t *testing.T, args ...string) []string {
	t.Helper()

	var stdout, stderr bytes.Buffer
	root := newRootCommand(&stdout, &stderr)
	for _, command := range root.Commands() {
		registerCompletions(command)
	}
	root.SetArgs(append([]string{cobra.ShellCompRequestCmd}, args...))
	if err := root.Execute(); err != nil {
		t.Fatalf("completion failed: %v", err)
	}

	var completions []string
	for _, line := range strings.Split(stdout.String(), "\n") {
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		// cobra separates a completion from its description with a tab
		completions = append(completions, strings.SplitN(line, "\t", 2)[0])
	}
	return completions
}

func TestSessionNamesAreCompleted(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{
		"alpha.eu-latest.cumulocity.com-me.json",
		"beta.us.cumulocity.com-me.json",
		"notasession.txt",
	} {
		if err := os.WriteFile(filepath.Join(home, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(home, "activitylog"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := complete(t, "export", "--sessionHome", home, "--session", "")
	want := []string{
		"alpha.eu-latest.cumulocity.com-me.json",
		"beta.us.cumulocity.com-me.json",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("completed %v, want %v", got, want)
	}
}

func TestSessionCompletionFiltersByWhatIsTyped(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{"alpha.json", "beta.json"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got := complete(t, "export", "--sessionHome", home, "--session", "al")
	if len(got) != 1 || got[0] != "alpha.json" {
		t.Errorf("completed %v, want just alpha.json", got)
	}
}

func TestBothMigrateSessionFlagsAreCompleted(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "only.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, flag := range []string{"--sourceSession", "--targetSession"} {
		got := complete(t, "migrate", "--sessionHome", home, flag, "")
		if len(got) != 1 || got[0] != "only.json" {
			t.Errorf("%s completed %v", flag, got)
		}
	}
}

func TestDataTypesAreCompleted(t *testing.T) {
	got := complete(t, "export", "--dataTypes", "")
	want := []string{"measurements", "events", "alarms", "all"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("completed %v, want %v", got, want)
	}
}

func TestDataTypeCompletionKeepsWhatIsTypedAndDropsWhatIsChosen(t *testing.T) {
	got := complete(t, "export", "--dataTypes", "measurements,")
	want := []string{"measurements,events", "measurements,alarms"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("completed %v, want %v", got, want)
	}
}

func TestAllIsOnlyOfferedOnItsOwn(t *testing.T) {
	// It cannot be combined with anything, so suggesting it after a comma would
	// suggest something the command refuses.
	for _, completion := range complete(t, "export", "--dataTypes", "measurements,") {
		if strings.HasSuffix(completion, "all") {
			t.Errorf("'all' was offered after a comma: %q", completion)
		}
	}
}

func TestChildTypesAreCompleted(t *testing.T) {
	got := complete(t, "export", "--childTypes", "asset,d")
	if len(got) != 1 || got[0] != "asset,device" {
		t.Errorf("completed %v, want asset,device", got)
	}
}

func TestRootIDIsNotCompleted(t *testing.T) {
	// Completion runs on every tab press, and resolving device names means
	// querying the tenant. A shell that pauses for an authenticated round trip
	// is worse than one that completes nothing.
	if got := complete(t, "export", "--rootId", ""); len(got) != 0 {
		t.Errorf("--rootId offered %v, want nothing", got)
	}
}

func TestCompletionScriptsAreGeneratable(t *testing.T) {
	// Cobra provides these, but a broken command tree would break them, and a
	// user following the README should not be the one to find out.
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		var stdout, stderr bytes.Buffer
		root := newRootCommand(&stdout, &stderr)
		root.SetArgs([]string{"completion", shell})
		if err := root.Execute(); err != nil {
			t.Errorf("completion %s failed: %v", shell, err)
			continue
		}
		if stdout.Len() == 0 {
			t.Errorf("completion %s produced nothing", shell)
		}
	}
}
