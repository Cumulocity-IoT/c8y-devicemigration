package service_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/authentication"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/service"
)

// What a browser session looks like, so it can be tested at all.
//
// The Cumulocity UI - and the API documentation application's "Try it out" -
// sends no Authorization header. The credential is a JWT in the "authorization"
// cookie, and a write is accompanied by the X-XSRF-TOKEN header. Without tests
// shaped like that, a 401 for every browser caller would go unnoticed until it
// was deployed.

// claims is the part of a session token these tests care about.
type claims struct {
	Tenant    string `json:"ten"`
	User      string `json:"sub"`
	XSRFToken string `json:"xsrfToken"`
}

// mintToken makes a session token. Three base64url segments and no signature,
// because nothing verifies one: the service reads the claims to learn which
// tenant to ask about, and the platform is what decides whether the token is
// real.
func mintToken(t *testing.T, c claims) string {
	t.Helper()

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))

	payload, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}

	return header + "." +
		base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString([]byte("not-a-signature"))
}

// claimsOf reads a token back, for the fixture.
func claimsOf(token string) (claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return claims{}, fmt.Errorf("not a JWT: %q", token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims{}, err
	}
	var decoded claims
	err = json.Unmarshal(payload, &decoded)
	return decoded, err
}

const sessionXSRF = "xsrf-abcdef123456"

// asBrowserSession authenticates a request the way a browser does: the token in
// a cookie, no Authorization header, and the XSRF token as a header for anything
// that is not a read.
func asBrowserSession(t *testing.T, r *http.Request, tenant, username, xsrf string) {
	t.Helper()

	r.Header.Del("Authorization")
	r.AddCookie(&http.Cookie{
		Name: "authorization",
		Value: mintToken(t, claims{
			Tenant: tenant, User: username, XSRFToken: sessionXSRF,
		}),
	})
	if xsrf != "" {
		r.Header.Set("X-XSRF-TOKEN", xsrf)
	}
}

func TestABrowserSessionCanRead(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	// No X-XSRF-TOKEN: a read changes nothing, and requiring it would make the
	// API documentation application useless for browsing.
	r := request(t, http.MethodGet, "/tenants", nil)
	asBrowserSession(t, r, bootstrapTenant, "admin", "")

	recorder := call(t, svc, r)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), bootstrapTenant) {
		t.Fatalf("body = %s", recorder.Body)
	}
}

func TestABrowserSessionCanStartAMigration(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	r := request(t, http.MethodPost, "/migrations", service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
	})
	asBrowserSession(t, r, bootstrapTenant, "admin", sessionXSRF)

	recorder := call(t, svc, r)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
}

// A cookie travels with any request a browser can be talked into making, so a
// write needs proof the caller meant it.
func TestAWriteFromABrowserSessionNeedsTheXSRFHeader(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	r := request(t, http.MethodPost, "/migrations", service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
	})
	asBrowserSession(t, r, bootstrapTenant, "admin", "")

	recorder := call(t, svc, r)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	// The message has to name the header, because whoever sees this has to know
	// what is missing.
	if !strings.Contains(recorder.Body.String(), "X-XSRF-TOKEN") {
		t.Fatalf("the refusal does not say what is missing: %s", recorder.Body)
	}
	if len(p.tenants[bootstrapTenant].Created()) != 0 {
		t.Fatal("a refused write still recorded a job")
	}
}

func TestAWriteWithSomebodyElsesXSRFTokenIsRefused(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	r := request(t, http.MethodPost, "/migrations", service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
	})
	asBrowserSession(t, r, bootstrapTenant, "admin", "xsrf-from-another-session")

	recorder := call(t, svc, r)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
}

// The tenant comes from the token's own claim. Taking it from anywhere else -
// or assuming the owner tenant because that is who usually calls - would let a
// subtenant's administrator through.
func TestABrowserSessionFromASubtenantIsStillRefused(t *testing.T) {
	p := twoTenants(t)
	p.AddUser("t-source", "admin", "source-secret", service.RequiredRole)
	svc := p.start(t)

	r := request(t, http.MethodGet, "/tenants", nil)
	asBrowserSession(t, r, "t-source", "admin", "")

	recorder := call(t, svc, r)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
}

