package service_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/service"
)

// Moving what a tenant trusts.
//
// The operation has an outage in it and no flag to flip back, so what these
// tests hold in place is mostly about *not* starting one: everything checkable is
// checked before the source loses its certificate, and a refused create is put
// back rather than left.

const movableFingerprint = "f75bd686243e6916c94408e1fa65e34367136077"

func cutoverRequest(t *testing.T, body service.TrustAnchorCutoverRequest) *http.Request {
	t.Helper()
	return request(t, http.MethodPost, "/certificate-cutovers", body)
}

func TestTheTruststoreListingSaysWhatCanBeMoved(t *testing.T) {
	p := twoTenants(t)
	source := p.tenants["t-source"]
	source.AddTrustedCertificate(movableFingerprint, map[string]any{
		"name":     "customer-ca",
		"notAfter": time.Now().Add(365 * 24 * time.Hour).Format(time.RFC3339),
	})
	source.AddTrustedCertificate("tenantownca", map[string]any{
		"name":                       "tenantownca",
		"tenantCertificateAuthority": true,
	})
	source.AddTrustedCertificate("expiredca", map[string]any{
		"name":     "expiredca",
		"notAfter": time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
	})
	svc := p.start(t)

	body := decode(t, call(t, svc,
		request(t, http.MethodGet, "/tenants/t-source/trusted-certificates", nil)))

	certificates, _ := body["certificates"].([]any)
	if len(certificates) != 3 {
		t.Fatalf("listed %d certificates, want 3: %v", len(certificates), body)
	}

	movable := map[string]bool{}
	reasons := map[string]string{}
	for _, entry := range certificates {
		fields, _ := entry.(map[string]any)
		fingerprint := fmt.Sprint(fields["fingerprint"])
		movable[fingerprint] = fields["movable"] == true
		reasons[fingerprint] = fmt.Sprint(fields["reason"])
	}

	if !movable[movableFingerprint] {
		t.Errorf("a customer CA was not offered: %s", reasons[movableFingerprint])
	}
	// The tenant's own CA cannot be moved at all, and an expired one would
	// produce a cutover that cannot work after an outage that cannot be avoided.
	if movable["tenantownca"] {
		t.Error("the tenant's own certificate authority was offered as movable")
	}
	if !strings.Contains(reasons["tenantownca"], "re-enrol") {
		t.Errorf("the refusal does not say what such a fleet needs: %s", reasons["tenantownca"])
	}
	if movable["expiredca"] {
		t.Error("an expired certificate was offered as movable")
	}

	// The blast radius, in words rather than a number: the platform records
	// nothing about which devices a certificate authenticates.
	warning := fmt.Sprint(body["warning"])
	if !strings.Contains(warning, "cannot tell you how many") {
		t.Errorf("the listing does not admit it cannot count the fleet: %s", warning)
	}
}

func TestACutoverMovesTheCertificate(t *testing.T) {
	p := twoTenants(t)
	p.tenants["t-source"].AddTrustedCertificate(movableFingerprint,
		map[string]any{"name": movableFingerprint})
	svc := p.start(t)

	body := decode(t, call(t, svc, cutoverRequest(t, service.TrustAnchorCutoverRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", Fingerprint: movableFingerprint,
	})))

	if body["autoRegistrationEnabled"] != true {
		t.Errorf("autoRegistrationEnabled = %v: an arriving device would be refused",
			body["autoRegistrationEnabled"])
	}
	if got := p.tenants["t-source"].TrustedCertificates(); len(got) != 0 {
		t.Errorf("the source still trusts %v", got)
	}
	entry, ok := p.tenants["t-target"].TrustedCertificate(movableFingerprint)
	if !ok {
		t.Fatalf("the target does not trust it: %v", p.tenants["t-target"].TrustedCertificates())
	}
	if entry["status"] != "ENABLED" {
		t.Errorf("the target's copy is %v, want ENABLED", entry["status"])
	}
	if entry["autoRegistrationEnabled"] != true {
		t.Errorf("the target's copy has autoRegistration %v", entry["autoRegistrationEnabled"])
	}
	// The answer must not say the fleet needs pointing at the target's hostname.
	// It does not - the certificate moves the device - and saying so would send
	// an operator after a device-side change that does not exist.
	answer := fmt.Sprint(body["message"])
	if !strings.Contains(answer, "nothing to change on the device") {
		t.Errorf("the answer does not say the fleet moves by itself: %v", answer)
	}
	if strings.Contains(answer, "pointed at") {
		t.Errorf("the answer still asks for a device-side hostname change: %v", answer)
	}
}

