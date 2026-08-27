package service_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The proxy exists so the platform's own asset selector can browse a tenant the
// browser has no session in. That only works if what comes back is what the
// platform said - the envelope, the statistics and the ordering included - so
// these tests are mostly about the answer being untouched.

// The envelope has to survive. @c8y/client reads statistics to page, and a
// response reshaped into something tidier would break the component this whole
// endpoint exists for.
func TestTheInventoryEnvelopeIsProxiedUntouched(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodGet,
		"/tenants/t-source/platform/inventory/managedObjects", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("the proxy answered %d: %s", recorder.Code, recorder.Body)
	}

	body := decode(t, recorder)
	objects, ok := body["managedObjects"].([]any)
	if !ok || len(objects) == 0 {
		t.Fatalf("no managedObjects collection came back: %s", recorder.Body)
	}
	if _, ok := body["statistics"].(map[string]any); !ok {
		t.Errorf("no statistics, so a client cannot page: %s", recorder.Body)
	}

	first, _ := objects[0].(map[string]any)
	if first["id"] == nil || first["type"] == nil {
		t.Errorf("the objects are not whole managed objects: %v", first)
	}
}

// Every filter, page and ordering the platform understands has to reach it. The
// asset selector builds its own queries, so anything dropped here is a feature
// of the platform's component silently not working.
func TestTheProxyForwardsTheQueryVerbatim(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	query := "?query=%24filter%3D%28has%28c8y_IsDevice%29%29&pageSize=3&withTotalPages=true"
	recorder := call(t, svc, request(t, http.MethodGet,
		"/tenants/t-source/platform/inventory/managedObjects"+query, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("the proxy answered %d: %s", recorder.Code, recorder.Body)
	}

	forwarded := false
	for _, seen := range p.tenants["t-source"].Queries() {
		if strings.HasPrefix(seen, "/inventory/managedObjects?") &&
			strings.Contains(seen, "pageSize=3") &&
			strings.Contains(seen, "withTotalPages=true") &&
			strings.Contains(seen, "c8y_IsDevice") {
			forwarded = true
		}
	}
	if !forwarded {
		t.Fatalf("the query did not reach the platform:\n%s",
			strings.Join(p.tenants["t-source"].Queries(), "\n"))
	}

	// And it was applied: three of eleven objects, because the platform was
	// asked for three.
	body := decode(t, recorder)
	if objects, _ := body["managedObjects"].([]any); len(objects) != 3 {
		t.Errorf("pageSize=3 returned %d objects", len(objects))
	}
}

// Expanding a group is a child-collection read, which is most of what a picker
// does after the first level.
func TestTheProxyReadsChildCollections(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	for _, collection := range []string{"childAssets", "childDevices", "childAdditions"} {
		recorder := call(t, svc, request(t, http.MethodGet,
			"/tenants/t-source/platform/inventory/managedObjects/1/"+collection, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s answered %d: %s", collection, recorder.Code, recorder.Body)
		}
		if !strings.Contains(recorder.Body.String(), "references") {
			t.Errorf("%s did not come back as a reference collection: %s",
				collection, recorder.Body)
		}
	}
}

// A path parameter must not be a way into the rest of the API.
func TestTheProxyRefusesCollectionsItDoesNotKnow(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodGet,
		"/tenants/t-source/platform/inventory/managedObjects/1/dashboards", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("an unknown collection answered %d, want 400: %s", recorder.Code, recorder.Body)
	}
}

// The inventory and nothing else. Everything the platform can do is one path
// away, and the only thing stopping this from becoming a general-purpose
// credential-borrowing proxy is that no other route exists.
func TestOnlyTheInventoryIsProxied(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	for _, path := range []string{
		"/tenants/t-source/platform/user/currentUser",
		"/tenants/t-source/platform/tenant/currentTenant",
		"/tenants/t-source/platform/measurement/measurements",
		"/tenants/t-source/platform/inventory/managedObjects/1/childAssets/2",
	} {
		recorder := call(t, svc, request(t, http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("GET %s answered %d, want 404: %s", path, recorder.Code, recorder.Body)
		}
	}
}

// A tenant nobody subscribed has no service user, so there is nothing to read
// with and the caller is told exactly that.
func TestTheProxyRefusesAnUnsubscribedTenant(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodGet,
		"/tenants/t-nobody/platform/inventory/managedObjects", nil))
	if recorder.Code == http.StatusOK {
		t.Fatalf("an unsubscribed tenant was read anyway: %s", recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), "subscribed") {
		t.Errorf("the refusal does not say what is wrong: %s", recorder.Body)
	}
}

// The platform's address inside a container is an internal one. A browser cannot
// reach it, and nothing outside the platform should be told what it is - so the
// links in a proxied body point back at this service instead.
func TestTheProxyDoesNotHandOutThePlatformsInternalAddress(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodGet,
		"/tenants/t-source/platform/inventory/managedObjects/1", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("the proxy answered %d: %s", recorder.Code, recorder.Body)
	}

	platform := p.tenants["t-source"].URL
	if strings.Contains(recorder.Body.String(), platform) {
		t.Errorf("the platform's own address %s came through: %s", platform, recorder.Body)
	}

	var object map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &object); err != nil {
		t.Fatal(err)
	}
	self, _ := object["self"].(string)
	if want := "/service/devicemigration/tenants/t-source/platform/inventory/managedObjects/1"; self != want {
		t.Errorf("self = %q, want %q", self, want)
	}
}
