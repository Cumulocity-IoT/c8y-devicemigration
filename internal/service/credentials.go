package service

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/authentication"
)

// Credential material can arrive in three forms, and a microservice that
// understands only the first two turns away everybody using a browser.
//
// The SDK's microservice.AuthFromRequest reads the Authorization header and
// nothing else, so a session from the Cumulocity UI - or from the API
// documentation application's "Try it out" - would be refused before any rule of
// ours was reached: there is no Authorization header on such a request at all. The
// credential is a JWT in the "authorization" cookie, and the platform pairs it
// with an X-XSRF-TOKEN header.

// cookieAuthorization is the cookie the platform puts a browser session's
// credential in. Named in the library but unused there.
const cookieAuthorization = "authorization"

// headerXSRF accompanies a cookie session. The platform requires it on anything
// that changes state, and so do we - see Service.Authorize.
const headerXSRF = "X-XSRF-TOKEN"

// headerTFA carries a two-factor token. The platform's own words: it "always
// must be included in the request to /user/currentUser", so it is forwarded when
// a caller presents it.
const headerTFA = "tfatoken"

// credentials is what a caller presented, and what to present back to the
// platform when verifying it.
type credentials struct {
	// auth is re-presented through api.WithAuth. A cookie's JWT goes out as a
	// bearer token, which the platform accepts: XSRF protection applies to
	// credentials carried in a cookie, not to the token itself.
	auth authentication.AuthOptions

	tenant   string
	username string

	// fromCookie distinguishes a browser session from a programmatic caller,
	// which is what the XSRF rule turns on.
	fromCookie bool

	// xsrfClaim is the session's own XSRF token, from the JWT. Empty when the
	// credential is not a JWT or carries no such claim.
	xsrfClaim string

	// tfaToken is the tfatoken header, if there was one.
	tfaToken string
}

// credentialsFrom reads whatever credentials a request carries.
//
// In the order the platform documents them: an Authorization header, then the
// authorization cookie. Returns false when there is nothing to work with, which
// the caller reports as unauthenticated.
func credentialsFrom(r *http.Request) (credentials, bool) {
	found := credentials{tfaToken: r.Header.Get(headerTFA)}

	if username, password, ok := r.BasicAuth(); ok {
		tenant, user := authentication.SplitTenantUser(username)
		found.auth = authentication.AuthOptions{
			Tenant: tenant, Username: user, Password: password,
		}
		found.tenant, found.username = tenant, user
		return found, true
	}

	if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && token != "" {
		return fromToken(found, token), true
	}

	cookie, err := r.Cookie(cookieAuthorization)
	if err != nil || cookie.Value == "" {
		return credentials{}, false
	}
	found.fromCookie = true

	// The cookie holds a JWT on an OAI-Secure tenant and "Basic <base64>" on a
	// tenant whose UI authenticates with basic credentials. Both are real, so
	// both are read.
	if encoded, ok := strings.CutPrefix(cookie.Value, "Basic "); ok {
		tenant, user, password, ok := decodeBasic(encoded)
		if !ok {
			return credentials{}, false
		}
		found.auth = authentication.AuthOptions{
			Tenant: tenant, Username: user, Password: password,
		}
		found.tenant, found.username = tenant, user
		return found, true
	}

	token := strings.TrimPrefix(cookie.Value, "Bearer ")
	if token == "" {
		return credentials{}, false
	}
	return fromToken(found, token), true
}

// fromToken fills in what a JWT says about its bearer.
func fromToken(found credentials, token string) credentials {
	found.auth = authentication.AuthOptions{Token: token}

	// A token that cannot be read is still passed to the platform: it is the
	// platform's job to reject it, and doing so here would turn an expired
	// session into a confusing error about claims. What is lost is the tenant,
	// which the caller then reports as missing.
	if claim, err := parseClaims(token); err == nil {
		found.auth.Tenant = claim.Tenant
		found.tenant = claim.Tenant
		found.username = claim.User
		found.xsrfClaim = claim.XSRFToken
	}
	return found
}

// parseClaims decodes a JWT payload.
//
// Deliberately not authentication.ParseToken, which decodes with
// base64.RawStdEncoding: JWT mandates the URL-safe alphabet, and the two differ
// exactly when a payload needs '-' or '_'. That makes the library's version fail
// on a token whose claims contain characters as ordinary as '?' or '>', so the
// twelve lines here are worth it. The claim type is still the library's.
//
// No signature or expiry check, because none is needed: nothing is trusted from
// these claims. The tenant and username are used to decide what to ask the
// platform, and the platform's answer is what decides anything.
func parseClaims(token string) (*authentication.TokenClaim, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("not a JWT")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}

	var claim authentication.TokenClaim
	if err := json.Unmarshal(payload, &claim); err != nil {
		return nil, err
	}
	return &claim, nil
}

// decodeBasic reads a base64 "tenant/user:password".
func decodeBasic(encoded string) (tenant, username, password string, ok bool) {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", "", "", false
	}

	user, password, found := strings.Cut(string(decoded), ":")
	if !found {
		return "", "", "", false
	}

	tenant, username = authentication.SplitTenantUser(user)
	return tenant, username, password, true
}

// isRead is whether a method only reads. A browser session may read without an
// XSRF token; anything else needs one.
func isRead(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}