// The one that matters most. The source has already lost the certificate by the
// time a create can fail, so the fleet is on neither tenant - putting it back is
// the only recovery, and it has to happen without the caller asking.
func TestARefusedCreatePutsTheCertificateBack(t *testing.T) {
	p := twoTenants(t)
	p.tenants["t-source"].AddTrustedCertificate(movableFingerprint,
		map[string]any{"name": movableFingerprint})
	p.tenants["t-target"].RefuseCertificateCreate()
	svc := p.start(t)

	recorder := call(t, svc, cutoverRequest(t, service.TrustAnchorCutoverRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", Fingerprint: movableFingerprint,
	}))
	if recorder.Code == http.StatusOK {
		t.Fatal("a refused create was reported as a successful cutover")
	}
	if body := recorder.Body.String(); !strings.Contains(body, "put back") {
		t.Errorf("the failure does not say the certificate was restored: %s", body)
	}

	if got := p.tenants["t-source"].TrustedCertificates(); len(got) != 1 {
		t.Errorf("the source trusts %v, so the fleet is on neither tenant", got)
	}
}

// Every refusal below leaves the source untouched, which is the whole point of
// checking before deleting.
func TestNothingIsChangedByARefusedCutover(t *testing.T) {
	expired := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)

	cases := map[string]struct {
		seed    map[string]any
		request service.TrustAnchorCutoverRequest
		status  int
		says    string
	}{
		"the tenant's own certificate authority": {
			seed: map[string]any{"name": movableFingerprint, "tenantCertificateAuthority": true},
			request: service.TrustAnchorCutoverRequest{
				SourceTenant: "t-source", TargetTenant: "t-target", Fingerprint: movableFingerprint},
			status: http.StatusBadRequest, says: "re-enrol",
		},
		"an expired certificate": {
			seed: map[string]any{"name": movableFingerprint, "notAfter": expired},
			request: service.TrustAnchorCutoverRequest{
				SourceTenant: "t-source", TargetTenant: "t-target", Fingerprint: movableFingerprint},
			status: http.StatusBadRequest, says: "expired",
		},
		// Disabled in the source means it authenticates nobody, so there is no
		// fleet here to move - and it is disabled because somebody disabled it,
		// which a cutover is not the place to undo.
		"a disabled certificate": {
			seed: map[string]any{"name": movableFingerprint, "status": "DISABLED"},
			request: service.TrustAnchorCutoverRequest{
				SourceTenant: "t-source", TargetTenant: "t-target", Fingerprint: movableFingerprint},
			status: http.StatusBadRequest, says: "disabled",
		},
		"no readable PEM": {
			seed: map[string]any{"name": movableFingerprint, "certInPemFormat": ""},
			request: service.TrustAnchorCutoverRequest{
				SourceTenant: "t-source", TargetTenant: "t-target", Fingerprint: movableFingerprint},
			status: http.StatusBadRequest, says: "certInPemFormat",
		},
		"a certificate that is not there": {
			seed: map[string]any{"name": movableFingerprint},
			request: service.TrustAnchorCutoverRequest{
				SourceTenant: "t-source", TargetTenant: "t-target", Fingerprint: "nosuchthing"},
			status: http.StatusNotFound, says: "nosuchthing",
		},
		"the same tenant twice": {
			seed: map[string]any{"name": movableFingerprint},
			request: service.TrustAnchorCutoverRequest{
				SourceTenant: "t-source", TargetTenant: "t-source", Fingerprint: movableFingerprint},
			status: http.StatusBadRequest, says: "same tenant",
		},
	}

	for name, tc := range cases {
		p := twoTenants(t)
		p.tenants["t-source"].AddTrustedCertificate(movableFingerprint, tc.seed)
		svc := p.start(t)

		recorder := call(t, svc, cutoverRequest(t, tc.request))
		if recorder.Code != tc.status {
			t.Errorf("%s: status = %d, want %d: %s", name, recorder.Code, tc.status, recorder.Body)
		}
		if !strings.Contains(recorder.Body.String(), tc.says) {
			t.Errorf("%s: the refusal does not mention %q: %s", name, tc.says, recorder.Body)
		}
		// The source still trusts it, so no fleet went offline for a request
		// that was never going to work.
		if got := p.tenants["t-source"].TrustedCertificates(); len(got) != 1 {
			t.Errorf("%s: the source trusts %v after a refusal", name, got)
		}
	}
}

