package service_test

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/service"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/testfixture"
)

// twoTenants is the usual arrangement: an owner tenant the microservice is
// deployed on, a source holding a hierarchy, and an empty target.
func twoTenants(t *testing.T) *platform {
	t.Helper()
	return newPlatform(t, map[string]*testfixture.Server{
		bootstrapTenant: testfixture.New(t, "empty.json"),
		"t-source":      testfixture.New(t, "hierarchy.json"),
		"t-target":      testfixture.New(t, "empty.json"),
	})
}

func TestStartupLoadsTheSubscribedTenants(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	if p.SubscriptionCalls() == 0 {
		t.Fatal("the subscriptions were never read, so no service user can have been loaded")
	}
	if svc.OwnerTenant() != bootstrapTenant {
		t.Fatalf("owner tenant = %q, want %q", svc.OwnerTenant(), bootstrapTenant)
	}

	got := strings.Join(svc.Tenants(t.Context()), ",")
	for _, want := range []string{bootstrapTenant, "t-source", "t-target"} {
		if !strings.Contains(got, want) {
			t.Fatalf("tenants = %q, missing %q", got, want)
		}
	}
}

// The owner tenant is where job records are written, so a service that cannot
// act there is misconfigured in a way that only shows up later. It has to
// refuse at startup.
func TestStartupRefusesWhenTheOwnerTenantIsNotSubscribed(t *testing.T) {
	p := twoTenants(t)
	p.Unsubscribe(bootstrapTenant)

	_, err := service.New(t.Context(), service.Options{InsecureSkipVerify: true})
	if err == nil {
		t.Fatal("the service started without a service user on its own tenant")
	}
	for _, want := range []string{bootstrapTenant, "job records", "subscribe"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error does not mention %q: %v", want, err)
		}
	}
}

func TestStartupRefusesWithoutBootstrapCredentials(t *testing.T) {
	p := twoTenants(t)
	_ = p
	t.Setenv("C8Y_BOOTSTRAP_USER", "")

	_, err := service.New(t.Context(), service.Options{InsecureSkipVerify: true})
	if err == nil {
		t.Fatal("the service started with no bootstrap user")
	}
	if !strings.Contains(err.Error(), "C8Y_BOOTSTRAP_USER") {
		t.Fatalf("error should name the missing variable: %v", err)
	}
}

func TestTenantsEndpointReportsWhatTheServiceCanReach(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodGet, "/tenants", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}

	body := decode(t, recorder)
	if body["ownerTenant"] != bootstrapTenant {
		t.Fatalf("ownerTenant = %v", body["ownerTenant"])
	}
	tenants, ok := body["tenants"].([]any)
	if !ok || len(tenants) != 3 {
		t.Fatalf("tenants = %v", body["tenants"])
	}
}

func TestHealthEndpointNeedsNoCredentials(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	// The platform's liveness probe carries no credentials at all, so a health
	// endpoint behind the authorization check would fail every probe and get the
	// container restarted.
	r := request(t, http.MethodGet, "/health", nil)
	r.Header.Del("Authorization")

	recorder := call(t, svc, r)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), `"UP"`) {
		t.Fatalf("health body = %s", recorder.Body)
	}
}

func TestPreflightProvesBothTenants(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodPost, "/preflight",
		service.PreflightRequest{SourceTenant: "t-source", TargetTenant: "t-target"}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}

	body := decode(t, recorder)
	source, _ := body["source"].(map[string]any)
	target, _ := body["target"].(map[string]any)
	if source["tenant"] != "t-source" || target["tenant"] != "t-target" {
		t.Fatalf("tenants not reported back: %s", recorder.Body)
	}
	// Proof that the service user really authenticated, and against the right
	// tenant: each fixture answers /tenant/currentTenant with its own identity.
	if source["reportedTenant"] != "t-source" {
		t.Fatalf("the source credentials were never proven: %s", recorder.Body)
	}
	if source["serviceUser"] != serviceUser {
		t.Fatalf("serviceUser = %v", source["serviceUser"])
	}
}

// migrate does not check this itself - it disables the importer's own check,
// because the CLI checks it in a pre-flight the service cannot reach. So this is
// the service's job, and losing it would let a hierarchy be duplicated inside
// its own tenant.
func TestPreflightRefusesTheSameTenantOnBothSides(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodPost, "/preflight",
		service.PreflightRequest{SourceTenant: "t-source", TargetTenant: "t-source"}))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), "same tenant") {
		t.Fatalf("body = %s", recorder.Body)
	}
}

func TestPreflightNamesATenantThatIsNotSubscribed(t *testing.T) {
	p := twoTenants(t)
	p.Unsubscribe("t-target")
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodPost, "/preflight",
		service.PreflightRequest{SourceTenant: "t-source", TargetTenant: "t-target"}))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), "t-target") {
		t.Fatalf("the response does not say which tenant is missing: %s", recorder.Body)
	}
}

func TestPreflightRequiresBothTenants(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodPost, "/preflight",
		service.PreflightRequest{SourceTenant: "t-source"}))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
}

// --- authorization ----------------------------------------------------------

func TestACallerFromASubtenantIsRefused(t *testing.T) {
	p := twoTenants(t)
	// A source-tenant admin with the role, which is deliberately still not
	// enough: they could otherwise copy devices into a tenant they have no
	// standing in.
	p.AddUser("t-source", "admin", "source-secret", service.RequiredRole)
	svc := p.start(t)

	r := request(t, http.MethodGet, "/tenants", nil)
	authenticate(r, "t-source", "admin", "source-secret")

	recorder := call(t, svc, r)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), bootstrapTenant) {
		t.Fatalf("the refusal should say which tenant may call: %s", recorder.Body)
	}
}

