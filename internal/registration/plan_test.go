package registration_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/registration"
)

// writeExport builds an export directory by hand.
//
// The fixture hierarchy happens to mark every object as a device and gives every
// identified device a c8y_Serial, so the branches that matter most in a real
// tenant - a group, and a device with several identities and no c8y_Serial - are
// unreachable through it. These are cheap to construct directly and the artefact
// layout is a documented contract, so there is nothing to drift from.
func writeExport(t *testing.T, objects map[string]string, identities map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for id, body := range objects {
		path := filepath.Join(dir, "exported_device_"+id+".json")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for id, body := range identities {
		path := filepath.Join(dir, id+"_identities.json")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func planOf(t *testing.T, dir string, opts registration.Options) *registration.Plan {
	t.Helper()
	opts.InputDirectory = dir
	var stderr bytes.Buffer
	plan, err := registration.New(nil, &stderr, opts).Plan()
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// c8y_Serial wins over any other type, because it is what an agent presents when
// it reconnects - thin-edge uses it for device.id and certificate
// auto-registration looks it up.
func TestPreferredIdentityWinsOverOthers(t *testing.T) {
	dir := writeExport(t,
		map[string]string{"2": `{"name":"Line A","type":"c8y_Pump","c8y_IsDevice":{}}`},
		map[string]string{"2": `[{"type":"c8y_ExternalId","externalId":"line-a@example"},
		                         {"type":"c8y_Serial","externalId":"LINE-A-SN"}]`},
	)
	plan := planOf(t, dir, registration.Options{})

	if len(plan.Records) != 1 {
		t.Fatalf("planned %d records, want 1", len(plan.Records))
	}
	if plan.Records[0].ExternalID != "LINE-A-SN" {
		t.Fatalf("registered under %s (%s), want the c8y_Serial identity",
			plan.Records[0].ExternalID, plan.Records[0].ExternalType)
	}
}

func TestIdentityTypeCanBeOverridden(t *testing.T) {
	dir := writeExport(t,
		map[string]string{"2": `{"name":"Line A","c8y_IsDevice":{}}`},
		map[string]string{"2": `[{"type":"c8y_ExternalId","externalId":"line-a@example"},
		                         {"type":"c8y_Serial","externalId":"LINE-A-SN"}]`},
	)
	plan := planOf(t, dir, registration.Options{IdentityType: "c8y_ExternalId"})

	if plan.Records[0].ExternalID != "line-a@example" {
		t.Fatalf("registered under %s, want the overridden type", plan.Records[0].ExternalID)
	}
}

// One identity of an unexpected type is unambiguous, so it is used rather than
// refused - refusing would deny a credential to a device nobody had a choice
// about.
func TestASingleIdentityIsUsedWhateverItsType(t *testing.T) {
	dir := writeExport(t,
		map[string]string{"2": `{"name":"Line A","c8y_IsDevice":{}}`},
		map[string]string{"2": `[{"type":"c8y_ExternalId","externalId":"line-a@example"}]`},
	)
	plan := planOf(t, dir, registration.Options{})

	if len(plan.Records) != 1 || plan.Records[0].ExternalID != "line-a@example" {
		t.Fatalf("plan is %+v, want the single identity used", plan.Records)
	}
}

// Several identities and none of the preferred type: refused rather than guessed
// at. The artefact's order is the platform's, not a preference, and binding a
// device user to an identity nobody chose would only be discovered when the
// device reconnected as something else.
func TestAmbiguousIdentityIsRefusedAndExplained(t *testing.T) {
	dir := writeExport(t,
		map[string]string{"2": `{"name":"Line A","c8y_IsDevice":{}}`},
		map[string]string{"2": `[{"type":"c8y_ExternalId","externalId":"line-a@example"},
		                         {"type":"c8y_Imei","externalId":"350077521234567"}]`},
	)
	plan := planOf(t, dir, registration.Options{})

	if len(plan.Records) != 0 {
		t.Fatalf("planned %+v, want nothing", plan.Records)
	}
	if len(plan.Skipped) != 1 {
		t.Fatalf("skipped %d objects, want 1", len(plan.Skipped))
	}
	reason := plan.Skipped[0].Reason
	// The reason has to name both types and the way out, or the operator can
	// only see that a device was skipped, not what to do about it.
	for _, want := range []string{"c8y_ExternalId", "c8y_Imei", "c8y_Serial", "--identityType"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason does not mention %s: %s", want, reason)
		}
	}
}

// A group has no device user to create. Registering one would invent a credential
// for something that cannot authenticate, and a revert's withDeviceUser would not
// remove it - the flag only applies where the platform considers the object a
// device.
func TestGroupsAreNotRegistered(t *testing.T) {
	dir := writeExport(t,
		map[string]string{
			"1": `{"name":"Plant Berlin","type":"c8y_DeviceGroup","c8y_IsDeviceGroup":{}}`,
			"2": `{"name":"Line A","c8y_IsDevice":{}}`,
		},
		map[string]string{
			"1": `[{"type":"c8y_Serial","externalId":"PLANT-BERLIN-01"}]`,
			"2": `[{"type":"c8y_Serial","externalId":"LINE-A-SN"}]`,
		},
	)
	plan := planOf(t, dir, registration.Options{})

	if len(plan.Records) != 1 || plan.Records[0].ExternalID != "LINE-A-SN" {
		t.Fatalf("plan is %+v, want only the device", plan.Records)
	}
	if len(plan.Skipped) != 1 || plan.Skipped[0].SourceID != "1" {
		t.Fatalf("skipped %+v, want the group", plan.Skipped)
	}
	if !strings.Contains(plan.Skipped[0].Reason, "not a device") {
		t.Errorf("group was skipped for the wrong reason: %s", plan.Skipped[0].Reason)
	}
}

// The export omits the identities file entirely rather than writing an empty
// list, so its absence is an answer and not a failure.
func TestMissingIdentitiesFileIsSkippedNotAnError(t *testing.T) {
	dir := writeExport(t,
		map[string]string{"3": `{"name":"Line B","c8y_IsDevice":{}}`},
		nil,
	)
	plan := planOf(t, dir, registration.Options{})

	if len(plan.Skipped) != 1 {
		t.Fatalf("skipped %+v, want one entry", plan.Skipped)
	}
	if !strings.Contains(plan.Skipped[0].Reason, "no external identity") {
		t.Errorf("reason is %q", plan.Skipped[0].Reason)
	}
}

// The name and type the registration file carries come from the source object,
// and the writer quotes them - a device called `Pump "West"` is ordinary.
func TestPlanCarriesSourceNameAndType(t *testing.T) {
	dir := writeExport(t,
		map[string]string{"4": `{"name":"Pump \"West\"","type":"c8y_Pump","c8y_IsDevice":{}}`},
		map[string]string{"4": `[{"type":"c8y_Serial","externalId":"PUMP WEST 001"}]`},
	)
	plan := planOf(t, dir, registration.Options{})

	if got := plan.Records[0].Name; got != `Pump "West"` {
		t.Errorf("name is %q", got)
	}
	if got := plan.Records[0].Type; got != "c8y_Pump" {
		t.Errorf("type is %q", got)
	}
}
