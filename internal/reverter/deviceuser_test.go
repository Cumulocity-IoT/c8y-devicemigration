package reverter_test

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/c8yclient"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/export"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/importer"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/registration"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/reverter"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/testfixture"
)

// A revert of a migration that provisioned credentials has to take the device
// users with the objects. Leaving them behind would leave credentials that still
// authenticate against the target, while the revert reported success - the
// failure withDeviceUser exists to prevent.
func TestRevertDeletesTheDeviceUsersRegistrationCreated(t *testing.T) {
	const runID = "20260908T101112Z_ab12cd"

	server := testfixture.New(t, "hierarchy.json")
	work := t.TempDir()
	s := server.ReadSession(t, work, "fixture.json")
	client, err := c8yclient.New(s, "")
	if err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(work, "export")
	var stderr bytes.Buffer
	if err := export.New(client, &stderr, export.Options{
		RootID: "1", OutputDirectory: out, ChildTypes: export.DefaultChildTypes,
	}).Run(context.Background()); err != nil {
		t.Fatalf("export failed: %v\n%s", err, stderr.String())
	}

	if _, err := registration.New(client, &stderr, registration.Options{
		InputDirectory: out,
		Marker:         importer.MarkerPrefix + runID,
		Tenant:         "t1186848933",
	}).Run(context.Background()); err != nil {
		t.Fatalf("registration failed: %v\n%s", err, stderr.String())
	}
	if len(server.DeviceUsers()) == 0 {
		t.Fatal("registration created no device users, so there is nothing to prove")
	}

	mapping := filepath.Join(work, "id_mapping.json")
	if err := importer.New(client, &stderr, importer.Options{
		InputDirectory:  out,
		MappingFile:     mapping,
		RunID:           runID,
		AdoptRegistered: true,
		AllowSameTenant: true,
	}).Run(context.Background()); err != nil {
		t.Fatalf("import failed: %v\n%s", err, stderr.String())
	}

	if err := reverter.New(client, &stderr, bytes.NewReader(nil), reverter.Options{
		MappingFile: mapping,
		Force:       true,
	}).Run(context.Background()); err != nil {
		t.Fatalf("revert failed: %v\n%s", err, stderr.String())
	}

	if remaining := server.DeviceUsers(); len(remaining) != 0 {
		t.Errorf("the revert left %d device users behind: %v", len(remaining), remaining)
	}

	// The flag lands on exactly the objects a device user owned, and nowhere
	// else. Not on every delete: withDeviceUser=true on a device that has no
	// device user is a 409 that leaves the object where it is, so setting it
	// everywhere would break the revert of every migration that did not
	// provision credentials.
	registered := len(server.Registrations())
	if got := len(server.DeletedWithDeviceUser()); got != registered {
		t.Errorf("%d deletes carried withDeviceUser, want the %d that had a device user",
			got, registered)
	}
	if len(server.Deleted()) <= registered {
		t.Fatalf("only %d objects were deleted, so the flag was not tested against objects "+
			"that have no device user", len(server.Deleted()))
	}
}

// The regression the flag introduced, as a test: a migration that provisioned
// nothing must still revert completely. Every one of its devices is owned by an
// ordinary user, and the platform answers withDeviceUser=true on those with a
// 409 - so a reverter that sets it unconditionally deletes none of them.
func TestRevertWithoutCredentialsStillDeletesEverything(t *testing.T) {
	server := testfixture.New(t, "hierarchy.json")
	work := t.TempDir()
	s := server.ReadSession(t, work, "fixture.json")
	client, err := c8yclient.New(s, "")
	if err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(work, "export")
	var stderr bytes.Buffer
	if err := export.New(client, &stderr, export.Options{
		RootID: "1", OutputDirectory: out, ChildTypes: export.DefaultChildTypes,
	}).Run(context.Background()); err != nil {
		t.Fatalf("export failed: %v\n%s", err, stderr.String())
	}

	mapping := filepath.Join(work, "id_mapping.json")
	if err := importer.New(client, &stderr, importer.Options{
		InputDirectory:  out,
		MappingFile:     mapping,
		AllowSameTenant: true,
	}).Run(context.Background()); err != nil {
		t.Fatalf("import failed: %v\n%s", err, stderr.String())
	}
	if len(server.Created()) == 0 {
		t.Fatal("the import created nothing")
	}

	if err := reverter.New(client, &stderr, bytes.NewReader(nil), reverter.Options{
		MappingFile: mapping,
		Force:       true,
	}).Run(context.Background()); err != nil {
		t.Fatalf("revert failed: %v\n%s", err, stderr.String())
	}

	if survived := len(server.Created()); survived != 0 {
		t.Fatalf("%d objects survived a revert of a migration without credentials", survived)
	}
	if carried := len(server.DeletedWithDeviceUser()); carried != 0 {
		t.Errorf("%d deletes carried withDeviceUser when no device user existed", carried)
	}
}