func TestACallerWithoutTheRoleIsRefused(t *testing.T) {
	p := twoTenants(t)
	p.AddUser(bootstrapTenant, "reader", "reader-secret", "ROLE_INVENTORY_READ")
	svc := p.start(t)

	r := request(t, http.MethodGet, "/tenants", nil)
	authenticate(r, bootstrapTenant, "reader", "reader-secret")

	recorder := call(t, svc, r)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), service.RequiredRole) {
		t.Fatalf("the refusal should name the missing role: %s", recorder.Body)
	}
}

func TestUnknownCredentialsAreRejectedByThePlatform(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	r := request(t, http.MethodGet, "/tenants", nil)
	authenticate(r, bootstrapTenant, "admin", "wrong-password")

	recorder := call(t, svc, r)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
}

func TestARequestWithoutCredentialsIsRejected(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	r := request(t, http.MethodGet, "/tenants", nil)
	r.Header.Del("Authorization")

	recorder := call(t, svc, r)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
}

// Basic auth without a tenant prefix is ambiguous - the service cannot tell
// which tenant the caller belongs to, so it must refuse rather than assume the
// owner tenant.
func TestCredentialsWithoutATenantAreRejected(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	r := request(t, http.MethodGet, "/tenants", nil)
	authenticate(r, "", "admin", "admin-secret")

	recorder := call(t, svc, r)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), "tenant") {
		t.Fatalf("body = %s", recorder.Body)
	}
}

// The subscription refresh runs on its own goroutine for the whole life of the
// container, so anything it touches is touched concurrently with request
// handling. Reading the SDK's ServiceUsers field directly here was a data race
// until this test found it, and -race is the only thing that would.
func TestRefreshingSubscriptionsRacesNothing(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				if err := svc.RefreshSubscriptions(t.Context()); err != nil {
					t.Error(err)
					return
				}
			}
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				recorder := call(t, svc, request(t, http.MethodGet, "/tenants", nil))
				if recorder.Code != http.StatusOK {
					t.Errorf("status = %d", recorder.Code)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// An id identifies a tenant and describes nothing. What somebody choosing
// between two of them reads is the name, and what tells two look-alike names
// apart is the domain.
func TestTenantsAreListedWithWhatTheyAreCalled(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	described := svc.TenantDescriptions(t.Context())
	if len(described) == 0 {
		t.Fatal("no tenants at all")
	}

	for _, tenant := range described {
		if tenant.ID == "" {
			t.Errorf("a tenant with no id: %+v", tenant)
		}
		if tenant.Name != "Werk "+tenant.ID {
			t.Errorf("%s is called %q, and tenant management says otherwise", tenant.ID, tenant.Name)
		}
		if tenant.Domain != tenant.ID+".example.com" {
			t.Errorf("%s has domain %q", tenant.ID, tenant.Domain)
		}
	}
}

// One call describes every tenant, and it is made once: this list is drawn every
// time somebody opens the screen, and every refresh after that.
func TestTenantNamesAreReadOnceForAllOfThem(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	for range 3 {
		svc.TenantDescriptions(t.Context())
	}

	if asked := p.TenantListCalls(); asked != 1 {
		t.Errorf("read tenant management %d times, want 1", asked)
	}
	if asked := p.CurrentTenantCalls("t-source"); asked != 0 {
		t.Errorf("asked t-source about itself %d times, and tenant management had already said",
			asked)
	}
}

// Without ROLE_TENANT_MANAGEMENT_READ there are no company names - but each
// tenant can still say where it lives, which is the more useful half anyway.
func TestWithoutTenantManagementTheHostsStillArrive(t *testing.T) {
	p := twoTenants(t)
	p.RefuseTenantManagement()
	svc := p.start(t)

	for _, tenant := range svc.TenantDescriptions(t.Context()) {
		if tenant.Name != "" {
			t.Errorf("%s was given a name nobody could read: %q", tenant.ID, tenant.Name)
		}
		if tenant.Domain != tenant.ID+".example.com" {
			t.Errorf("%s has host %q, which is not what the tenant said", tenant.ID, tenant.Domain)
		}
	}
}

// A tenant nobody can describe is still a tenant somebody may need to migrate.
// Losing the name is cosmetic; losing the tenant is not.
func TestATenantThatCannotBeDescribedIsStillListed(t *testing.T) {
	p := twoTenants(t)
	p.RefuseTenantManagement()
	p.Unsubscribe("t-source") // no service user, so nothing can ask it anything
	svc := p.start(t)

	described := svc.TenantDescriptions(t.Context())
	for _, tenant := range described {
		if tenant.ID == "" {
			t.Errorf("a tenant with no id at all: %+v", tenant)
		}
	}
	if len(described) == 0 {
		t.Error("the whole list disappeared")
	}
}

// The endpoint answers with the same thing, since that is what the UI reads.
func TestTheTenantsEndpointCarriesTheNames(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	body := decode(t, call(t, svc, request(t, http.MethodGet, "/tenants", nil)))
	tenants, _ := body["tenants"].([]any)
	if len(tenants) == 0 {
		t.Fatalf("no tenants in %v", body)
	}

	first, _ := tenants[0].(map[string]any)
	for _, field := range []string{"id", "name", "domain"} {
		if first[field] == nil || first[field] == "" {
			t.Errorf("the listing has no %s: %v", field, first)
		}
	}
}
