// Package session reads go-c8y-cli session files.
//
// This exists because the go-c8y library has no session, config or encryption
// support at all, and because asking the c8y binary is not an option: `c8y
// sessions get --session X` does not report session X, it reports whichever
// session is active and ignores --session. That was discovered the hard way, and
// it is recorded as assumption 7 in tests/fake/c8y.
//
// Reading the file directly is also strictly better: host and tenant are
// plaintext even when the credentials are encrypted, so identifying a session
// needs no passphrase, no network and no active session.
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Session is what a go-c8y-cli session file holds that we care about. Password
// and Token arrive either in the clear or wrapped in the {encrypted} marker.
type Session struct {
	// Path is the file this was read from, which is what gets displayed: a glob
	// that resolved to something unexpected is the failure worth surfacing.
	Path string

	Host     string
	Tenant   string
	Username string
	Password string
	Token    string

	// Certificate and CertificateKey authenticate with a client certificate
	// instead of a password: PEM contents, not paths. Used only for a remote
	// migration target the service reaches without a service user (see
	// docs/platform-behaviour.md). Like the fields above, Read decodes into
	// its own private struct, so no session file can set these either.
	Certificate    string
	CertificateKey string

	// InsecureSkipVerify accepts a certificate that does not verify. Set in Go
	// by the test fixture, which serves TLS with a self-signed certificate.
	// Read decodes a session file into its own private struct, so no session
	// file can set this and a real migration always verifies.
	InsecureSkipVerify bool
}

// Name is the file name, which is the part that says which tenant it is. A
// session taken from the environment may have no file behind it at all.
func (s *Session) Name() string {
	if s.Path == "" {
		return "(from the environment)"
	}
	return filepath.Base(s.Path)
}

// NormalisedHost strips the scheme, a single trailing slash and the case.
//
// Session files disagree about whether the host includes https://, and two
// spellings of one host must not look like two hosts - otherwise the
// same-tenant refusal never fires, which is the one thing it exists for.
func NormalisedHost(host string) string {
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimSuffix(host, "/")
	return strings.ToLower(host)
}

// BaseURL is the host with a scheme, which is what an HTTP client needs.
func (s *Session) BaseURL() string {
	if strings.HasPrefix(s.Host, "http://") || strings.HasPrefix(s.Host, "https://") {
		return strings.TrimSuffix(s.Host, "/")
	}
	return "https://" + NormalisedHost(s.Host)
}

// Home is the directory --session values are matched against.
//
// Deliberately not read from `c8y settings list`: this tool must work without
// the c8y binary installed. Someone who has overridden session.home in c8y's
// settings has to pass --sessionHome.
func Home(override string) string {
	if override != "" {
		return override
	}
	if fromEnv := os.Getenv("C8Y_SESSION_HOME"); fromEnv != "" {
		return fromEnv
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".cumulocity")
	}
	return ".cumulocity"
}

// AmbiguousError is a session value that matched more than one file. It is an
// error rather than a first-match because c8y resolves it silently, and the
// wrong tenant here creates objects where they do not belong.
type AmbiguousError struct {
	Value   string
	Side    string
	Matches []string
}

func (e *AmbiguousError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "The %s session '%s' matches %d files:\n", e.Side, e.Value, len(e.Matches))
	for _, m := range e.Matches {
		fmt.Fprintf(&b, "  %s\n", filepath.Base(m))
	}
	b.WriteString("c8y would pick one of these without saying which, and the wrong\n")
	b.WriteString("tenant here creates objects where they do not belong. Name one exactly.")
	return b.String()
}

// NotFoundError is a session value that matched nothing.
type NotFoundError struct {
	Value string
	Side  string
	Home  string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("Could not resolve the %s session: %s\n", e.Side, e.Value) +
		fmt.Sprintf("Pass a path, or a file name or glob matching a file in %s.\n", e.Home) +
		"A bare session name without its .json extension does not resolve."
}

// Resolve turns a --session value into the one file it names.
//
// Mirrors commands/migrate:220. An existing path wins; otherwise the value is
// matched against file names in the session home as a GLOB, not a substring:
// c8y resolves 'foo.eu-latest.cumulocity.com-me.json' and '*eu-latest*', but a
// bare 'foo' matches nothing at all.
//
// side is "source" or "target", or "" for a single-session command, and only
// shapes the error text.
func Resolve(value, home, side string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("no session given")
	}
	if info, err := os.Stat(value); err == nil && !info.IsDir() {
		return value, nil
	}

	sessionHome := Home(home)
	matches, err := filepath.Glob(filepath.Join(sessionHome, value))
	if err != nil {
		// only ErrBadPattern, which is a caller mistake worth reporting plainly
		return "", fmt.Errorf("invalid session pattern %q: %w", value, err)
	}

	files := make([]string, 0, len(matches))
	for _, m := range matches {
		if info, statErr := os.Stat(m); statErr == nil && !info.IsDir() {
			files = append(files, m)
		}
	}
	sort.Strings(files)

	switch len(files) {
	case 0:
		return "", &NotFoundError{Value: value, Side: sideOrDefault(side), Home: sessionHome}
	case 1:
		return files[0], nil
	default:
		return "", &AmbiguousError{Value: value, Side: sideOrDefault(side), Matches: files}
	}
}

func sideOrDefault(side string) string {
	if side == "" {
		return "given"
	}
	return side
}

// Load resolves a --session value and reads the file behind it.
func Load(value, home, side string) (*Session, error) {
	path, err := Resolve(value, home, side)
	if err != nil {
		return nil, err
	}
	return Read(path)
}

// Read parses one session file. Unknown fields are ignored: a session file
// carries settings this tool has no interest in.
func Read(path string) (*Session, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read session file %s: %w", path, err)
	}

	var doc struct {
		Host     string `json:"host"`
		Tenant   string `json:"tenant"`
		Username string `json:"username"`
		Password string `json:"password"`
		Token    string `json:"token"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("session file %s is not valid JSON: %w", path, err)
	}

	return &Session{
		Path:     path,
		Host:     doc.Host,
		Tenant:   doc.Tenant,
		Username: doc.Username,
		Password: doc.Password,
		Token:    doc.Token,
	}, nil
}

// SameTenantAs compares host AND tenant, because a tenant id only identifies a
// tenant within one host: t12345 on eu-latest and t12345 on another host are
// different tenants, and refusing that migration would be wrong.
func (s *Session) SameTenantAs(other *Session) bool {
	return NormalisedHost(s.Host) == NormalisedHost(other.Host) && s.Tenant == other.Tenant
}
