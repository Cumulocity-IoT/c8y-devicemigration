package service

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/registration"
)

func someCredentials() []registration.Credential {
	return []registration.Credential{
		{ExternalID: "SN-1", Tenant: "t-target", Username: "device_SN-1", Password: "Tk7#pQ2$mR9!wZ4"},
	}
}

// Held for one read and for a bounded time. The second half is what stops a
// fleet's passwords being resident for the life of a container.
func TestUncollectedCredentialsExpire(t *testing.T) {
	store := newCredentialStore()
	if err := store.put("job-1", someCredentials()); err != nil {
		t.Fatal(err)
	}
	if !store.held("job-1") {
		t.Fatal("credentials were not held")
	}

	// Reach in rather than sleep an hour: what is being tested is the rule, not
	// the clock.
	entry := store.files["job-1"]
	entry.expiresAt = time.Now().Add(-time.Minute)
	store.files["job-1"] = entry

	if store.held("job-1") {
		t.Error("an expired file is still offered for collection")
	}
	if _, err := store.take("job-1"); err == nil {
		t.Error("an expired file was handed over")
	}
	// And taking it forgot it, so the bytes do not outlive the refusal.
	if _, present := store.files["job-1"]; present {
		t.Error("an expired file was left in the store after being refused")
	}
}

// take and held refuse an expired file the moment they are asked; the sweep is
// what stops the bytes sitting there until somebody does ask.
func TestTheSweepDropsWhatHasExpired(t *testing.T) {
	store := newCredentialStore()
	if err := store.put("job-1", someCredentials()); err != nil {
		t.Fatal(err)
	}
	if err := store.put("job-2", someCredentials()); err != nil {
		t.Fatal(err)
	}

	if dropped := store.sweep(time.Now()); dropped != 0 {
		t.Errorf("swept %d files that had not expired", dropped)
	}
	if dropped := store.sweep(time.Now().Add(credentialTTL + time.Minute)); dropped != 2 {
		t.Errorf("swept %d files, want 2", dropped)
	}
	if len(store.files) != 0 {
		t.Errorf("%d files survived the sweep", len(store.files))
	}
}

// The one-shot handover, the download offer and the expiry are all this
// process's own memory, so they are only coherent with a single replica. The
// manifest says so; this is what notices if that ever changes.
func TestTheManifestKeepsThisToOneReplica(t *testing.T) {
	raw, err := os.ReadFile("../../docker/cumulocity.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Replicas int    `json:"replicas"`
		Scale    string `json:"scale"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}

	// With more than one container, each would hold its own credentials:
	// credentialsAvailable would depend on which replica answered, and a
	// collection could be refused by one while another still held the file.
	if manifest.Replicas != 1 {
		t.Errorf("replicas = %d: device credentials are held in one process's memory, "+
			"so a second replica would hand them over inconsistently", manifest.Replicas)
	}
	if manifest.Scale != "NONE" {
		t.Errorf("scale = %q, want NONE for the same reason", manifest.Scale)
	}
}
