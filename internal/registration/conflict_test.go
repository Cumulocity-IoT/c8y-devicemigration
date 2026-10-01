package registration_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/registration"
)

// An external identity the target already holds is the one case where
// registering does not create a device but replaces the credential of a device
// that is already there. The run has to refuse before it generates anything.

func TestRegistrationIsRefusedWhenTheTargetAlreadyHoldsAnIdentity(t *testing.T) {
	server, registrar, _ := exportThenRegister(t, registration.Options{})

	plan, err := registrar.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Records) == 0 {
		t.Fatal("the fixture registers no devices")
	}
	taken := plan.Records[0]
	server.HoldIdentity(taken.ExternalType, taken.ExternalID, "9999")

	credentials, err := registrar.Run(context.Background())

	var conflict *registration.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error is %v, want a *ConflictError", err)
	}
	if len(credentials) != 0 {
		t.Errorf("%d credentials were issued by a refused registration", len(credentials))
	}
	if len(conflict.Conflicts) != 1 {
		t.Fatalf("named %d conflicts, want 1: %+v", len(conflict.Conflicts), conflict.Conflicts)
	}
	if got := conflict.Conflicts[0].ExternalID; got != taken.ExternalID {
		t.Errorf("named %s, want %s", got, taken.ExternalID)
	}
	if got := conflict.Conflicts[0].TargetID; got != "9999" {
		t.Errorf("named target object %q, want the one already holding the identity", got)
	}
}

// The whole run, not the conflicting device alone. Registering the rest would
// leave a half-provisioned fleet whose credentials file silently covers fewer
// devices than the migration does - and there is no flag to ask for that.
func TestOneConflictRefusesEveryDevice(t *testing.T) {
	server, registrar, _ := exportThenRegister(t, registration.Options{})

	plan, err := registrar.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Records) < 2 {
		t.Skipf("fixture has %d registrable devices, need at least 2", len(plan.Records))
	}
	server.HoldIdentity(plan.Records[0].ExternalType, plan.Records[0].ExternalID, "9999")

	if _, err := registrar.Run(context.Background()); err == nil {
		t.Fatal("the registration was not refused")
	}
	if users := server.DeviceUsers(); len(users) != 0 {
		t.Errorf("a refused registration created device users: %v", users)
	}
}

// The refusal names every device, not just the first one found: an operator
// settling duplicates one round trip at a time would run this as many times as
// there are conflicts.
func TestTheRefusalNamesEveryConflictingDevice(t *testing.T) {
	server, registrar, _ := exportThenRegister(t, registration.Options{})

	plan, err := registrar.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Records) < 2 {
		t.Skipf("fixture has %d registrable devices, need at least 2", len(plan.Records))
	}
	for index, record := range plan.Records {
		server.HoldIdentity(record.ExternalType, record.ExternalID, "999"+string(rune('0'+index)))
	}

	checked, err := registrar.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(checked.Conflicts) != len(plan.Records) {
		t.Fatalf("found %d conflicts, want all %d", len(checked.Conflicts), len(plan.Records))
	}
	for _, conflict := range checked.Conflicts {
		if conflict.SourceID == "" {
			t.Errorf("a conflict names no source object: %+v", conflict)
		}
	}
}

// Check is what both the run and --dry-run call, so the two cannot disagree
// about whether a registration is possible.
func TestCheckAgreesWithWhatTheRunRefuses(t *testing.T) {
	server, registrar, _ := exportThenRegister(t, registration.Options{})

	plan, err := registrar.Plan()
	if err != nil {
		t.Fatal(err)
	}
	server.HoldIdentity(plan.Records[0].ExternalType, plan.Records[0].ExternalID, "9999")

	checked, err := registrar.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := registrar.Run(context.Background())

	var conflict *registration.ConflictError
	if !errors.As(runErr, &conflict) {
		t.Fatalf("the run failed with %v, want a *ConflictError", runErr)
	}
	if len(checked.Conflicts) != len(conflict.Conflicts) {
		t.Errorf("Check found %d conflicts, the run refused over %d",
			len(checked.Conflicts), len(conflict.Conflicts))
	}
}

// A lookup that did not answer is not evidence that the target holds nothing.
// Failing open here would register exactly the device the check exists for.
func TestALookupFailureRefusesRatherThanRegisters(t *testing.T) {
	server, registrar, _ := exportThenRegister(t, registration.Options{})
	server.FailIdentityLookups()

	credentials, err := registrar.Run(context.Background())
	if err == nil {
		t.Fatal("a registration went ahead with an unanswered identity lookup")
	}
	if len(credentials) != 0 {
		t.Errorf("%d credentials were issued anyway", len(credentials))
	}
	if users := server.DeviceUsers(); len(users) != 0 {
		t.Errorf("device users were created anyway: %v", users)
	}
}

// Nothing in the refusal invites a retry that would succeed by force: there is
// no flag, so the message must not imply one.
func TestTheRefusalOffersNoOverride(t *testing.T) {
	err := &registration.ConflictError{Conflicts: []registration.Conflict{{ExternalID: "SN-1"}}}
	for _, forbidden := range []string{"--force", "--allow", "override", "anyway"} {
		if strings.Contains(strings.ToLower(err.Error()), forbidden) {
			t.Errorf("the refusal mentions %q: %s", forbidden, err.Error())
		}
	}
}
