package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A session file shaped like a real one: host and tenant plaintext, credentials
// encrypted. That split is the whole reason the identity can be read locally.
func writeSession(t *testing.T, dir, name, host, tenant string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	body := `{"host":"` + host + `","tenant":"` + tenant + `","username":"me",` +
		`"password":"{encrypted}xxx","token":"{encrypted}yyy"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveExactFileName(t *testing.T) {
	home := t.TempDir()
	writeSession(t, home, "source.eu-latest.cumulocity.com-me.json", "https://source.example.com", "t1")

	got, err := Resolve("source.eu-latest.cumulocity.com-me.json", home, "source")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Base(got) != "source.eu-latest.cumulocity.com-me.json" {
		t.Fatalf("resolved to %q", got)
	}
}

func TestResolveExplicitPathWins(t *testing.T) {
	// A path that exists is used as given, even when the session home holds
	// something that would also match.
	elsewhere := t.TempDir()
	path := writeSession(t, elsewhere, "somewhere.json", "https://a.example.com", "t1")

	got, err := Resolve(path, t.TempDir(), "source")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != path {
		t.Fatalf("got %q, want the path as given %q", got, path)
	}
}

func TestResolveGlob(t *testing.T) {
	home := t.TempDir()
	writeSession(t, home, "only-one.eu-latest.json", "https://a.example.com", "t1")
	writeSession(t, home, "other.us.json", "https://b.example.com", "t2")

	got, err := Resolve("*eu-latest*", home, "source")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Base(got) != "only-one.eu-latest.json" {
		t.Fatalf("resolved to %q", got)
	}
}

func TestResolveAmbiguousGlobIsRefused(t *testing.T) {
	// c8y silently takes the first match. For a command that creates objects in
	// a second tenant, guessing is the worst available behaviour.
	home := t.TempDir()
	writeSession(t, home, "first.eu-latest.json", "https://a.example.com", "t1")
	writeSession(t, home, "second.eu-latest.json", "https://b.example.com", "t2")

	_, err := Resolve("*eu-latest*", home, "target")
	if err == nil {
		t.Fatal("an ambiguous glob must be refused, not resolved")
	}
	var ambiguous *AmbiguousError
	if !asError(err, &ambiguous) {
		t.Fatalf("got %T, want *AmbiguousError", err)
	}
	msg := err.Error()
	for _, want := range []string{
		"target session '*eu-latest*' matches 2 files",
		"first.eu-latest.json",
		"second.eu-latest.json",
		"Name one exactly",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message is missing %q:\n%s", want, msg)
		}
	}
}

func TestResolveNoMatchExplainsTheExtension(t *testing.T) {
	// A bare name without .json is the usual mistake, and c8y does not resolve
	// it either, so the message has to say so.
	home := t.TempDir()
	writeSession(t, home, "source.eu-latest.json", "https://a.example.com", "t1")

	_, err := Resolve("source", home, "source")
	if err == nil {
		t.Fatal("a bare name must not resolve")
	}
	msg := err.Error()
	for _, want := range []string{
		"Could not resolve the source session: source",
		home,
		"without its .json extension does not resolve",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message is missing %q:\n%s", want, msg)
		}
	}
}

func TestResolveIgnoresDirectories(t *testing.T) {
	// A directory in the session home must not be mistaken for a session.
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "activitylog"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve("activitylog", home, "source"); err == nil {
		t.Fatal("a directory must not resolve as a session")
	}
}

func TestNormalisedHostSeesThroughSpellings(t *testing.T) {
	// Real session files disagree about the scheme. Two spellings of one host
	// must not look like two hosts, or the same-tenant refusal never fires.
	for _, spelling := range []string{
		"https://Example.Cumulocity.com",
		"http://example.cumulocity.com",
		"example.cumulocity.com/",
		"example.cumulocity.com",
	} {
		if got := NormalisedHost(spelling); got != "example.cumulocity.com" {
			t.Errorf("NormalisedHost(%q) = %q", spelling, got)
		}
	}
}

func TestSameTenantNeedsHostAndTenant(t *testing.T) {
	// A tenant id only identifies a tenant within one host: t12345 on two
	// different hosts are different tenants, and refusing that would be wrong.
	a := &Session{Host: "https://one.example.com", Tenant: "t12345"}
	sameSpeltDifferently := &Session{Host: "one.example.com/", Tenant: "t12345"}
	otherHost := &Session{Host: "https://two.example.com", Tenant: "t12345"}
	otherTenant := &Session{Host: "https://one.example.com", Tenant: "t99999"}

	if !a.SameTenantAs(sameSpeltDifferently) {
		t.Error("the same tenant spelt differently must compare equal")
	}
	if a.SameTenantAs(otherHost) {
		t.Error("the same tenant id on a different host is a different tenant")
	}
	if a.SameTenantAs(otherTenant) {
		t.Error("a different tenant id is a different tenant")
	}
}

func TestBaseURLAlwaysHasAScheme(t *testing.T) {
	for _, tc := range []struct{ host, want string }{
		{"https://a.example.com", "https://a.example.com"},
		{"a.example.com", "https://a.example.com"},
		{"a.example.com/", "https://a.example.com"},
		{"http://a.example.com", "http://a.example.com"},
	} {
		if got := (&Session{Host: tc.host}).BaseURL(); got != tc.want {
			t.Errorf("BaseURL(%q) = %q, want %q", tc.host, got, tc.want)
		}
	}
}

func TestReadKeepsPlaintextAndCiphertextApart(t *testing.T) {
	home := t.TempDir()
	path := writeSession(t, home, "s.json", "https://a.example.com", "t42")

	s, err := Read(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Host != "https://a.example.com" || s.Tenant != "t42" {
		t.Fatalf("host/tenant not read: %+v", s)
	}
	// the identity is readable without a passphrase, which is the whole point
	if !IsEncrypted(s.Password) || !IsEncrypted(s.Token) {
		t.Fatal("credentials should still be encrypted after Read")
	}
	if s.Name() != "s.json" {
		t.Fatalf("Name() = %q", s.Name())
	}
}

func TestHomeFallsBackToTheEnvironment(t *testing.T) {
	t.Setenv("C8Y_SESSION_HOME", "/somewhere/else")
	if got := Home(""); got != "/somewhere/else" {
		t.Fatalf("Home() = %q", got)
	}
	// an explicit override still wins, so --sessionHome works
	if got := Home("/explicit"); got != "/explicit" {
		t.Fatalf("Home(override) = %q", got)
	}
}

// errors.As without importing errors into the test's readable surface
func asError[T error](err error, target *T) bool {
	for err != nil {
		if v, ok := err.(T); ok {
			*target = v
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
