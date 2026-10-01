package service_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/service"
)

// The one endpoint a caller from outside the owner tenant may reach.
//
// A migrated device asks where to reconnect and who it now is. What these tests
// hold in place: the identity comes from the platform rather than the request,
// the password is handed over exactly once, and "nothing to do" is an ordinary
// answer rather than a failure.

const (
	deviceExternalID = "LINE-A-SN"
	deviceUser       = "device_" + deviceExternalID
	devicePassword   = "device-secret"
)

// migratedDevice runs a migration that carries a device, and registers that
// device's own user in the source tenant so it can call as itself.
//
// The stamp is on, because that is the order Option B depends on: the object is
// created owned by the user this endpoint will later create.
func migratedDevice(t *testing.T) (*platform, *service.Service, map[string]any) {
	t.Helper()

	p := twoTenants(t)
	// A device of the source tenant, with the roles the platform gives one.
	p.AddUser("t-source", deviceUser, devicePassword, "ROLE_DEVICE", "ROLE_INVENTORY_READ")
	svc := p.start(t)

	job := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
		DeviceSelfCredentials: true,
	})
	if job["status"] != string(service.StatusSucceeded) {
		t.Fatalf("status = %v, error %v", job["status"], job["error"])
	}
	return p, svc, job
}

// deviceRequest is a call as the device rather than as an administrator.
func deviceRequest(t *testing.T, tenant, username, password string) *http.Request {
	t.Helper()
	r := request(t, http.MethodPost, "/devices/self/migration", nil)
	authenticate(r, tenant, username, password)
	return r
}

func TestADeviceIsToldWhereToGoAndWhoItIs(t *testing.T) {
	p, svc, job := migratedDevice(t)

	body := decode(t, call(t, svc, deviceRequest(t, "t-source", deviceUser, devicePassword)))

	if body["migrate"] != true {
		t.Fatalf("migrate = %v, want true: %v", body["migrate"], body)
	}
	if got := fmt.Sprint(body["tenant"]); got != "t-target" {
		t.Errorf("tenant = %q, want t-target", got)
	}
	if got := fmt.Sprint(body["username"]); got != deviceUser {
		t.Errorf("username = %q, want %q", got, deviceUser)
	}
	if fmt.Sprint(body["password"]) == "" || body["password"] == nil {
		t.Error("no password was issued")
	}
	if got := fmt.Sprint(body["runId"]); got != fmt.Sprint(job["runId"]) {
		t.Errorf("runId = %q, want the migration's %q", got, job["runId"])
	}
	if fmt.Sprint(body["host"]) == "" || body["host"] == nil {
		t.Error("no host to reconnect to")
	}

	// The user really was created in the target, not just named in the answer.
	if len(p.tenants["t-target"].Registrations()) == 0 {
		t.Error("no device user was registered in the target tenant")
	}
}

// The whole point of minting on demand: the password exists in that one response
// and nowhere else, so a second call cannot produce it again. But the endpoint has
// to keep answering where to go, because a device retrying a failed move asks
// again every few minutes.
func TestThePasswordIsIssuedOnceAndTheEndpointKeepsAnswering(t *testing.T) {
	_, svc, _ := migratedDevice(t)

	first := decode(t, call(t, svc, deviceRequest(t, "t-source", deviceUser, devicePassword)))
	if first["password"] == nil {
		t.Fatalf("the first call issued no password: %v", first)
	}

	second := decode(t, call(t, svc, deviceRequest(t, "t-source", deviceUser, devicePassword)))
	if second["password"] != nil {
		t.Error("a second call issued another password, so a device that cannot keep " +
			"one would rotate its own credential forever")
	}
	if second["alreadyIssued"] != true {
		t.Errorf("alreadyIssued = %v, want true", second["alreadyIssued"])
	}
	// Still told where to go, and who it is.
	if second["migrate"] != true {
		t.Errorf("migrate = %v on the second call, want true", second["migrate"])
	}
	if got := fmt.Sprint(second["tenant"]); got != "t-target" {
		t.Errorf("tenant = %q on the second call", got)
	}
	// And it says how to recover, which is the only way back.
	if !strings.Contains(fmt.Sprint(second["message"]), service.IssuedFragment) {
		t.Errorf("the second answer does not say how to re-issue: %v", second["message"])
	}
}

