package service_test

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/service"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/testfixture"
)

// A real platform is one host serving many tenants: which tenant a request
// lands in is decided by the credentials, not the URL. The test fixture models
// exactly that - one front server that reads the tenant out of the Basic auth
// and proxies to that tenant's testfixture.Server. Without this the service
// could not be tested against two tenants at all, because it only ever knows
// one base URL.
//
// The front server also answers the two endpoints the fixtures do not model:
// the application's subscriptions, and the current user.

const (
	bootstrapTenant   = "t-owner"
	bootstrapUser     = "servicebootstrap_devicemigration"
	bootstrapPassword = "bootstrap-secret"
	serviceUser       = "service_devicemigration"
)

// platform is a fake Cumulocity: a set of tenants, the application's
// subscriptions, and the users who may call the microservice.
type platform struct {
	*httptest.Server

	t *testing.T

	mu sync.Mutex
	// tenants maps a tenant id to the fixture serving its API.
	tenants map[string]*testfixture.Server
	// subscribed is the tenant list the subscriptions endpoint reports, kept
	// separate from tenants so a tenant can exist but not be subscribed.
	subscribed []string
	// users maps "tenant/username" to a password and its effective roles.
	users map[string]platformUser
	// requests counts calls per path prefix, so a test can assert that the
	// subscriptions endpoint was reached with the bootstrap user.
	subscriptionCalls int
	// delay slows every tenant API call, which is what makes a migration last
	// long enough to be cancelled while it is running.
	delay time.Duration
	// tfaTokens is the tfatoken header of every /user/currentUser call, so a
	// test can assert that a two-factor session's token was forwarded.
	tfaTokens []string
}

type platformUser struct {
	password string
	roles    []string
}

// newPlatform starts a platform serving the named tenants. The first tenant is
// the owner tenant, because that is where the microservice is deployed and where
// job records go.
func newPlatform(t *testing.T, tenants map[string]*testfixture.Server) *platform {
	t.Helper()

	p := &platform{
		t:       t,
		tenants: tenants,
		users:   make(map[string]platformUser),
	}
	for tenant := range tenants {
		p.subscribed = append(p.subscribed, tenant)
	}

	p.Server = httptest.NewTLSServer(http.HandlerFunc(p.handle))
	t.Cleanup(p.Server.Close)

	// The operator who drives the service in most tests: an owner-tenant user
	// holding the required role.
	p.AddUser(bootstrapTenant, "admin", "admin-secret", service.RequiredRole)

	// The platform's own environment, as injected into a microservice container.
	t.Setenv("C8Y_BASEURL", p.URL)
	t.Setenv("C8Y_BOOTSTRAP_TENANT", bootstrapTenant)
	t.Setenv("C8Y_BOOTSTRAP_USER", bootstrapUser)
	t.Setenv("C8Y_BOOTSTRAP_PASSWORD", bootstrapPassword)
	t.Setenv("C8Y_MICROSERVICE_ISOLATION", "MULTI_TENANT")

	return p
}

// AddUser registers a user who may authenticate against this platform.
func (p *platform) AddUser(tenant, username, password string, roles ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.users[tenant+"/"+username] = platformUser{password: password, roles: roles}
}

// Unsubscribe removes a tenant from the subscription list while leaving its API
// in place, which is what an unsubscribed tenant looks like from inside the
// microservice.
func (p *platform) Unsubscribe(tenant string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	kept := p.subscribed[:0]
	for _, name := range p.subscribed {
		if name != tenant {
			kept = append(kept, name)
		}
	}
	p.subscribed = kept
}

// SlowDown makes every tenant API call take at least d, so a test can catch a
// migration in the middle of one.
func (p *platform) SlowDown(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.delay = d
}

// SubscriptionCalls is how often the subscriptions endpoint was read.
func (p *platform) SubscriptionCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.subscriptionCalls
}

func (p *platform) handle(w http.ResponseWriter, r *http.Request) {
	tenant, username, password, kind, ok := credentialsOf(r)
	if !ok {
		unauthorized(w)
		return
	}

	switch r.URL.Path {
	case "/application/currentApplication/subscriptions":
		// Only the bootstrap user may read this, which is what the platform
		// enforces and therefore what the fixture enforces.
		if tenant != bootstrapTenant || username != bootstrapUser || password != bootstrapPassword {
			unauthorized(w)
			return
		}
		p.writeSubscriptions(w)
		return

	case "/user/currentUser":
		// Values, not Get: an empty header and no header at all are different
		// things to a platform, and only this tells them apart.
		received := headerAbsent
		if values := r.Header.Values("tfatoken"); values != nil {
			received = strings.Join(values, ",")
		}
		p.mu.Lock()
		p.tfaTokens = append(p.tfaTokens, received)
		p.mu.Unlock()
		p.writeCurrentUser(w, tenant, username, password, kind)
		return

	case "/application/currentApplication":
		// Bootstrap logs a warning and carries on when this is missing, so the
		// fixture leaves it missing on purpose - that path should stay covered.
		http.NotFound(w, r)
		return
	}

	// Everything else is a tenant API call, routed by the service user's tenant.
	if kind != authBasic || username != serviceUser {
		unauthorized(w)
		return
	}
	p.proxy(w, r, tenant, password)
}

