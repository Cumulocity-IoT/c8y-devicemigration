package service_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/export"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/service"
)

// Choosing what to migrate means looking at a tenant the browser has no session
// in. These endpoints exist for that, and for answering "does this id exist"
// before a migration spends minutes finding out it does not.

func inventory(t *testing.T, svc *service.Service, path string) service.InventoryResponse {
	t.Helper()

	recorder := call(t, svc, request(t, http.MethodGet, path, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, body %s", path, recorder.Code, recorder.Body)
	}

	var response service.InventoryResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("body is not an inventory response: %v\n%s", err, recorder.Body)
	}
	return response
}

func TestTheInventoryOfAnotherTenantCanBeRead(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	response := inventory(t, svc, "/tenants/t-source/inventory")
	if response.Tenant != "t-source" {
		t.Fatalf("tenant = %q", response.Tenant)
	}
	if len(response.Objects) == 0 {
		t.Fatal("the source tenant's inventory came back empty")
	}

	// Enough of each object to draw a row: an id, a name, and which kind of
	// thing it is.
	for _, object := range response.Objects {
		if object.ID == "" {
			t.Fatalf("an object has no id: %+v", object)
		}
		if object.Name == "" && object.Type == "" {
			t.Fatalf("object %s has neither name nor type, so it cannot be offered as a choice",
				object.ID)
		}
	}
}

func TestOneObjectComesBackWithItsChildCounts(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodGet, "/tenants/t-source/inventory/1", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}

	var object service.InventoryObject
	if err := json.Unmarshal(recorder.Body.Bytes(), &object); err != nil {
		t.Fatal(err)
	}
	if object.ID != "1" || object.Name != "Plant Berlin" {
		t.Fatalf("object = %+v", object)
	}
	// The counts are what let a picker warn before somebody expands a group of
	// four hundred.
	if len(object.Children) == 0 {
		t.Fatalf("no child counts on %s, so a picker cannot say how big it is", object.ID)
	}
}

// An id that does not exist has to say so here, not minutes into a migration.
func TestAnObjectThatDoesNotExistIsNotFound(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc,
		request(t, http.MethodGet, "/tenants/t-source/inventory/99999999", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), "t-source") {
		t.Fatalf("the refusal does not say which tenant was searched: %s", recorder.Body)
	}
}

func TestDrillingDownReturnsTheChildrenOfAnObject(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	response := inventory(t, svc, "/tenants/t-source/inventory/1/children")
	if len(response.Objects) == 0 {
		t.Fatal("the root reported no children")
	}

	// Each child says which relationship it was reached by, so a picker can
	// group them the way a migration will follow them.
	kinds := map[string]int{}
	for _, object := range response.Objects {
		if object.ChildType == "" {
			t.Fatalf("child %s does not say which relationship it came from", object.ID)
		}
		kinds[object.ChildType]++
	}
	if kinds["asset"] == 0 || kinds["device"] == 0 {
		t.Fatalf("the default child types were not both followed: %v", kinds)
	}
}

func TestDrillingDownFollowsOnlyTheRelationshipsAsked(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	response := inventory(t, svc, "/tenants/t-source/inventory/1/children?childTypes=device")
	if len(response.Objects) == 0 {
		t.Fatal("no device children came back")
	}
	for _, object := range response.Objects {
		if object.ChildType != "device" {
			t.Fatalf("%s is a %s child, which was not asked for", object.ID, object.ChildType)
		}
	}
}

// A search has to reach the platform as a query, not be filtered here: the
// platform ignores type and fragmentType when a query is present, and this
// repository has twice shipped a listing that quietly read a whole tenant.
func TestASearchIsSentToThePlatform(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	inventory(t, svc, "/tenants/t-source/inventory?text=Berlin")

	asked := false
	for _, query := range p.tenants["t-source"].Queries() {
		if strings.HasPrefix(query, "/inventory/managedObjects?") && strings.Contains(query, "Berlin") {
			asked = true
		}
	}
	if !asked {
		t.Fatalf("the search term never reached the platform:\n%s",
			strings.Join(p.tenants["t-source"].Queries(), "\n"))
	}
}

