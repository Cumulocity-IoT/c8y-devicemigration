package importer_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/c8yclient"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/importer"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/registration"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/testfixture"
)

// A device authenticates with ROLE_DEVICE, and that role carries writes only
// together with source-ownership - so a migrated device is read-only on its own
// twin until something owns it. The stamp is what makes any route that issues
// credentials *after* the import work at all.

// stampedExport builds an export by hand and imports it with the stamp on.
//
// By hand because the shared fixture marks every object as a device and gives
// every identified one a c8y_Serial, so the cases that decide this - a group, a
// device with no identity, a device with several and none preferred - are
// unreachable through it.
func stampedExport(t *testing.T, objects, identities map[string]string, opts importer.Options) (*testfixture.Server, string) {
	t.Helper()

	dir := t.TempDir()
	for id, body := range objects {
		if err := os.WriteFile(filepath.Join(dir, "exported_device_"+id+".json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for id, body := range identities {
		if err := os.WriteFile(filepath.Join(dir, id+"_identities.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	server := testfixture.New(t, "hierarchy.json")
	work := t.TempDir()
	s := server.ReadSession(t, work, "fixture.json")
	client, err := c8yclient.New(s, "")
	if err != nil {
		t.Fatal(err)
	}

	opts.InputDirectory = dir
	opts.MappingFile = filepath.Join(work, "id_mapping.json")
	opts.AllowSameTenant = true

	var stderr bytes.Buffer
	if err := importer.New(client, &stderr, opts).Run(context.Background()); err != nil {
		t.Fatalf("import failed: %v\n%s", err, stderr.String())
	}
	return server, stderr.String()
}

// ownerOf finds the created object with the given name and returns its owner.
func ownerOf(t *testing.T, server *testfixture.Server, name string) string {
	t.Helper()
	for _, body := range server.Created() {
		if gjson.GetBytes(body, "name").String() == name {
			return gjson.GetBytes(body, "owner").String()
		}
	}
	t.Fatalf("no created object called %q", name)
	return ""
}

func TestStampNamesTheDeviceUserOfThePreferredIdentity(t *testing.T) {
	server, stderr := stampedExport(t,
		map[string]string{"2": `{"name":"Line A","type":"c8y_Pump","c8y_IsDevice":{}}`},
		map[string]string{"2": `[{"type":"c8y_ExternalId","externalId":"line-a@example"},
		                         {"type":"c8y_Serial","externalId":"LINE-A-SN"}]`},
		importer.Options{StampDeviceOwner: true},
	)

	if got, want := ownerOf(t, server, "Line A"), "device_LINE-A-SN"; got != want {
		t.Errorf("owner is %q, want %q", got, want)
	}
	if !bytes.Contains([]byte(stderr), []byte("owned by the device user")) {
		t.Errorf("the summary does not mention the stamp:\n%s", stderr)
	}
}

// The source tenant's owner must never travel - it names a user of a different
// tenant. It is on StripServerManaged's list, and the stamp is applied after the
// strip so the two cannot fight.
func TestStampReplacesTheSourceOwnerRatherThanKeepingIt(t *testing.T) {
	server, _ := stampedExport(t,
		map[string]string{"2": `{"name":"Line A","owner":"someone@source.example","c8y_IsDevice":{}}`},
		map[string]string{"2": `[{"type":"c8y_Serial","externalId":"LINE-A-SN"}]`},
		importer.Options{StampDeviceOwner: true},
	)

	if got := ownerOf(t, server, "Line A"); got != "device_LINE-A-SN" {
		t.Errorf("owner is %q, want the device user rather than the source's owner", got)
	}
}

// A group has no device user. Naming one would invent an owner nothing will ever
// be, and the platform would happily accept it.
func TestStampLeavesGroupsAlone(t *testing.T) {
	server, _ := stampedExport(t,
		map[string]string{
			"1": `{"name":"Plant Berlin","type":"c8y_DeviceGroup","c8y_IsDeviceGroup":{}}`,
			"2": `{"name":"Line A","c8y_IsDevice":{}}`,
		},
		map[string]string{
			"1": `[{"type":"c8y_Serial","externalId":"PLANT-BERLIN-01"}]`,
			"2": `[{"type":"c8y_Serial","externalId":"LINE-A-SN"}]`,
		},
		importer.Options{StampDeviceOwner: true},
	)

	if got := ownerOf(t, server, "Plant Berlin"); got == "device_PLANT-BERLIN-01" {
		t.Error("a group was given a device user as its owner")
	}
	if got := ownerOf(t, server, "Line A"); got != "device_LINE-A-SN" {
		t.Errorf("the device's owner is %q", got)
	}
}

func TestStampSkipsWhatItCannotDecide(t *testing.T) {
	cases := map[string]struct {
		object     string
		identities string
	}{
		"no identity at all": {`{"name":"Line B","c8y_IsDevice":{}}`, ""},
		"several identities and none preferred": {
			`{"name":"Line B","c8y_IsDevice":{}}`,
			`[{"type":"c8y_ExternalId","externalId":"a"},{"type":"c8y_Imei","externalId":"b"}]`,
		},
	}
	for name, tc := range cases {
		identities := map[string]string{}
		if tc.identities != "" {
			identities["3"] = tc.identities
		}
		server, _ := stampedExport(t,
			map[string]string{"3": tc.object}, identities,
			importer.Options{StampDeviceOwner: true},
		)
		if got := ownerOf(t, server, "Line B"); got != "" {
			t.Errorf("%s: owner is %q, want none - guessing would name an owner the device never becomes", name, got)
		}
	}
}

// One identity of an unexpected type is unambiguous, so it is used.
func TestStampUsesASingleIdentityWhateverItsType(t *testing.T) {
	server, _ := stampedExport(t,
		map[string]string{"2": `{"name":"Line A","c8y_IsDevice":{}}`},
		map[string]string{"2": `[{"type":"c8y_ExternalId","externalId":"line-a@example"}]`},
		importer.Options{StampDeviceOwner: true},
	)
	if got, want := ownerOf(t, server, "Line A"), "device_line-a@example"; got != want {
		t.Errorf("owner is %q, want %q", got, want)
	}
}

func TestStampCanChooseTheIdentityType(t *testing.T) {
	server, _ := stampedExport(t,
		map[string]string{"2": `{"name":"Line A","c8y_IsDevice":{}}`},
		map[string]string{"2": `[{"type":"c8y_ExternalId","externalId":"line-a@example"},
		                         {"type":"c8y_Serial","externalId":"LINE-A-SN"}]`},
		importer.Options{StampDeviceOwner: true, IdentityType: "c8y_ExternalId"},
	)
	if got, want := ownerOf(t, server, "Line A"), "device_line-a@example"; got != want {
		t.Errorf("owner is %q, want %q", got, want)
	}
}

// Off by default: an ordinary import must keep creating objects owned by whoever
// ran it, which is what it always did.
func TestWithoutTheFlagNothingIsStamped(t *testing.T) {
	server, stderr := stampedExport(t,
		map[string]string{"2": `{"name":"Line A","c8y_IsDevice":{}}`},
		map[string]string{"2": `[{"type":"c8y_Serial","externalId":"LINE-A-SN"}]`},
		importer.Options{},
	)
	if got := ownerOf(t, server, "Line A"); got != "" {
		t.Errorf("owner is %q without the flag", got)
	}
	if bytes.Contains([]byte(stderr), []byte("owned by the device user")) {
		t.Errorf("an unstamped import claimed to stamp:\n%s", stderr)
	}
}

// The rule that picks the identity is written twice - here and in the
// registration phase - because the importer cannot depend on the package that
// provisions credentials. This is what stops the two drifting: the owner the
// import stamps has to be the user the registration phase would create, or a
// device arrives owning an object nobody will ever be.
func TestTheStampAgreesWithWhatRegistrationWouldCreate(t *testing.T) {
	cases := map[string]string{
		"prefers c8y_Serial": `[{"type":"c8y_ExternalId","externalId":"line-a@example"},
		                        {"type":"c8y_Serial","externalId":"LINE-A-SN"}]`,
		"a single identity of another type": `[{"type":"c8y_ExternalId","externalId":"only@example"}]`,
		"several and none preferred": `[{"type":"c8y_ExternalId","externalId":"a"},
		                               {"type":"c8y_Imei","externalId":"b"}]`,
	}

	for name, identities := range cases {
		dir := t.TempDir()
		object := `{"name":"Device","type":"c8y_Thing","c8y_IsDevice":{}}`
		if err := os.WriteFile(filepath.Join(dir, "exported_device_9.json"), []byte(object), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "9_identities.json"), []byte(identities), 0o644); err != nil {
			t.Fatal(err)
		}

		// What registration would register this device as.
		var discard bytes.Buffer
		plan, err := registration.New(nil, &discard, registration.Options{InputDirectory: dir}).Plan()
		if err != nil {
			t.Fatal(err)
		}
		wantOwner := ""
		if len(plan.Records) == 1 {
			wantOwner = registration.DeviceUsername(plan.Records[0].ExternalID)
		}

		// What the import stamps.
		server, _ := stampedExport(t,
			map[string]string{"9": object},
			map[string]string{"9": identities},
			importer.Options{StampDeviceOwner: true},
		)
		if got := ownerOf(t, server, "Device"); got != wantOwner {
			t.Errorf("%s: the import stamps %q but registration would create %q",
				name, got, wantOwner)
		}
	}
}

// And the prefix itself, which three packages spell out separately.
func TestTheDeviceUserPrefixAgreesEverywhere(t *testing.T) {
	if got, want := registration.DeviceUsername("X"), importer.DeviceUserPrefix+"X"; got != want {
		t.Errorf("registration builds %q, the importer would stamp %q", got, want)
	}
}