// A fingerprint is unique per instance, so the target having it already means
// there is nothing to move - and finding that out after the delete would be an
// outage for nothing.
func TestACutoverIntoATargetThatAlreadyHasItIsRefused(t *testing.T) {
	p := twoTenants(t)
	p.tenants["t-source"].AddTrustedCertificate(movableFingerprint,
		map[string]any{"name": movableFingerprint})
	p.tenants["t-target"].AddTrustedCertificate(movableFingerprint,
		map[string]any{"name": movableFingerprint})
	svc := p.start(t)

	recorder := call(t, svc, cutoverRequest(t, service.TrustAnchorCutoverRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", Fingerprint: movableFingerprint,
	}))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", recorder.Code, recorder.Body)
	}
	if got := p.tenants["t-source"].TrustedCertificates(); len(got) != 1 {
		t.Errorf("the source lost the certificate for a move that could not happen: %v", got)
	}
}

func TestACutoverNeedsTheMigrationRole(t *testing.T) {
	p := twoTenants(t)
	p.tenants["t-source"].AddTrustedCertificate(movableFingerprint,
		map[string]any{"name": movableFingerprint})
	p.AddUser(bootstrapTenant, "bystander", "secret")
	svc := p.start(t)

	r := cutoverRequest(t, service.TrustAnchorCutoverRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", Fingerprint: movableFingerprint,
	})
	authenticate(r, bootstrapTenant, "bystander", "secret")
	if recorder := call(t, svc, r); recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body)
	}
	if got := p.tenants["t-source"].TrustedCertificates(); len(got) != 1 {
		t.Errorf("an unauthorised request moved the certificate: %v", got)
	}
}

// What a CA may do in the tenant it arrives in is carried across, not decided
// here.
//
// Status and autoRegistrationEnabled both follow the source unless somebody asks
// otherwise. Forcing them to ENABLED and true would switch auto-registration on
// for a tenant that had trusted a CA *without* it, with nothing saying so.

func TestAutoRegistrationIsCarriedAcrossRatherThanForced(t *testing.T) {
	p := twoTenants(t)
	p.tenants["t-source"].AddTrustedCertificate(movableFingerprint, map[string]any{
		"name": movableFingerprint, "autoRegistrationEnabled": false,
	})
	svc := p.start(t)

	body := decode(t, call(t, svc, cutoverRequest(t, service.TrustAnchorCutoverRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", Fingerprint: movableFingerprint,
	})))

	entry, ok := p.tenants["t-target"].TrustedCertificate(movableFingerprint)
	if !ok {
		t.Fatal("the certificate did not move")
	}
	if entry["autoRegistrationEnabled"] != false {
		t.Errorf("the target's copy has autoRegistration %v, want the source's false",
			entry["autoRegistrationEnabled"])
	}
	if body["autoRegistrationEnabled"] != false {
		t.Errorf("the answer reports %v", body["autoRegistrationEnabled"])
	}
	// And it says what that means, because a device arriving with no device user
	// of its own is refused there.
	if answer := fmt.Sprint(body["message"]); !strings.Contains(answer, "refused") {
		t.Errorf("the answer does not say arriving devices are refused: %s", answer)
	}
}

func TestAutoRegistrationCanBeTurnedOnDeliberately(t *testing.T) {
	p := twoTenants(t)
	p.tenants["t-source"].AddTrustedCertificate(movableFingerprint, map[string]any{
		"name": movableFingerprint, "autoRegistrationEnabled": false,
	})
	svc := p.start(t)

	on := true
	body := decode(t, call(t, svc, cutoverRequest(t, service.TrustAnchorCutoverRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", Fingerprint: movableFingerprint,
		AutoRegistrationEnabled: &on,
	})))

	entry, _ := p.tenants["t-target"].TrustedCertificate(movableFingerprint)
	if entry["autoRegistrationEnabled"] != true {
		t.Errorf("the target's copy has autoRegistration %v, want the requested true",
			entry["autoRegistrationEnabled"])
	}
	// Repeated back, because it widens what this CA may do in the target.
	if answer := fmt.Sprint(body["message"]); !strings.Contains(answer, "where it was off") {
		t.Errorf("the answer does not say the setting was changed: %s", answer)
	}
}