// On a tenant whose UI authenticates with basic credentials, the same cookie
// holds "Basic <base64>" instead of a token.
func TestACookieHoldingBasicCredentialsWorks(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	r := request(t, http.MethodGet, "/tenants", nil)
	r.Header.Del("Authorization")
	r.AddCookie(&http.Cookie{
		Name: "authorization",
		Value: "Basic " + base64.StdEncoding.EncodeToString(
			[]byte(bootstrapTenant+"/admin:admin-secret")),
	})

	recorder := call(t, svc, r)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
}

// A bearer token in the header, which is how a programmatic OAuth caller
// arrives. Reading cookies must not get in its way.
func TestABearerTokenStillWorks(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	r := request(t, http.MethodGet, "/tenants", nil)
	r.Header.Set("Authorization", "Bearer "+mintToken(t, claims{
		Tenant: bootstrapTenant, User: "admin",
	}))

	recorder := call(t, svc, r)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
}

// The platform's words: a tfatoken "always must be included in the request to
// /user/currentUser". Without forwarding it, the platform refuses to verify a
// two-factor session.
func TestATwoFactorTokenIsForwardedToThePlatform(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	r := request(t, http.MethodGet, "/tenants", nil)
	asBrowserSession(t, r, bootstrapTenant, "admin", "")
	r.Header.Set("tfatoken", "two-factor-token")

	recorder := call(t, svc, r)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}

	forwarded := p.TFATokens()
	if len(forwarded) == 0 {
		t.Fatal("the platform was never asked about the caller")
	}
	for _, token := range forwarded {
		if token == "two-factor-token" {
			return
		}
	}
	t.Fatalf("the two-factor token never reached the platform: %q", forwarded)
}

// And a caller who presents none does not get an empty header sent on their
// behalf, which some platforms treat differently from no header at all.
func TestNoTwoFactorHeaderIsSentWhenThereIsNone(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodGet, "/tenants", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}

	recorded := p.TFATokens()
	if len(recorded) == 0 {
		t.Fatal("the platform was never asked about the caller")
	}
	for _, token := range recorded {
		// Absent, not empty: sending an empty header is not the same as sending
		// none, and the platform is entitled to treat them differently.
		if token != headerAbsent {
			t.Fatalf("a tfatoken header was sent anyway: %q", token)
		}
	}
}

// JWT mandates the URL-safe base64 alphabet, and the library's own
// authentication.ParseToken decodes with the standard one - they differ exactly
// when a payload needs '-' or '_'. This mints a token that does, checks that the
// library cannot read it, and checks that the service can. If the library is
// ever fixed, the first assertion fails and the local decoder can go.
func TestASessionTokenUsingTheURLSafeAlphabetIsUnderstood(t *testing.T) {
	p := twoTenants(t)

	// A character at an offset that lands in the last of base64's four output
	// symbols produces '-' or '_' - '?' and '>' both do it. Which offset that is
	// depends on the length of everything before it, so the name is padded until
	// the encoding really exercises the alphabet.
	var token, username string
	for pad := range 3 {
		username = "adm" + strings.Repeat("n", pad) + "?"
		token = mintToken(t, claims{Tenant: bootstrapTenant, User: username})
		if strings.ContainsAny(strings.Split(token, ".")[1], "-_") {
			break
		}
		token = ""
	}
	if token == "" {
		t.Fatal("could not mint a token whose payload uses the URL-safe alphabet, " +
			"so this test would prove nothing")
	}

	if _, err := authentication.ParseToken(token); err == nil {
		t.Error("authentication.ParseToken now reads URL-safe payloads, so the local " +
			"decoder in credentials.go is no longer needed")
	}

	p.AddUser(bootstrapTenant, username, "irrelevant", service.RequiredRole)
	svc := p.start(t)

	r := request(t, http.MethodGet, "/tenants", nil)
	r.Header.Del("Authorization")
	r.AddCookie(&http.Cookie{Name: "authorization", Value: token})

	recorder := call(t, svc, r)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
}
