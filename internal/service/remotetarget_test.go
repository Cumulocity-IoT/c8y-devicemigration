package service_test

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"strings"
	"testing"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/service"
)

func TestEstablishingARemoteTargetReturnsACAToRegister(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodPost, "/remote-targets",
		service.RemoteTargetRequest{Host: "t99999.example.com", Tenant: "t99999"}))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}

	body := decode(t, recorder)
	for _, field := range []string{"id", "host", "tenant", "subject", "caCertificate"} {
		if body[field] == nil || body[field] == "" {
			t.Errorf("the response has no %s: %v", field, body)
		}
	}
	ca, _ := body["caCertificate"].(string)
	block, _ := pem.Decode([]byte(ca))
	if block == nil {
		t.Fatalf("caCertificate is not PEM: %q", ca)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !cert.IsCA {
		t.Error("the returned certificate is not a CA, so it cannot be a trust anchor")
	}
}

// The whole point of the handle design: the secret never leaves the service.
func TestARemoteTargetNeverExposesItsPrivateMaterial(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	created := call(t, svc, request(t, http.MethodPost, "/remote-targets",
		service.RemoteTargetRequest{Host: "h", Tenant: "t99999"}))
	if strings.Contains(created.Body.String(), "PRIVATE KEY") {
		t.Fatal("the create response leaked a private key")
	}

	listed := call(t, svc, request(t, http.MethodGet, "/remote-targets", nil))
	body := listed.Body.String()
	if strings.Contains(body, "PRIVATE KEY") || strings.Contains(body, "caCertificate") ||
		strings.Contains(body, "BEGIN CERTIFICATE") {
		t.Fatalf("the listing leaked certificate material: %s", body)
	}
}

func TestDiscardingARemoteTarget(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	created := decode(t, call(t, svc, request(t, http.MethodPost, "/remote-targets",
		service.RemoteTargetRequest{Host: "h", Tenant: "t99999"})))
	id, _ := created["id"].(string)

	if code := call(t, svc, request(t, http.MethodDelete, "/remote-targets/"+id, nil)).Code; code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", code)
	}
	if code := call(t, svc, request(t, http.MethodDelete, "/remote-targets/"+id, nil)).Code; code != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", code)
	}
}

// Exactly one of a subscribed tenant or a remote target - not both, not neither.
func TestPreflightWantsExactlyOneKindOfTarget(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	both := call(t, svc, request(t, http.MethodPost, "/preflight", map[string]string{
		"sourceTenant": "t-source", "targetTenant": "t-target", "remoteTargetId": "x"}))
	if both.Code != http.StatusBadRequest || !strings.Contains(both.Body.String(), "exactly one") {
		t.Errorf("both target kinds: %d %s", both.Code, both.Body)
	}

	neither := call(t, svc, request(t, http.MethodPost, "/preflight", map[string]string{
		"sourceTenant": "t-source"}))
	if neither.Code != http.StatusBadRequest || !strings.Contains(neither.Body.String(), "exactly one") {
		t.Errorf("neither target kind: %d %s", neither.Code, neither.Body)
	}
}

// A remote-target migration cannot be undone from here - the certificate is
// gone - so the refusal has to say what to do instead, not fail obscurely.
func TestRevertingARemoteTargetWhoseCertificateIsGoneExplainsItself(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	// A handle the store has never heard of - which is what a restart leaves
	// behind, because the keys live in memory and die with the container.
	job := service.Job{
		ID: "20260101T000000Z_remote", Kind: service.KindMigration, Status: service.StatusSucceeded,
		RunID:   "20260101T000000Z_run",
		Request: &service.MigrationRequest{SourceTenant: "t-source", TargetTenant: "t99999", RootID: "1", RemoteTargetID: "gone"},
	}
	if _, err := svc.CreateRecord(t.Context(), &job); err != nil {
		t.Fatal(err)
	}

	recorder := call(t, svc, request(t, http.MethodPost, "/reverts",
		service.RevertRequest{JobID: job.ID}))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), "establish it again") {
		t.Errorf("the refusal does not say what to do about it: %s", recorder.Body)
	}
}

// The point of the feature: while the certificate lives, the undo is a normal
// undo. It has to get past validation on the strength of that certificate, the
// way a migration to the same target does.
func TestRevertingARemoteTargetGetsPastValidationWhileTheCertificateLives(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	// A dead host, so anything that does reach out fails fast; what matters is
	// that the refusal is not "this cannot be undone from here".
	established := decode(t, call(t, svc, request(t, http.MethodPost, "/remote-targets",
		service.RemoteTargetRequest{Host: "127.0.0.1:1", Tenant: "t-remote"})))
	id, _ := established["id"].(string)

	job := service.Job{
		ID: "20260101T000000Z_live", Kind: service.KindMigration, Status: service.StatusSucceeded,
		RunID:   "20260101T000000Z_run",
		Request: &service.MigrationRequest{SourceTenant: "t-source", TargetTenant: "t-remote", RootID: "1", RemoteTargetID: id},
	}
	if _, err := svc.CreateRecord(t.Context(), &job); err != nil {
		t.Fatal(err)
	}

	recorder := call(t, svc, request(t, http.MethodPost, "/reverts",
		service.RevertRequest{JobID: job.ID}))

	body := recorder.Body.String()
	for _, refusal := range []string{"cannot undo", "command line", "no longer holds", "is not known"} {
		if strings.Contains(body, refusal) {
			t.Fatalf("a live remote target was refused as un-undoable: %s", body)
		}
	}
}

// Enqueue must not fill in targetTenant from the remote target before the
// pre-flight, or the pre-flight sees both a tenant and a remote id and refuses
// with "exactly one". A migration to a remote target must get past that
// validation - it may fail later reaching the target, but not on this.
func TestStartingAMigrationToARemoteTargetGetsPastValidation(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	// A dead host, so the pre-flight's certificate probe fails fast rather than
	// hanging - what matters is that it is not the validation that fails.
	established := decode(t, call(t, svc, request(t, http.MethodPost, "/remote-targets",
		service.RemoteTargetRequest{Host: "127.0.0.1:1", Tenant: "t-remote"})))
	id, _ := established["id"].(string)

	recorder := call(t, svc, request(t, http.MethodPost, "/migrations", service.MigrationRequest{
		SourceTenant: "t-source", RemoteTargetID: id, RootID: "1",
	}))

	if strings.Contains(recorder.Body.String(), "exactly one") {
		t.Fatalf("a remote-target migration was refused by target validation: %s", recorder.Body)
	}
	// It should be refused for a real reason - it cannot reach the dead host -
	// not accepted, since nothing there answers.
	if recorder.Code == http.StatusAccepted {
		t.Fatalf("a migration to an unreachable remote target was accepted: %s", recorder.Body)
	}
}
