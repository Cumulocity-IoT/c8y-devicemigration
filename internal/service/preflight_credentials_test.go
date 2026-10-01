package service_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/service"
)

// Whether credentials can be provisioned is a question about the devices, not
// about the two tenants, so it is asked for and it needs a hierarchy. What it
// answers is the one case where registering a device reaches a device the
// migration is not about: an external identity the target tenant already holds.

func TestPreflightDoesNotCheckCredentialsUnlessAsked(t *testing.T) {
	p := twoTenants(t)
	p.tenants["t-target"].HoldIdentity("c8y_Serial", "LINE-A-SN", "4242")
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodPost, "/preflight",
		service.PreflightRequest{SourceTenant: "t-source", TargetTenant: "t-target"}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}

	body := decode(t, recorder)
	if body["credentialsRefused"] != nil {
		t.Errorf("a pre-flight that was not asked answered anyway: %s", recorder.Body)
	}
	if body["conflicts"] != nil {
		t.Errorf("a pre-flight that was not asked walked the hierarchy: %s", recorder.Body)
	}
}

func TestPreflightRefusesCredentialsWhenTheTargetHoldsAnIdentity(t *testing.T) {
	p := twoTenants(t)
	p.tenants["t-target"].HoldIdentity("c8y_Serial", "LINE-A-SN", "4242")
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodPost, "/preflight",
		service.PreflightRequest{
			SourceTenant: "t-source", TargetTenant: "t-target",
			RootID: "1", ProvisionCredentials: true,
		}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}

	body := decode(t, recorder)
	if body["credentialsRefused"] != true {
		t.Fatalf("credentialsRefused = %v, want true: %s", body["credentialsRefused"], recorder.Body)
	}

	conflicts, _ := body["conflicts"].([]any)
	if len(conflicts) != 1 {
		t.Fatalf("reported %d conflicts, want 1: %s", len(conflicts), recorder.Body)
	}
	conflict, _ := conflicts[0].(map[string]any)
	if conflict["externalId"] != "LINE-A-SN" {
		t.Errorf("conflict names %v, want LINE-A-SN", conflict["externalId"])
	}
	// Enough to find the device on both sides, or whoever reads this cannot
	// decide what the duplicate means.
	for _, field := range []string{"sourceId", "targetId", "name", "externalType"} {
		if conflict[field] == nil || conflict[field] == "" {
			t.Errorf("the conflict carries no %s: %v", field, conflict)
		}
	}

	// The two tenants themselves are fine, and the response still says so: it is
	// one option that is unavailable, not the migration.
	source, _ := body["source"].(map[string]any)
	if source["reportedTenant"] != "t-source" {
		t.Errorf("the tenant check was lost: %s", recorder.Body)
	}
}

func TestPreflightPassesCredentialsWhenTheTargetHoldsNothing(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodPost, "/preflight",
		service.PreflightRequest{
			SourceTenant: "t-source", TargetTenant: "t-target",
			RootID: "1", ProvisionCredentials: true,
		}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}

	body := decode(t, recorder)
	if body["credentialsRefused"] != nil {
		t.Errorf("credentialsRefused = %v on a clean target: %s",
			body["credentialsRefused"], recorder.Body)
	}
}

// A caller that asked for the check and gave no hierarchy would otherwise read a
// clean response as the check having passed.
func TestPreflightNeedsAHierarchyToCheckCredentials(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodPost, "/preflight",
		service.PreflightRequest{
			SourceTenant: "t-source", TargetTenant: "t-target",
			ProvisionCredentials: true,
		}))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), "rootId") {
		t.Fatalf("the refusal does not say what is missing: %s", recorder.Body)
	}
}

// The pre-flight is what the screen asks; this is what holds whatever the caller
// did or did not ask. A migration that reaches the engine with an identity the
// target already holds has to fail having created nothing - no device user, and
// no object either, because registration is what would have created them and it
// runs before the import.
func TestAMigrationWithAConflictingIdentityCreatesNothing(t *testing.T) {
	p := twoTenants(t)
	p.tenants["t-target"].HoldIdentity("c8y_Serial", "LINE-A-SN", "4242")
	svc := p.start(t)

	job := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
		ProvisionCredentials: true,
	})

	if job["status"] != string(service.StatusFailed) {
		t.Fatalf("status = %v, want the migration to have been refused", job["status"])
	}
	if registered := p.tenants["t-target"].Registrations(); len(registered) != 0 {
		t.Errorf("%d devices were registered by a refused migration", len(registered))
	}
	if users := p.tenants["t-target"].DeviceUsers(); len(users) != 0 {
		t.Errorf("a refused migration created device users: %v", users)
	}
	if job["credentialsAvailable"] == true {
		t.Error("a refused migration left credentials to collect")
	}
	// Named, so whoever reads the failed job knows which device to look at
	// rather than only that something was refused.
	if !strings.Contains(fmt.Sprint(job["progress"]), "LINE-A-SN") {
		t.Errorf("the failure does not name the device: %v", job["progress"])
	}
}

// Which relationships are followed decides which devices the migration covers,
// so the check has to walk the same ones. Without the scope it would answer
// about a different set of devices - refusing over a device nobody is migrating,
// or passing over one that is.
func TestTheCredentialCheckFollowsTheMigrationsOwnScope(t *testing.T) {
	p := twoTenants(t)
	// A device child of the root, which the default scope reaches and an
	// asset-only migration does not.
	p.tenants["t-target"].HoldIdentity("c8y_Serial", "PLANT-BERLIN-01", "4242")
	svc := p.start(t)

	refused := func(scope service.PreflightRequest) bool {
		t.Helper()
		scope.SourceTenant, scope.TargetTenant = "t-source", "t-target"
		scope.RootID, scope.ProvisionCredentials = "1", true

		recorder := call(t, svc, request(t, http.MethodPost, "/preflight", scope))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
		}
		return decode(t, recorder)["credentialsRefused"] == true
	}

	// The root itself carries the held identity, so every scope that includes it
	// is refused - which is every scope, the root being where the walk starts.
	if !refused(service.PreflightRequest{}) {
		t.Error("the default scope was not refused over the root's own identity")
	}
	if !refused(service.PreflightRequest{NoChildren: true}) {
		t.Error("a root-only migration was not refused over the root's own identity")
	}

	// And a device reached only through a relationship kind that is not
	// followed is not checked at all.
	p2 := twoTenants(t)
	p2.tenants["t-target"].HoldIdentity("c8y_Serial", "LINE-A-SN", "4242")
	svc2 := p2.start(t)

	recorder := call(t, svc2, request(t, http.MethodPost, "/preflight",
		service.PreflightRequest{
			SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
			NoChildren: true, ProvisionCredentials: true,
		}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	if decode(t, recorder)["credentialsRefused"] == true {
		t.Error("a root-only migration was refused over a child it does not carry")
	}
}