// Recorded on the object, never the secret. The record is what makes the
// once-only property survive a restart of this service.
func TestIssuanceIsRecordedOnTheObjectWithoutTheSecret(t *testing.T) {
	p, svc, _ := migratedDevice(t)

	body := decode(t, call(t, svc, deviceRequest(t, "t-source", deviceUser, devicePassword)))
	password := fmt.Sprint(body["password"])

	var found bool
	for _, object := range p.tenants["t-target"].Created() {
		if !gjson.GetBytes(object, gjson.Escape(service.IssuedFragment)).Exists() {
			continue
		}
		found = true
		if strings.Contains(string(object), password) {
			t.Error("the password was written into the managed object")
		}
		if gjson.GetBytes(object, gjson.Escape(service.IssuedFragment)+".issuedAt").String() == "" {
			t.Error("the issuance record carries no timestamp")
		}
	}
	if !found {
		t.Error("no object records that a credential was issued")
	}
}

// A device that was never migrated asks on a timer like any other. "Nothing to
// do" has to be an ordinary answer, or every such device fills its log with
// failures.
func TestADeviceWithNoMigrationIsToldToStayPut(t *testing.T) {
	p := twoTenants(t)
	p.AddUser("t-source", "device_UNMIGRATED", devicePassword, "ROLE_DEVICE")
	svc := p.start(t)

	recorder := call(t, svc, deviceRequest(t, "t-source", "device_UNMIGRATED", devicePassword))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
	}
	body := decode(t, recorder)
	if body["migrate"] != false {
		t.Errorf("migrate = %v, want false", body["migrate"])
	}
	if body["password"] != nil {
		t.Error("a password was issued to a device nothing migrated")
	}
}

// The exception is for devices. A person starts a migration through
// POST /migrations, and this endpoint says so rather than refusing blankly.
func TestAPersonIsRefusedAndToldWhereToGoInstead(t *testing.T) {
	_, svc, _ := migratedDevice(t)

	recorder := call(t, svc, deviceRequest(t, bootstrapTenant, "admin", "admin-secret"))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "POST /migrations") {
		t.Errorf("the refusal does not say what a person should call: %s", body)
	}
}

// ROLE_DEVICE is a bar rather than a boundary - a tenant administrator can grant
// it - but a user merely named device_* does not get through on the name alone.
func TestAUserNamedLikeADeviceWithoutTheRoleIsRefused(t *testing.T) {
	p := twoTenants(t)
	p.AddUser("t-source", "device_PRETENDER", devicePassword, "ROLE_INVENTORY_READ")
	svc := p.start(t)

	recorder := call(t, svc, deviceRequest(t, "t-source", "device_PRETENDER", devicePassword))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "ROLE_DEVICE") {
		t.Errorf("the refusal does not name what is missing: %s", body)
	}
}

// The flag is what bounds the exception: a migration whose operator did not ask
// for device self-service is not collectable, however well the device
// authenticates. Without this, every migration this service ever ran would be
// standing permission for its devices to mint credentials.
func TestAMigrationThatDidNotOptInIsNotCollectable(t *testing.T) {
	p := twoTenants(t)
	p.AddUser("t-source", deviceUser, devicePassword, "ROLE_DEVICE")
	svc := p.start(t)

	job := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
	})
	if job["status"] != string(service.StatusSucceeded) {
		t.Fatalf("the migration failed: %v", job["error"])
	}

	body := decode(t, call(t, svc, deviceRequest(t, "t-source", deviceUser, devicePassword)))
	if body["migrate"] != false {
		t.Errorf("migrate = %v, want false for a migration that did not opt in", body["migrate"])
	}
	if body["password"] != nil {
		t.Error("a device collected a credential from a migration that did not enable it")
	}
	if len(p.tenants["t-target"].Registrations()) != 0 {
		t.Error("a device user was registered for a migration that did not enable self-service")
	}
}

// The two routes to a credential are mutually exclusive: one hands the operator
// a file, the other lets devices collect, and a device collecting would rotate
// the password already in that file.
func TestTheTwoCredentialRoutesAreRefusedTogether(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodPost, "/migrations", service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
		ProvisionCredentials:  true,
		DeviceSelfCredentials: true,
	}))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "rotate") {
		t.Errorf("the refusal does not explain the conflict: %s", body)
	}
}

// Device self-service implies the ownership stamp: without it a device arrives
// bound to its twin and unable to write it.
func TestDeviceSelfCredentialsStampsOwnership(t *testing.T) {
	p, _, _ := migratedDevice(t)

	var stamped int
	for _, object := range p.tenants["t-target"].Created() {
		if strings.HasPrefix(gjson.GetBytes(object, "owner").String(), "device_") {
			stamped++
		}
	}
	if stamped == 0 {
		t.Error("no migrated object is owned by the device user it will authenticate as")
	}
}