func TestAnUnsubscribedTenantsInventoryIsRefused(t *testing.T) {
	p := twoTenants(t)
	p.Unsubscribe("t-source")
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodGet, "/tenants/t-source/inventory", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), "t-source") {
		t.Fatalf("body = %s", recorder.Body)
	}
}

func TestBrowsingAnotherTenantNeedsTheRole(t *testing.T) {
	p := twoTenants(t)
	p.AddUser("t-source", "admin", "source-secret", service.RequiredRole)
	svc := p.start(t)

	r := request(t, http.MethodGet, "/tenants/t-source/inventory", nil)
	authenticate(r, "t-source", "admin", "source-secret")

	if recorder := call(t, svc, r); recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
}

func TestABadPageSizeIsRefusedRatherThanIgnored(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	for _, path := range []string{
		"/tenants/t-source/inventory?pageSize=lots",
		"/tenants/t-source/inventory?pageSize=0",
		"/tenants/t-source/inventory?pageSize=5000",
		"/tenants/t-source/inventory?currentPage=first",
	} {
		recorder := call(t, svc, request(t, http.MethodGet, path, nil))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s gave %d, body %s", path, recorder.Code, recorder.Body)
		}
	}
}

// The same refusal the migration makes: an id is an id, not a path.
func TestAnObjectIdThatIsAPathIsRefused(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc,
		request(t, http.MethodGet, "/tenants/t-source/inventory/..%2fsecrets", nil))
	if recorder.Code != http.StatusBadRequest && recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
}

// --- estimates ---------------------------------------------------------------

func estimate(t *testing.T, svc *service.Service, spec service.EstimateRequest) service.EstimateResponse {
	t.Helper()

	recorder := call(t, svc, request(t, http.MethodPost, "/estimates", spec))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}

	var response service.EstimateResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("body is not an estimate: %v\n%s", err, recorder.Body)
	}
	return response
}

func TestAnEstimateCountsWhatWouldMove(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	response := estimate(t, svc, service.EstimateRequest{
		SourceTenant: "t-source", RootID: "1",
	})

	if response.Objects != 9 {
		t.Fatalf("objects = %d, want the whole hierarchy of 9", response.Objects)
	}
	if response.Identities != 4 {
		t.Fatalf("identities = %d, want 4", response.Identities)
	}
	if !response.WithinLimits || len(response.Breaches) != 0 {
		t.Fatalf("a hierarchy inside every limit was reported as breaching: %+v", response.Breaches)
	}
	// The allowances travel with the answer, so nothing has to hardcode them.
	if response.Limits.MaxChildObjects != export.MaxChildObjects {
		t.Fatalf("maxChildObjects = %d", response.Limits.MaxChildObjects)
	}
	if response.Limits.PerDevice["measurements"] != export.MaxMeasurementsPerDevice {
		t.Fatalf("per-device limits = %v", response.Limits.PerDevice)
	}
}

func TestAnEstimateCountsRecordsAndNamesTheBusiestDevice(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	response := estimate(t, svc, service.EstimateRequest{
		SourceTenant: "t-source", RootID: "1",
		DataTypes: "measurements", DateFrom: "2026-01-01",
	})

	if response.Records["measurements"] == 0 {
		t.Fatalf("no measurements were counted: %+v", response.Records)
	}
	// The limits are per device, so the total alone cannot say whether a
	// migration would be refused.
	if response.Busiest["measurements"] == 0 {
		t.Fatal("the busiest device was not reported")
	}
	if response.Busiest["measurements"] > response.Records["measurements"] {
		t.Fatalf("the busiest device holds more than the total: %v vs %v",
			response.Busiest, response.Records)
	}
	if response.Window["measurements"].From == "" {
		t.Fatal("the window was not reported, so nobody can see what was counted")
	}
	if _, counted := response.Records["events"]; counted {
		t.Fatalf("events were counted without being asked for: %v", response.Records)
	}
}

