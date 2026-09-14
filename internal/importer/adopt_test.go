package importer_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/c8yclient"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/export"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/importer"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/registration"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/testfixture"
)

const testRunID = "20260908T101112Z_ab12cd"

// registerThenImport runs the two phases in the order Option A uses: register
// the devices in the target, then import onto the objects registration created.
//
// One server standing in for both tenants, as the other import tests do.
func registerThenImport(t *testing.T, opts importer.Options) (*testfixture.Server, string, string) {
	t.Helper()

	server := testfixture.New(t, "hierarchy.json")
	work := t.TempDir()
	s := server.ReadSession(t, work, "fixture.json")
	client, err := c8yclient.New(s, "")
	if err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(work, "export")
	var exportErr bytes.Buffer
	if err := export.New(client, &exportErr, export.Options{
		RootID: "1", OutputDirectory: out, ChildTypes: export.DefaultChildTypes,
	}).Run(context.Background()); err != nil {
		t.Fatalf("export failed: %v\n%s", err, exportErr.String())
	}

	var registerErr bytes.Buffer
	if _, err := registration.New(client, &registerErr, registration.Options{
		InputDirectory: out,
		Marker:         importer.MarkerPrefix + testRunID,
		Tenant:         "t1186848933",
	}).Run(context.Background()); err != nil {
		t.Fatalf("registration failed: %v\n%s", err, registerErr.String())
	}

	opts.InputDirectory = out
	opts.MappingFile = filepath.Join(work, "id_mapping.json")
	opts.RunID = testRunID
	opts.AllowSameTenant = true

	var stderr bytes.Buffer
	if err := importer.New(client, &stderr, opts).Run(context.Background()); err != nil {
		t.Fatalf("import failed: %v\n%s", err, stderr.String())
	}
	return server, stderr.String(), out
}

// objectOf resolves the object a registered external id belongs to, through the
// identity the registration created - the same route the importer takes.
func objectOf(t *testing.T, server *testfixture.Server, externalID string) (string, []byte) {
	t.Helper()
	for _, created := range server.CreatedIdentities() {
		if created.ExternalID == externalID {
			body, ok := server.Created()[created.DeviceID]
			if !ok {
				t.Fatalf("identity %s points at object %s, which does not exist",
					externalID, created.DeviceID)
			}
			return created.DeviceID, body
		}
	}
	t.Fatalf("no identity in the target for %s", externalID)
	return "", nil
}

// The point of the whole order: a registered device is updated, not duplicated.
func TestAdoptUpdatesRegisteredObjectsInsteadOfCreatingThem(t *testing.T) {
	server, stderr, exportDir := registerThenImport(t, importer.Options{AdoptRegistered: true})

	registered := len(server.Registrations())
	if registered == 0 {
		t.Fatal("nothing was registered, so there was nothing to adopt")
	}
	if !strings.Contains(stderr, "were objects this run had already registered") {
		t.Errorf("the summary does not mention adopted objects:\n%s", stderr)
	}

	// Every registered device was adopted rather than duplicated, so the number
	// of updates is exactly the number of registrations.
	if got := len(server.Updated()); got != registered {
		t.Errorf("updated %d objects, want the %d that were registered", got, registered)
	}

	// And one object per exported device: the registered ones plus the rest the
	// import created, with nothing counted twice.
	exported, err := importer.ObjectFiles(exportDir)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(server.Created()), len(exported); got != want {
		t.Errorf("target holds %d objects for %d exported devices", got, want)
	}

	// No two objects share a name, which is what a duplicate would look like.
	names := map[string]string{}
	for id, body := range server.Created() {
		name := gjson.GetBytes(body, "name").String()
		if other, clash := names[name]; clash && name != "" {
			t.Errorf("objects %s and %s are both called %q", other, id, name)
		}
		names[name] = id
	}
}

// Ownership is the reason register-first exists. The update must not hand a
// device's twin back to the service user, which is exactly what would happen if
// the exported `owner` were sent - and `owner` being on StripServerManaged's
// list is the only thing that stops it.
func TestAdoptLeavesTheDeviceUserAsOwner(t *testing.T) {
	server, _, _ := registerThenImport(t, importer.Options{AdoptRegistered: true})

	rows := server.Registrations()
	if len(rows) == 0 {
		t.Fatal("nothing was registered")
	}
	for _, row := range rows {
		id, body := objectOf(t, server, row.ExternalID)
		if got, want := gjson.GetBytes(body, "owner").String(), "device_"+row.ExternalID; got != want {
			t.Errorf("object %s for %s is owned by %q, want %q", id, row.ExternalID, got, want)
		}
		// And the exported content really did arrive on it, or "ownership
		// survived" would only mean the update never happened.
		if gjson.GetBytes(body, "name").String() == "" {
			t.Errorf("object %s for %s has no name, so the import did not fill it in",
				id, row.ExternalID)
		}
	}
}

