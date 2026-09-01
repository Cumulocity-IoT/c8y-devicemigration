package service

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCreatingARemoteTargetStoresItWithMaterial(t *testing.T) {
	store := newRemoteTargetStore()
	rt, err := store.create("t22222.example.com", "t22222", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	if rt.ID == "" || rt.Subject == "" {
		t.Fatalf("id/subject not set: %+v", rt)
	}
	if rt.caPEM() == "" || rt.material.LeafPEM == "" || rt.material.KeyPEM == "" {
		t.Fatal("certificate material was not generated")
	}
	if got, ok := store.get(rt.ID); !ok || got != rt {
		t.Fatal("the target cannot be fetched by its id")
	}
	if rt.ExpiresAt.Sub(rt.CreatedAt) != certValidity {
		t.Errorf("validity window is %s, want %s", rt.ExpiresAt.Sub(rt.CreatedAt), certValidity)
	}
}

func TestRemoteTargetIDsAreUniqueAndUnguessable(t *testing.T) {
	store := newRemoteTargetStore()
	seen := map[string]bool{}
	for range 50 {
		rt, _ := store.create("h", "t", time.Now())
		if seen[rt.ID] {
			t.Fatal("a remote-target id repeated")
		}
		if len(rt.ID) < 24 {
			t.Fatalf("id %q is too short to be unguessable", rt.ID)
		}
		seen[rt.ID] = true
	}
}

func TestRemovingARemoteTargetForgetsIt(t *testing.T) {
	store := newRemoteTargetStore()
	rt, _ := store.create("h", "t", time.Now())

	if !store.remove(rt.ID) {
		t.Fatal("remove said there was nothing there")
	}
	if _, ok := store.get(rt.ID); ok {
		t.Fatal("the target is still there after removal")
	}
	if store.remove(rt.ID) {
		t.Fatal("removing a gone target reported success")
	}
}

// Resolution is the one place that tells a subscribed target from a remote one.
// Only the remote branch is exercised here, because the subscription branch
// needs a live platform; an endpoint test covers that side.
func TestTargetClientResolvesARemoteTarget(t *testing.T) {
	svc := &Service{remoteTargets: newRemoteTargetStore(), insecureSkipVerify: true}

	// A remote id nobody established is a clear 400, not a confusing miss.
	if _, err := svc.targetClient("no-such-remote", ""); err == nil {
		t.Error("an unknown remote target was accepted")
	} else if !strings.Contains(err.Error(), "not known") {
		t.Errorf("unhelpful error for an unknown remote target: %v", err)
	}

	// An established remote target resolves to a client whose session carries the
	// certificate, not a password. The host is a dead port so the client's token
	// prime fails fast; construction still returns a usable session to inspect.
	rt, _ := svc.remoteTargets.create("127.0.0.1:1", "t-remote", time.Now())
	client, err := svc.targetClient(rt.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if client.session.Certificate == "" || client.session.Password != "" {
		t.Errorf("remote client is not certificate-authenticated: %+v", client.session)
	}
	if client.session.Tenant != "t-remote" {
		t.Errorf("remote client points at %q, want t-remote", client.session.Tenant)
	}
}

// The structural guarantee behind the whole design: nothing on the migration
// request can carry a secret, because there is no field for one. If somebody
// adds a credential field here, this fails and the record/response/public schema
// leak is caught before it ships.
func TestTheMigrationRequestHasNoCredentialField(t *testing.T) {
	for _, f := range reflect.VisibleFields(reflect.TypeOf(MigrationRequest{})) {
		name := strings.ToLower(f.Name)
		for _, banned := range []string{"password", "secret", "token", "certificate", "key", "credential"} {
			if strings.Contains(name, banned) {
				t.Errorf("MigrationRequest.%s looks like a secret and would land on the job record", f.Name)
			}
		}
	}
}

// Expiry used to be a note on a listing that nothing acted on: an expired target
// would have been handed to the platform and refused with a 401 saying nothing
// about why. Now that an undo depends on the certificate still being alive, the
// deadline decides whether that is possible, so it is enforced where the client
// is built - and said in words a person can act on.
func TestAnExpiredRemoteTargetIsRefusedWithItsDeadline(t *testing.T) {
	store := newRemoteTargetStore()
	rt, err := store.create("example.test", "t-remote", time.Now().Add(-certValidity-time.Hour))
	if err != nil {
		t.Fatalf("creating: %v", err)
	}
	if !rt.expired(time.Now()) {
		t.Fatal("a target minted longer ago than the validity window is not expired")
	}

	svc := &Service{remoteTargets: store, insecureSkipVerify: true}
	_, err = svc.targetClient(rt.ID, "t-remote")
	if err == nil {
		t.Fatal("an expired certificate was accepted")
	}
	for _, want := range []string{"expired", "t-remote"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q", err, want)
		}
	}
}

func TestALiveRemoteTargetStillBuildsAClient(t *testing.T) {
	store := newRemoteTargetStore()
	rt, err := store.create("example.test", "t-remote", time.Now())
	if err != nil {
		t.Fatalf("creating: %v", err)
	}
	if rt.expired(time.Now()) {
		t.Fatal("a freshly minted target is already expired")
	}

	svc := &Service{remoteTargets: store, insecureSkipVerify: true}
	if _, err := svc.targetClient(rt.ID, "t-remote"); err != nil {
		t.Errorf("a live remote target was refused: %v", err)
	}
}
