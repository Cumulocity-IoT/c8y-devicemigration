package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api"
	"github.com/tidwall/gjson"
)

// RequiredRole is the permission a caller must hold to start or undo a
// migration. It is declared in the manifest under "roles", which is what makes
// it grantable in the Administration UI - the platform does not enforce it for
// us, so every write endpoint checks it here.
const RequiredRole = "ROLE_DEVICE_MIGRATION_ADMIN"

// ErrUnauthenticated means the request carried no usable credentials, or the
// platform rejected them.
var ErrUnauthenticated = errors.New("unauthenticated")

// ForbiddenError means the caller is who they say they are but may not do this.
// It carries its own reason because "forbidden" alone sends people to the logs.
type ForbiddenError struct {
	Reason string
}

func (e *ForbiddenError) Error() string { return e.Reason }

// Caller is a verified caller.
type Caller struct {
	Tenant   string
	Username string
}

// Authorize establishes who is calling and whether they may migrate.
//
// The API gateway has already authenticated the request, but it forwards the
// credentials rather than a verdict, and it says nothing about roles. So the
// credentials are proven against the platform and then checked against two
// rules: the caller must belong to the tenant this microservice is deployed on,
// and must hold RequiredRole. A user of a subscribed subtenant is deliberately
// not enough - that would let one subtenant's admin copy another's devices.
func (s *Service) Authorize(ctx context.Context, r *http.Request) (*Caller, error) {
	creds, ok := credentialsFrom(r)
	if !ok {
		return nil, fmt.Errorf(
			"%w: no credentials on the request, which needs either an Authorization "+
				"header or an %s cookie", ErrUnauthenticated, cookieAuthorization)
	}
	if creds.tenant == "" {
		return nil, fmt.Errorf(
			"%w: the credentials name no tenant, so authenticate as tenant/username",
			ErrUnauthenticated)
	}

	if err := checkXSRF(r, creds); err != nil {
		return nil, err
	}

	// Verified through the owner tenant's client with the caller's credentials
	// overridden per request, so this costs no new connection pool. A cookie's
	// JWT goes out as a bearer token, which is why the cookie itself does not
	// have to be forwarded.
	owner, err := s.tenant(s.ownerTenant)
	if err != nil {
		return nil, err
	}

	verify := api.WithAuth(ctx, creds.auth)
	if creds.tfaToken != "" {
		// The platform requires a two-factor token on every call made with such
		// a session, this one included.
		verify = withTFAToken(verify, creds.tfaToken)
	}

	result := owner.client.Users.CurrentUser.Get(verify)
	if result.Err != nil {
		if result.HTTPStatus == http.StatusUnauthorized || result.HTTPStatus == http.StatusForbidden {
			return nil, fmt.Errorf("%w: the platform rejected the credentials", ErrUnauthenticated)
		}
		return nil, fmt.Errorf("could not verify the caller: %w", result.Err)
	}

	if creds.tenant != s.ownerTenant {
		return nil, &ForbiddenError{Reason: fmt.Sprintf(
			"only users of %s may migrate, and these credentials belong to %s",
			s.ownerTenant, creds.tenant)}
	}

	if !hasRole(result.Data.Bytes(), RequiredRole) {
		return nil, &ForbiddenError{Reason: fmt.Sprintf(
			"%s does not hold %s, which is required to migrate. Grant it in "+
				"Administration under Roles", creds.username, RequiredRole)}
	}

	return &Caller{Tenant: creds.tenant, Username: creds.username}, nil
}

// checkXSRF applies the platform's cross-site rule to a browser session.
//
// A cookie travels with any request the browser is talked into making, so
// without this a page somebody visits while logged in could start a migration
// with their session. The platform's own UI sends this header on every write for
// the same reason. Reads are left alone: they change nothing, and refusing them
// would make the API documentation application useless for browsing.
func checkXSRF(r *http.Request, creds credentials) error {
	if !creds.fromCookie || isRead(r.Method) {
		return nil
	}

	presented := r.Header.Get(headerXSRF)
	if presented == "" {
		return fmt.Errorf("%w: this is a browser session, so anything other than a read "+
			"also needs the %s header", ErrUnauthenticated, headerXSRF)
	}
	// Compared when the session says what its token is. A session whose token
	// carries no such claim still has to present the header, which is as much as
	// can be checked without it.
	if creds.xsrfClaim != "" && presented != creds.xsrfClaim {
		return fmt.Errorf("%w: the %s header does not match this session",
			ErrUnauthenticated, headerXSRF)
	}
	return nil
}

// hasRole looks for a role in a currentUser document.
//
// Read from three places on purpose: currentUser returns effectiveRoles as a
// flat array, while the user and role collections wrap the same thing in
// references. Checking all three costs nothing and means a platform that
// answers in either shape is understood.
func hasRole(user []byte, role string) bool {
	for _, path := range []string{
		"effectiveRoles.#.id",
		"effectiveRoles.references.#.role.id",
		"roles.references.#.role.id",
	} {
		for _, found := range gjson.GetBytes(user, path).Array() {
			if found.String() == role {
				return true
			}
		}
	}
	return false
}