func TestAutoRegistrationCanBeTurnedOffDeliberately(t *testing.T) {
	p := twoTenants(t)
	p.tenants["t-source"].AddTrustedCertificate(movableFingerprint,
		map[string]any{"name": movableFingerprint})
	svc := p.start(t)

	off := false
	body := decode(t, call(t, svc, cutoverRequest(t, service.TrustAnchorCutoverRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", Fingerprint: movableFingerprint,
		AutoRegistrationEnabled: &off,
	})))

	entry, _ := p.tenants["t-target"].TrustedCertificate(movableFingerprint)
	if entry["autoRegistrationEnabled"] != false {
		t.Errorf("the target's copy has autoRegistration %v, want the requested false",
			entry["autoRegistrationEnabled"])
	}
	// A deliberate false is not a failure of the move, so the answer must not
	// read as one - but it does have to say what it means.
	if body["autoRegistrationEnabled"] != false {
		t.Errorf("the answer reports %v", body["autoRegistrationEnabled"])
	}
	if answer := fmt.Sprint(body["message"]); strings.Contains(answer, "WARNING") {
		t.Errorf("a setting that was asked for is reported as a warning: %s", answer)
	}
}

// The status is whatever the source said, not a value this service chooses. A
// status it does not recognise is the clearest way to pin that: nothing here
// should be interpreting it.
func TestTheStatusIsCarriedAcrossUnchanged(t *testing.T) {
	p := twoTenants(t)
	p.tenants["t-source"].AddTrustedCertificate(movableFingerprint, map[string]any{
		"name": movableFingerprint, "status": "SOMETHING_ELSE",
	})
	svc := p.start(t)

	if recorder := call(t, svc, cutoverRequest(t, service.TrustAnchorCutoverRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", Fingerprint: movableFingerprint,
	})); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}

	entry, ok := p.tenants["t-target"].TrustedCertificate(movableFingerprint)
	if !ok {
		t.Fatal("the certificate did not move")
	}
	if entry["status"] != "SOMETHING_ELSE" {
		t.Errorf("the target's copy is %v, want the source's own status", entry["status"])
	}
}

// Putting the certificate back is undoing a move, not performing a different
// one, so it restores what the source had rather than what was asked for.
func TestARestoreUsesTheSourcesOwnSettings(t *testing.T) {
	p := twoTenants(t)
	p.tenants["t-source"].AddTrustedCertificate(movableFingerprint, map[string]any{
		"name": movableFingerprint, "autoRegistrationEnabled": false, "status": "ENABLED",
	})
	p.tenants["t-target"].RefuseCertificateCreate()
	svc := p.start(t)

	on := true
	if recorder := call(t, svc, cutoverRequest(t, service.TrustAnchorCutoverRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", Fingerprint: movableFingerprint,
		AutoRegistrationEnabled: &on,
	})); recorder.Code == http.StatusOK {
		t.Fatal("a refused create was reported as a successful cutover")
	}

	entry, ok := p.tenants["t-source"].TrustedCertificate(movableFingerprint)
	if !ok {
		t.Fatal("the certificate was not put back, so the fleet is on neither tenant")
	}
	if entry["autoRegistrationEnabled"] != false {
		t.Errorf("the restored copy has autoRegistration %v: a failed move changed the source",
			entry["autoRegistrationEnabled"])
	}
	if entry["status"] != "ENABLED" {
		t.Errorf("the restored copy is %v, want the source's own status", entry["status"])
	}
}

// A disabled certificate is not offered for moving at all, with the reason on
// the listing where somebody chooses.
func TestADisabledCertificateIsNotOffered(t *testing.T) {
	p := twoTenants(t)
	p.tenants["t-source"].AddTrustedCertificate(movableFingerprint, map[string]any{
		"name": movableFingerprint, "status": "DISABLED",
	})
	svc := p.start(t)

	body := decode(t, call(t, svc,
		request(t, http.MethodGet, "/tenants/t-source/trusted-certificates", nil)))
	certificates, _ := body["certificates"].([]any)
	if len(certificates) != 1 {
		t.Fatalf("listed %d certificates, want 1", len(certificates))
	}

	fields, _ := certificates[0].(map[string]any)
	if fields["movable"] == true {
		t.Error("a disabled certificate was offered as movable")
	}
	if reason := fmt.Sprint(fields["reason"]); !strings.Contains(reason, "Enable it here first") {
		t.Errorf("the reason does not say what to do about it: %s", reason)
	}
}