// The marker has to end up saying which phase last touched the object, so a run
// that died between the two is legible from the tenant alone.
func TestAdoptAdvancesTheMarkerPhase(t *testing.T) {
	server, _, _ := registerThenImport(t, importer.Options{AdoptRegistered: true})

	marker := importer.MarkerPrefix + testRunID
	var seen int
	for _, body := range server.Updated() {
		phase := gjson.GetBytes(body, gjson.Escape(marker)+".phase").String()
		if phase == "" {
			continue
		}
		if phase != importer.PhaseImported {
			t.Errorf("marker phase is %q, want %q", phase, importer.PhaseImported)
		}
		seen++
	}
	if seen == 0 {
		t.Fatalf("no update carried the marker %s", marker)
	}
}

// The identity registration created must not be reported as a collision: it
// exists because everything worked.
func TestAdoptDoesNotReportTheRegisteredIdentityAsAFailure(t *testing.T) {
	_, stderr, _ := registerThenImport(t, importer.Options{AdoptRegistered: true})

	if strings.Contains(stderr, "external identities could not be created") {
		t.Errorf("an adopted import reported identity failures:\n%s", stderr)
	}
	if !strings.Contains(stderr, "created when the device was registered") {
		t.Errorf("the skipped identity was not explained:\n%s", stderr)
	}
}

// Off by default, and the default has to stay what it always was: create, never
// update. "Existing objects in the target tenant are never matched or updated"
// is documented behaviour.
func TestWithoutAdoptRegisteredNothingIsUpdated(t *testing.T) {
	server, stderr, _ := registerThenImport(t, importer.Options{})

	if len(server.Updated()) != 0 {
		t.Errorf("an import without AdoptRegistered updated %d objects", len(server.Updated()))
	}
	if strings.Contains(stderr, "were objects this run had already registered") {
		t.Errorf("an import without AdoptRegistered claimed to adopt:\n%s", stderr)
	}
	// The registered identities are still in the target, so the collisions the
	// import reports are the ones it always reported.
	if !strings.Contains(stderr, "it may already exist in the target tenant") {
		t.Errorf("expected the usual identity collisions:\n%s", stderr)
	}
}

// The safety gate. An object carrying the same external id but NOT this run's
// marker belongs to somebody else, and adopting it would silently overwrite it.
func TestAdoptRefusesAnObjectThisRunDidNotRegister(t *testing.T) {
	server := testfixture.New(t, "hierarchy.json")
	work := t.TempDir()
	s := server.ReadSession(t, work, "fixture.json")
	client, err := c8yclient.New(s, "")
	if err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(work, "export")
	var exportErr bytes.Buffer
	if err := export.New(client, &exportErr, export.Options{
		RootID: "1", OutputDirectory: out, ChildTypes: export.DefaultChildTypes,
	}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Registered under a DIFFERENT run, so the objects exist and carry a marker
	// - just not this one.
	var registerErr bytes.Buffer
	if _, err := registration.New(client, &registerErr, registration.Options{
		InputDirectory: out,
		Marker:         importer.MarkerPrefix + "20260101T000000Z_999999",
		Tenant:         "t1186848933",
	}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	err = importer.New(client, &stderr, importer.Options{
		InputDirectory:  out,
		MappingFile:     filepath.Join(work, "id_mapping.json"),
		RunID:           testRunID,
		AdoptRegistered: true,
		AllowSameTenant: true,
	}).Run(context.Background())
	if err != nil {
		t.Fatalf("import failed: %v\n%s", err, stderr.String())
	}

	if len(server.Updated()) != 0 {
		t.Errorf("adopted %d objects another run had registered", len(server.Updated()))
	}
	if !strings.Contains(stderr.String(), "which this run did not create") {
		t.Errorf("the refusal was not explained:\n%s", stderr.String())
	}
}

// --noFlag turns marking off, and without a marker there is no way to tell an
// object this run registered from any other - so adoption cannot be safe and is
// not attempted.
func TestAdoptDoesNothingWithoutAMarker(t *testing.T) {
	server, _, _ := registerThenImport(t, importer.Options{AdoptRegistered: true, NoFlag: true})

	if len(server.Updated()) != 0 {
		t.Errorf("adopted %d objects with no run marker to check against", len(server.Updated()))
	}
}
