package session

import (
	"errors"
	"fmt"
	"os"
)

// Origin says where a session came from, so a command can explain itself and a
// test can assert which route was taken.
type Origin int

const (
	// OriginFlag is an explicit --session value.
	OriginFlag Origin = iota

	// OriginEnvironment is the ambient C8Y_* variables, which is how an
	// activated go-c8y-cli session presents itself:
	//
	//	eval "$(c8y sessions set --sessionFilter my-tenant)"
	//
	// The credentials there are already decrypted, so this route needs no
	// passphrase.
	OriginEnvironment

	// OriginSessionFile is the file C8Y_SESSION points at, used when a session
	// has been selected but its credentials were not exported.
	OriginSessionFile
)

func (o Origin) String() string {
	switch o {
	case OriginFlag:
		return "--session"
	case OriginEnvironment:
		return "the active session"
	case OriginSessionFile:
		return "C8Y_SESSION"
	default:
		return "unknown"
	}
}

// environmentNames mirrors the library's own lists, and the set that
// commands/export clears when --session is given. Kept here rather than
// imported so this package stays free of the API client.
var (
	envHost     = []string{"C8Y_BASEURL", "C8Y_URL", "C8Y_HOST"}
	envTenant   = []string{"C8Y_TENANT"}
	envUsername = []string{"C8Y_USERNAME", "C8Y_USER"}
	envPassword = []string{"C8Y_PASSWORD"}
	envToken    = []string{"C8Y_TOKEN"}
)

func firstEnv(names []string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}

// FromEnvironment builds a session from the ambient C8Y_* variables, or returns
// nil when there is no host to talk to.
//
// A session activated with `eval "$(c8y sessions set ...)"` exports its
// credentials already decrypted, which is why this route works without
// C8Y_PASSPHRASE.
func FromEnvironment() *Session {
	host := firstEnv(envHost)
	if host == "" {
		return nil
	}

	return &Session{
		// C8Y_SESSION names the file the active session came from, when it is
		// set. Used for display only: the credentials come from the variables.
		Path:     os.Getenv("C8Y_SESSION"),
		Host:     host,
		Tenant:   firstEnv(envTenant),
		Username: firstEnv(envUsername),
		Password: firstEnv(envPassword),
		Token:    firstEnv(envToken),
	}
}

// hasCredentials reports whether there is anything to authenticate with.
func (s *Session) hasCredentials() bool {
	return s.Password != "" || s.Token != "" || s.Username != ""
}

// ErrNoSession means no session was given and none could be found.
var ErrNoSession = errors.New("no session")

// Current resolves which session a command should use.
//
// In order of precedence:
//
//  1. an explicit --session value, which wins outright
//  2. the ambient C8Y_* variables, i.e. an active session. Preferred over the
//     file below because a session activated through `eval` exports decrypted
//     credentials, so this route needs no passphrase
//  3. the file C8Y_SESSION points at, for a session that was selected without
//     its credentials being exported
//
// This mirrors what the c8y binary does for the implicit case: the environment
// wins. When --session is given the file wins instead, and the ambient
// variables are ignored entirely, which is the behaviour commands/export gets by
// clearing them.
func Current(value, home string) (*Session, Origin, error) {
	if value != "" {
		s, err := Load(value, home, "")
		if err != nil {
			return nil, OriginFlag, err
		}
		return s, OriginFlag, nil
	}

	if fromEnv := FromEnvironment(); fromEnv != nil && fromEnv.hasCredentials() {
		return fromEnv, OriginEnvironment, nil
	}

	if path := os.Getenv("C8Y_SESSION"); path != "" {
		s, err := Read(path)
		if err != nil {
			return nil, OriginSessionFile, err
		}
		return s, OriginSessionFile, nil
	}

	return nil, OriginFlag, fmt.Errorf(
		"%w. Pass --session, or activate one first:\n"+
			"  eval \"$(c8y sessions set --sessionFilter my-tenant)\"", ErrNoSession)
}