// The estimate has to describe the migration that follows it, so it applies the
// same configured default window a run would.
func TestAnEstimateUsesTheConfiguredDefaultWindow(t *testing.T) {
	p := twoTenants(t)
	p.SetOption(bootstrapTenant, service.SettingsCategory, service.SettingDefaultDateFrom, "2026-01-01")
	svc := p.start(t)

	response := estimate(t, svc, service.EstimateRequest{
		SourceTenant: "t-source", RootID: "1", DataTypes: "measurements",
	})

	if response.Records["measurements"] == 0 {
		t.Fatalf("the configured window was not applied, so nothing was counted: %+v",
			response.Window)
	}
	if !strings.HasPrefix(response.Window["measurements"].From, "2026-01-01") {
		t.Fatalf("window = %+v, want the configured default", response.Window["measurements"])
	}
}

// The answer a wizard needs before it lets somebody press Start.
func TestAnEstimateReportsABreachedLimit(t *testing.T) {
	p := twoTenants(t)
	p.tenants["t-source"].SetReportedTotal("measurements", export.MaxMeasurementsPerDevice+1)
	svc := p.start(t)

	response := estimate(t, svc, service.EstimateRequest{
		SourceTenant: "t-source", RootID: "1",
		DataTypes: "measurements", DateFrom: "2026-01-01",
	})

	if response.WithinLimits {
		t.Fatal("a hierarchy over the measurement limit was reported as within limits")
	}
	if len(response.Breaches) != 1 {
		t.Fatalf("breaches = %+v, want one", response.Breaches)
	}
	breach := response.Breaches[0]
	if breach.Kind != "measurements" {
		t.Fatalf("breach kind = %q", breach.Kind)
	}
	if breach.Allowed != export.MaxMeasurementsPerDevice {
		t.Fatalf("breach allowance = %d", breach.Allowed)
	}
	if breach.ObjectID == "" {
		t.Fatal("the breach names no object, so nobody knows what to narrow")
	}
}

func TestAnEstimateCreatesNothing(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	estimate(t, svc, service.EstimateRequest{
		SourceTenant: "t-source", RootID: "1", DataTypes: "all", DateFrom: "2026-01-01",
	})

	for name, server := range p.tenants {
		if created := len(server.Created()); created != 0 {
			t.Fatalf("estimating created %d objects in %s", created, name)
		}
		if deleted := len(server.Deleted()); deleted != 0 {
			t.Fatalf("estimating deleted %d objects in %s", deleted, name)
		}
	}
}

func TestAnEstimateNeedsATenantAndARoot(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	for _, spec := range []service.EstimateRequest{
		{},
		{SourceTenant: "t-source"},
		{RootID: "1"},
		{SourceTenant: "t-source", RootID: "./1"},
		{SourceTenant: "t-source", RootID: "1", DataTypes: "all,events"},
		{SourceTenant: "t-source", RootID: "1", ChildTypes: "asset", NoChildren: true},
	} {
		recorder := call(t, svc, request(t, http.MethodPost, "/estimates", spec))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%+v gave %d, body %s", spec, recorder.Code, recorder.Body)
		}
	}
}

func TestAnEstimateOfAnUnsubscribedTenantSaysWhichOne(t *testing.T) {
	p := twoTenants(t)
	p.Unsubscribe("t-source")
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodPost, "/estimates",
		service.EstimateRequest{SourceTenant: "t-source", RootID: "1"}))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), "t-source") {
		t.Fatalf("body = %s", recorder.Body)
	}
}

// What the wizard actually does: estimate, then migrate the same thing, and the
// two must agree about how many objects there are.
func TestAnEstimateAgreesWithTheMigrationThatFollowsIt(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	spec := service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
	}
	estimated := estimate(t, svc, service.EstimateRequest{
		SourceTenant: spec.SourceTenant, RootID: spec.RootID,
	})

	job := migrate1(t, svc, spec)
	if job["status"] != string(service.StatusSucceeded) {
		t.Fatalf("the migration failed: %v", job["error"])
	}

	created := len(p.tenants["t-target"].Created())
	if created != estimated.Objects {
		t.Fatalf("the estimate promised %d objects and the migration created %d",
			estimated.Objects, created)
	}
	if _, ok := fmt.Sprint(job["runId"]), true; !ok {
		t.Fatal("unreachable")
	}
}