func (p *platform) writeSubscriptions(w http.ResponseWriter) {
	p.mu.Lock()
	users := make([]map[string]string, 0, len(p.subscribed))
	for _, tenant := range p.subscribed {
		users = append(users, map[string]string{
			"tenant":   tenant,
			"name":     serviceUser,
			"password": servicePassword(tenant),
		})
	}
	p.subscriptionCalls++
	p.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (p *platform) writeCurrentUser(w http.ResponseWriter, tenant, username, password string, kind authKind) {
	p.mu.Lock()
	user, ok := p.users[tenant+"/"+username]
	p.mu.Unlock()

	if !ok {
		unauthorized(w)
		return
	}
	// A password is checked; a token is taken at its word, because the platform
	// checks a signature this fixture does not have. What matters for these
	// tests is that a token naming nobody is refused, which the lookup above
	// already does.
	if kind == authBasic && user.password != password {
		unauthorized(w)
		return
	}

	roles := make([]map[string]string, 0, len(user.roles))
	for _, role := range user.roles {
		roles = append(roles, map[string]string{"id": role, "name": role})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":             username,
		"userName":       username,
		"effectiveRoles": roles,
	})
}

// proxy forwards a tenant API call to that tenant's fixture.
func (p *platform) proxy(w http.ResponseWriter, r *http.Request, tenant, password string) {
	p.mu.Lock()
	backend, known := p.tenants[tenant]
	delay := p.delay
	subscribed := false
	for _, name := range p.subscribed {
		if name == tenant {
			subscribed = true
		}
	}
	p.mu.Unlock()

	if !known || !subscribed || password != servicePassword(tenant) {
		unauthorized(w)
		return
	}

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			// The engine's request was cancelled, which is what a cancelled job
			// looks like from down here.
			return
		}
	}

	target, err := url.Parse(backend.URL)
	if err != nil {
		p.t.Fatal(err)
	}
	reverse := httputil.NewSingleHostReverseProxy(target)
	reverse.Transport = &http.Transport{
		// The fixture behind this also serves TLS with its own certificate.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test fixture
	}
	reverse.ServeHTTP(w, r)
}

// servicePassword is the service user's password in a tenant. Derived rather
// than random so a test can predict it, and distinct per tenant so a client
// built for one tenant cannot accidentally pass for another.
func servicePassword(tenant string) string { return "service-secret-" + tenant }

// authKind is how a request authenticated. The fixture cares because a tenant
// API call only ever comes from a service user with a password, while
// /user/currentUser is reached by whatever the caller had.
type authKind int

const (
	authBasic authKind = iota
	authBearer
)

func credentialsOf(r *http.Request) (tenant, username, password string, kind authKind, ok bool) {
	header := r.Header.Get("Authorization")

	if encoded, found := strings.CutPrefix(header, "Basic "); found {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return "", "", "", authBasic, false
		}
		user, password, found := strings.Cut(string(decoded), ":")
		if !found {
			return "", "", "", authBasic, false
		}
		tenant, username, found := strings.Cut(user, "/")
		if !found {
			// A user without a tenant prefix is exactly what the service
			// refuses, so it has to be representable.
			return "", user, password, authBasic, true
		}
		return tenant, username, password, authBasic, true
	}

	// The service turns a browser session's cookie into a bearer token, which is
	// what arrives here.
	if token, found := strings.CutPrefix(header, "Bearer "); found && token != "" {
		claims, err := claimsOf(token)
		if err != nil {
			return "", "", "", authBearer, false
		}
		return claims.Tenant, claims.User, "", authBearer, true
	}

	return "", "", "", authBasic, false
}

// headerAbsent is what TFATokens records when a request carried no tfatoken
// header, as distinct from one carrying an empty value.
const headerAbsent = "(absent)"

// TFATokens is the tfatoken header of every call to /user/currentUser.
func (p *platform) TFATokens() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.tfaTokens...)
}

func unauthorized(w http.ResponseWriter) {
	writeJSON(w, http.StatusUnauthorized, map[string]string{
		"error":   "security/Unauthorized",
		"message": "invalid credentials",
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// --- helpers for driving the service ----------------------------------------

// start builds a service against this platform, as main does.
func (p *platform) start(t *testing.T) *service.Service {
	t.Helper()

	svc, err := service.New(t.Context(), service.Options{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("the service did not start: %v", err)
	}
	return svc
}

// request builds a request to the service, authenticated as an owner-tenant
// admin unless the caller says otherwise.
func request(t *testing.T, method, path string, body any) *http.Request {
	t.Helper()

	var reader *strings.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(encoded))
	} else {
		reader = strings.NewReader("")
	}

	r := httptest.NewRequest(method, path, reader)
	authenticate(r, bootstrapTenant, "admin", "admin-secret")
	return r
}

func authenticate(r *http.Request, tenant, username, password string) {
	user := username
	if tenant != "" {
		user = tenant + "/" + username
	}
	r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString(
		[]byte(fmt.Sprintf("%s:%s", user, password))))
}

// call runs a request through the service's handler and returns the recorder.
func call(t *testing.T, svc *service.Service, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	svc.Handler().ServeHTTP(recorder, r)
	return recorder
}

// decode reads a JSON response body into a map.
func decode(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, recorder.Body.String())
	}
	return body
}