func TestADeviceWithoutCredentialsIsUnauthenticated(t *testing.T) {
	_, svc, _ := migratedDevice(t)

	r := request(t, http.MethodPost, "/devices/self/migration", nil)
	r.Header.Del("Authorization")
	if recorder := call(t, svc, r); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", recorder.Code, recorder.Body)
	}
}

// A working device password must not sit in a proxy after the one call that
// produced it.
func TestTheAnswerIsNotCacheable(t *testing.T) {
	_, svc, _ := migratedDevice(t)

	recorder := call(t, svc, deviceRequest(t, "t-source", deviceUser, devicePassword))
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// What a device asking on a timer costs this service.
//
// Every ask used to list the owner tenant's job records and then, per candidate
// migration, resolve an identity and read an object in the target tenant - all
// with this service's own service users, so a caller asking in a loop spends the
// platform's budget rather than its own.

func TestTheJobRecordsAreNotReReadForEveryDevice(t *testing.T) {
	p, svc, _ := migratedDevice(t)
	owner := p.tenants[bootstrapTenant]

	before := owner.CountExact("/inventory/managedObjects")
	for range 4 {
		if recorder := call(t, svc, deviceRequest(t, "t-source", deviceUser, devicePassword)); recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
		}
	}
	asked := owner.CountExact("/inventory/managedObjects") - before

	// One listing covers the lot: everything that decides whether a migration can
	// answer for a device is terminal, so re-reading it per caller buys nothing.
	if asked > 1 {
		t.Errorf("the job records were listed %d times for 4 devices", asked)
	}
}

// A device of a tenant with nothing to collect is the common case once a fleet
// has moved, and it should cost nothing in the other tenants.
func TestADeviceWithNothingToCollectTouchesNoOtherTenant(t *testing.T) {
	p := twoTenants(t)
	p.AddUser("t-source", deviceUser, devicePassword, "ROLE_DEVICE")
	svc := p.start(t)

	target := p.tenants["t-target"]
	before := len(target.Requests())

	recorder := call(t, svc, deviceRequest(t, "t-source", deviceUser, devicePassword))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	if decode(t, recorder)["migrate"] != false {
		t.Fatalf("a device with no migration was told to move: %s", recorder.Body)
	}
	if asked := len(target.Requests()) - before; asked != 0 {
		t.Errorf("%d calls were made into the target tenant to answer 'nothing to do'", asked)
	}
}

// A migration that has just succeeded has to be collectable at once. The held
// index is dropped when a job finishes, so this cannot wait for it to expire.
func TestAFinishedMigrationIsCollectableImmediately(t *testing.T) {
	p := twoTenants(t)
	p.AddUser("t-source", deviceUser, devicePassword, "ROLE_DEVICE", "ROLE_INVENTORY_READ")
	svc := p.start(t)

	// Asking first is what puts an index in place that says there is nothing.
	if recorder := call(t, svc, deviceRequest(t, "t-source", deviceUser, devicePassword)); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}

	job := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
		DeviceSelfCredentials: true,
	})
	if job["status"] != string(service.StatusSucceeded) {
		t.Fatalf("status = %v, error %v", job["status"], job["error"])
	}

	body := decode(t, call(t, svc, deviceRequest(t, "t-source", deviceUser, devicePassword)))
	if body["migrate"] != true {
		t.Fatalf("a device was not told about a migration that had just succeeded: %v", body)
	}
}

// A caller asking far more often than a device would is refused, because the
// answer cannot have changed and the search costs platform calls in other
// tenants. The device is told when to come back rather than why.
func TestACallerAskingInALoopIsRefused(t *testing.T) {
	p, svc, _ := migratedDevice(t)

	var limited *httptest.ResponseRecorder
	for range 20 {
		recorder := call(t, svc, deviceRequest(t, "t-source", deviceUser, devicePassword))
		if recorder.Code == http.StatusTooManyRequests {
			limited = recorder
			break
		}
	}
	if limited == nil {
		t.Fatal("a caller asking twenty times in a row was never refused")
	}
	if limited.Header().Get("Retry-After") == "" {
		t.Error("the refusal does not say when to come back")
	}
	if got := decode(t, limited)["error"]; got != "devicemigration/tooManyRequests" {
		// Not devicemigration/busy, which means a migration is running and says
		// nothing about the caller.
		t.Errorf("error kind is %v", got)
	}

	// And one device's behaviour does not cost another device its allowance.
	p.AddUser("t-source", "device_OTHER", devicePassword, "ROLE_DEVICE")
	other := call(t, svc, deviceRequest(t, "t-source", "device_OTHER", devicePassword))
	if other.Code == http.StatusTooManyRequests {
		t.Error("a second device was refused because the first had been asking")
	}
}
