// Package c8yclient builds a go-c8y v2 client from a go-c8y-cli session file.
//
// Two tenants means two clients, each with its own base URL and credentials.
// That is the whole reason this port exists: the bash implementation had one
// ambient process environment to express both sides with, which is how a
// migration ended up reading and writing the same tenant.
package c8yclient

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/authentication"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/tenants/currenttenant"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/session"
)

// Timeout is generous: a migration reads pages of measurements, and a slow
// tenant is not a failed one.
const Timeout = 10 * time.Minute

// New builds a client for one session.
//
// Credentials are decrypted here rather than by the caller, so a passphrase
// never has to be threaded through the rest of the program.
//
// Username and password are preferred over a stored token, deliberately. A
// token in a session file has no expiry check on this path, so a stale one would
// make every request fail with 401 and never recover; credentials are exchanged
// for a fresh token by the library instead.
func New(s *session.Session, passphrase string) (*api.Client, error) {
	username, password, token, err := s.Credentials(passphrase)
	if err != nil {
		if errors.Is(err, session.ErrNoPassphrase) {
			return nil, fmt.Errorf("%s holds encrypted credentials, so C8Y_PASSPHRASE has to be set", s.Name())
		}
		return nil, fmt.Errorf("%s: %w", s.Name(), err)
	}

	auth := authentication.AuthOptions{
		// The tenant from the session file, so the client does not have to
		// spend a call on /tenant/currentTenant working it out.
		Tenant:   s.Tenant,
		Username: username,
		Password: password,
	}
	if password == "" && token != "" {
		auth.Token = token
	}
	if auth.Username == "" && auth.Token == "" {
		return nil, fmt.Errorf("%s holds no usable credentials: no password and no token", s.Name())
	}

	return api.NewClient(api.ClientOptions{
		BaseURL: s.BaseURL(),
		Auth:    auth,
		Timeout: Timeout,
		// A test fixture serves TLS with a self-signed certificate, which is
		// the only reason this is here. Off by default, so a real migration
		// still verifies the platform's certificate.
		InsecureSkipVerify: s.InsecureSkipVerify,
	}), nil
}

// Probe proves the credentials actually work, rather than that a file exists.
// Returns the tenant name the platform reports, which is the authoritative
// answer where the session file's tenant field is only a claim.
func Probe(ctx context.Context, client *api.Client) (string, error) {
	result := client.Tenants.Current.Get(ctx, currenttenant.GetOptions{})
	if result.Err != nil {
		return "", result.Err
	}
	return result.Data.Name(), nil
}
