package service_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/service"
)

// The third answer to "how will these devices authenticate": they already do,
// with a certificate, and nothing has to be issued.
//
// What these tests hold in place is that the flag still *does* something. It
// creates no credential, so it would be easy to treat as documentation - but
// without the ownership stamp it produces a fleet that arrives, binds to its
// migrated twin, and is silently unable to write it.

func TestACertificateFleetStampsOwnershipAndIssuesNothing(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	job := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
		CertificateFleet: true,
	})
	if job["status"] != string(service.StatusSucceeded) {
		t.Fatalf("status = %v, error %v", job["status"], job["error"])
	}

	var stamped int
	for _, object := range p.tenants["t-target"].Created() {
		if strings.HasPrefix(gjson.GetBytes(object, "owner").String(), "device_") {
			stamped++
		}
	}
	if stamped == 0 {
		t.Error("no migrated object is owned by the device user its certificate will " +
			"authenticate as, so the fleet would arrive read-only on its own twins")
	}

	// The whole point of this answer: no user, no password, nothing to hand over.
	if got := p.tenants["t-target"].Registrations(); len(got) != 0 {
		t.Errorf("a certificate fleet registered %d devices", len(got))
	}
	if issued, _ := job["credentialsIssued"].(float64); issued != 0 {
		t.Errorf("credentialsIssued = %v for a certificate fleet", job["credentialsIssued"])
	}
}

// One question, one answer. Asking for a password alongside a chain the devices
// already have is a contradiction, and acting on both would leave every device
// holding two ways to authenticate where one was wanted.
func TestACertificateFleetIsRefusedWithEitherCredentialRoute(t *testing.T) {
	cases := map[string]service.MigrationRequest{
		"a file of passwords": {
			SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
			CertificateFleet: true, ProvisionCredentials: true,
		},
		"devices collecting their own": {
			SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
			CertificateFleet: true, DeviceSelfCredentials: true,
		},
	}

	for name, migration := range cases {
		p := twoTenants(t)
		svc := p.start(t)

		recorder := call(t, svc, request(t, http.MethodPost, "/migrations", migration))
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400: %s", name, recorder.Code, recorder.Body)
			continue
		}
		if body := recorder.Body.String(); !strings.Contains(body, "no password to issue") {
			t.Errorf("%s: the refusal does not explain the conflict: %s", name, body)
		}
	}
}

// The pre-existing hole this closes. provisionCredentials was already refused for
// a remote target and deviceSelfCredentials was not - so the combination was
// accepted, ran, and then answered every asking device "no succeeded migration
// carried this device", with the cause recorded nowhere.
func TestDeviceSelfCredentialsIsRefusedForARemoteTarget(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodPost, "/migrations", service.MigrationRequest{
		SourceTenant: "t-source", RootID: "1",
		RemoteTargetID:        "anything",
		DeviceSelfCredentials: true,
	}))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "device access token") {
		t.Errorf("the refusal does not explain why:\n%s", body)
	}
}

// And the one that must *not* be refused. A certificate fleet creates no user -
// only an owner on objects the token already owns - so it is the only answer that
// works across instances, where there is no cutover to perform either.
func TestACertificateFleetIsAllowedWithARemoteTarget(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	// A dead host, so the run fails reaching it rather than hanging. What is
	// being tested is that it is not the credential validation that refuses.
	established := decode(t, call(t, svc, request(t, http.MethodPost, "/remote-targets",
		service.RemoteTargetRequest{Host: "127.0.0.1:1", Tenant: "t-remote"})))
	id, _ := established["id"].(string)

	recorder := call(t, svc, request(t, http.MethodPost, "/migrations", service.MigrationRequest{
		SourceTenant: "t-source", RootID: "1",
		RemoteTargetID:   id,
		CertificateFleet: true,
	}))

	for _, unwanted := range []string{"no password to issue", "device access token"} {
		if strings.Contains(recorder.Body.String(), unwanted) {
			t.Fatalf("a certificate fleet was refused as though it issued credentials: %s",
				recorder.Body)
		}
	}
}

// Asking for a certificate fleet is not asking for device self-service. The one
// endpoint outside the owner tenant stays bounded by its own flag, or every
// certificate migration would quietly become collectable.
func TestACertificateFleetIsNotCollectableByADevice(t *testing.T) {
	p := twoTenants(t)
	p.AddUser("t-source", deviceUser, devicePassword, "ROLE_DEVICE")
	svc := p.start(t)

	job := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
		CertificateFleet: true,
	})
	if job["status"] != string(service.StatusSucceeded) {
		t.Fatalf("the migration failed: %v", job["error"])
	}

	body := decode(t, call(t, svc, deviceRequest(t, "t-source", deviceUser, devicePassword)))
	if body["migrate"] != false {
		t.Errorf("migrate = %v, want false for a certificate fleet", body["migrate"])
	}
	if body["password"] != nil {
		t.Error("a device collected a password from a migration that issues none")
	}
}
