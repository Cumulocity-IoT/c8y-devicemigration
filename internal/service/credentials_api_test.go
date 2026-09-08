package service_test

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/service"
)

// A migration that asks for credentials issues them, records the count and not
// the secrets, and offers them for collection.
func TestAMigrationWithCredentialsIssuesThem(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	job := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
		ProvisionCredentials: true,
	})
	if job["status"] != string(service.StatusSucceeded) {
		t.Fatalf("status = %v, error %v", job["status"], job["error"])
	}

	registered := len(p.tenants["t-target"].Registrations())
	if registered == 0 {
		t.Fatal("no devices were registered")
	}
	if got := fmt.Sprint(job["credentialsIssued"]); got != fmt.Sprint(registered) {
		t.Errorf("credentialsIssued is %v, want %d", job["credentialsIssued"], registered)
	}
	if job["credentialsAvailable"] != true {
		t.Errorf("credentialsAvailable is %v, want true before collection", job["credentialsAvailable"])
	}
}

// The record is an ordinary managed object any tenant administrator can read, so
// no password may appear anywhere in it - not in the request it embeds, not in
// the progress lines the engine printed.
func TestNoPasswordReachesTheJobRecord(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	job := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
		ProvisionCredentials: true,
	})

	id := fmt.Sprint(job["id"])
	body := call(t, svc, request(t, http.MethodGet, "/migrations/"+id, nil)).Body.String()
	for _, row := range p.tenants["t-target"].Registrations() {
		if strings.Contains(body, row.Password) {
			t.Fatalf("the password of %s appears in the job response", row.ExternalID)
		}
	}

	// And the stored record, not just the response built from it.
	for _, created := range p.tenants[bootstrapTenant].Created() {
		for _, row := range p.tenants["t-target"].Registrations() {
			if strings.Contains(string(created), row.Password) {
				t.Fatalf("the password of %s was written into a managed object", row.ExternalID)
			}
		}
	}
}

// Collected once: the file arrives as CSV matching what was registered, and a
// second call is Gone rather than the same body again.
func TestCredentialsAreHandedOverExactlyOnce(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	job := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
		ProvisionCredentials: true,
	})
	id := fmt.Sprint(job["id"])

	first := call(t, svc, request(t, http.MethodGet, "/migrations/"+id+"/credentials", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", first.Code, first.Body)
	}
	if got := first.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/csv") {
		t.Errorf("Content-Type is %q, want text/csv", got)
	}
	// A list of working device passwords must not be cached by a proxy or a
	// browser after the one read that was allowed.
	if got := first.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control is %q, want no-store", got)
	}
	if got := first.Header().Get("Content-Disposition"); !strings.Contains(got, "attachment") {
		t.Errorf("Content-Disposition is %q, want an attachment", got)
	}

	rows, err := csv.NewReader(strings.NewReader(first.Body.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	sent := map[string]string{}
	for _, row := range p.tenants["t-target"].Registrations() {
		sent[row.ExternalID] = row.Password
	}
	if got, want := len(rows)-1, len(sent); got != want {
		t.Fatalf("the file holds %d credentials for %d registered devices", got, want)
	}
	for _, row := range rows[1:] {
		if sent[row[0]] != row[3] {
			t.Errorf("%s was registered with one password and issued another", row[0])
		}
		if want := "device_" + row[0]; row[2] != want {
			t.Errorf("username for %s is %q, want %q", row[0], row[2], want)
		}
	}

	second := call(t, svc, request(t, http.MethodGet, "/migrations/"+id+"/credentials", nil))
	if second.Code != http.StatusGone {
		t.Fatalf("a second collection returned %d, want 410", second.Code)
	}
	if body := second.Body.String(); !strings.Contains(body, "rotates") {
		t.Errorf("the 410 does not say how to recover:\n%s", body)
	}

	// And the job now says there is nothing to collect, so a user interface can
	// stop offering the download.
	after := decode(t, call(t, svc, request(t, http.MethodGet, "/migrations/"+id, nil)))
	if after["credentialsAvailable"] == true {
		t.Error("credentialsAvailable is still true after collection")
	}
}

// A migration that did not ask for credentials has none to collect, and the
// answer is the same 410 rather than something that hints at what might exist.
func TestAMigrationWithoutCredentialsHasNoneToCollect(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	job := migrate1(t, svc, service.MigrationRequest{
		SourceTenant: "t-source", TargetTenant: "t-target", RootID: "1",
	})
	id := fmt.Sprint(job["id"])

	recorder := call(t, svc, request(t, http.MethodGet, "/migrations/"+id+"/credentials", nil))
	if recorder.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410", recorder.Code)
	}
	if len(p.tenants["t-target"].Registrations()) != 0 {
		t.Error("devices were registered without provisionCredentials")
	}
}

// An unknown job is a 404, not a 410: "there is no such migration" and "its
// credentials are gone" are different answers, and confusing them would leave a
// caller unable to tell a typo from a race with a colleague.
func TestCredentialsForAnUnknownJobAreNotFound(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc,
		request(t, http.MethodGet, "/migrations/20260101T000000Z_nope/credentials", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}

// A remote target is reached with a device access token, whose roles are the
// device ones - it cannot register anything. Refused when the migration is
// accepted, not discovered half way through a run.
func TestCredentialsAreRefusedForARemoteTarget(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	recorder := call(t, svc, request(t, http.MethodPost, "/migrations", service.MigrationRequest{
		SourceTenant: "t-source", RootID: "1",
		RemoteTargetID:       "anything",
		ProvisionCredentials: true,
	}))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "device access token") {
		t.Errorf("the refusal does not explain why:\n%s", body)
	}
}
