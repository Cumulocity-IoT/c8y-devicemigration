package session

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// clearAmbient removes every C8Y_* variable this package looks at, so a test
// starts from a known state whatever the developer's shell holds.
func clearAmbient(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"C8Y_BASEURL", "C8Y_URL", "C8Y_HOST",
		"C8Y_TENANT", "C8Y_USERNAME", "C8Y_USER",
		"C8Y_PASSWORD", "C8Y_TOKEN", "C8Y_SESSION",
	} {
		t.Setenv(name, "")
		_ = os.Unsetenv(name)
	}
}

func TestCurrentPrefersTheExplicitFlag(t *testing.T) {
	// An explicit --session wins outright, and the ambient variables are
	// ignored. This is the property that stops a stray C8Y_HOST from
	// redirecting a command that was told exactly which tenant to use.
	clearAmbient(t)
	t.Setenv("C8Y_HOST", "https://ambient.example.com")
	t.Setenv("C8Y_PASSWORD", "ambient")

	home := t.TempDir()
	writeSession(t, home, "chosen.json", "https://chosen.example.com", "t-chosen")

	s, origin, err := Current("chosen.json", home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if origin != OriginFlag {
		t.Errorf("origin = %v, want OriginFlag", origin)
	}
	if NormalisedHost(s.Host) != "chosen.example.com" {
		t.Errorf("the ambient host won: %q", s.Host)
	}
}

func TestCurrentUsesTheActiveSession(t *testing.T) {
	// What `eval "$(c8y sessions set ...)"` leaves behind. The credentials are
	// already decrypted there, which is why this route needs no passphrase.
	clearAmbient(t)
	t.Setenv("C8Y_HOST", "https://active.example.com")
	t.Setenv("C8Y_TENANT", "t-active")
	t.Setenv("C8Y_USER", "someone")
	t.Setenv("C8Y_PASSWORD", "plaintext-because-it-was-exported")

	s, origin, err := Current("", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if origin != OriginEnvironment {
		t.Errorf("origin = %v, want OriginEnvironment", origin)
	}
	if NormalisedHost(s.Host) != "active.example.com" || s.Tenant != "t-active" {
		t.Errorf("session = %+v", s)
	}

	// no passphrase, and nothing to decrypt
	_, password, _, err := s.Credentials("")
	if err != nil {
		t.Fatalf("credentials from the environment should need no passphrase: %v", err)
	}
	if password != "plaintext-because-it-was-exported" {
		t.Errorf("password = %q", password)
	}
}

func TestCurrentAcceptsAnyOfTheHostVariables(t *testing.T) {
	// go-c8y-cli and the library both accept three spellings.
	for _, name := range []string{"C8Y_BASEURL", "C8Y_URL", "C8Y_HOST"} {
		t.Run(name, func(t *testing.T) {
			clearAmbient(t)
			t.Setenv(name, "https://via-"+strings.ToLower(name)+".example.com")
			t.Setenv("C8Y_TOKEN", "a-token")

			s, origin, err := Current("", "")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if origin != OriginEnvironment {
				t.Fatalf("origin = %v", origin)
			}
			if !strings.Contains(s.Host, "via-") {
				t.Fatalf("host = %q", s.Host)
			}
		})
	}
}

func TestCurrentFallsBackToTheSessionFileVariable(t *testing.T) {
	// A session selected without its credentials being exported: C8Y_SESSION
	// names the file, and there is nothing else to go on.
	clearAmbient(t)
	home := t.TempDir()
	path := writeSession(t, home, "selected.json", "https://selected.example.com", "t-selected")
	t.Setenv("C8Y_SESSION", path)

	s, origin, err := Current("", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if origin != OriginSessionFile {
		t.Errorf("origin = %v, want OriginSessionFile", origin)
	}
	if s.Tenant != "t-selected" {
		t.Errorf("tenant = %q", s.Tenant)
	}
	// still encrypted, so this route does need a passphrase
	if !IsEncrypted(s.Password) {
		t.Error("the file's credentials should still be encrypted")
	}
}

func TestActiveSessionBeatsTheSessionFileVariable(t *testing.T) {
	// Both are set after `eval "$(c8y sessions set ...)"`. The variables win,
	// because their credentials are decrypted and the file's are not - so
	// preferring the file would demand a passphrase for no reason.
	clearAmbient(t)
	home := t.TempDir()
	path := writeSession(t, home, "file.json", "https://from-file.example.com", "t-file")
	t.Setenv("C8Y_SESSION", path)
	t.Setenv("C8Y_HOST", "https://from-env.example.com")
	t.Setenv("C8Y_PASSWORD", "decrypted")

	s, origin, err := Current("", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if origin != OriginEnvironment {
		t.Fatalf("origin = %v, want OriginEnvironment", origin)
	}
	if NormalisedHost(s.Host) != "from-env.example.com" {
		t.Errorf("host = %q", s.Host)
	}
	// the file is still named, so the operator can tell which session it was
	if s.Name() != "file.json" {
		t.Errorf("Name() = %q, want the file C8Y_SESSION pointed at", s.Name())
	}
}

func TestAHostWithNoCredentialsIsNotASession(t *testing.T) {
	// C8Y_HOST alone cannot authenticate, so it must not shadow C8Y_SESSION.
	clearAmbient(t)
	home := t.TempDir()
	path := writeSession(t, home, "file.json", "https://from-file.example.com", "t-file")
	t.Setenv("C8Y_SESSION", path)
	t.Setenv("C8Y_HOST", "https://from-env.example.com")

	_, origin, err := Current("", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if origin != OriginSessionFile {
		t.Fatalf("origin = %v, want OriginSessionFile", origin)
	}
}

func TestNoSessionAtAllExplainsBothWaysIn(t *testing.T) {
	clearAmbient(t)

	_, _, err := Current("", t.TempDir())
	if err == nil {
		t.Fatal("with nothing to go on this must fail")
	}
	if !errors.Is(err, ErrNoSession) {
		t.Errorf("got %v, want ErrNoSession", err)
	}
	for _, want := range []string{"Pass --session", "c8y sessions set"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message is missing %q:\n%s", want, err.Error())
		}
	}
}

func TestNameOfAnEnvironmentOnlySession(t *testing.T) {
	clearAmbient(t)
	t.Setenv("C8Y_HOST", "https://active.example.com")
	t.Setenv("C8Y_TOKEN", "a-token")

	s, _, err := Current("", "")
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "(from the environment)" {
		t.Errorf("Name() = %q", s.Name())
	}
}
