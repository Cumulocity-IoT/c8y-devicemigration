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
	// Every delete carries the flag, not just the ones that happened to have a
	// user: the platform applies it only where the object is a device, so
	// setting it always is simpler than keeping a record that could disagree
	// with the tenant.
	if len(server.DeletedWithDeviceUser()) != len(server.Deleted()) {
		t.Errorf("%d of %d deletes carried withDeviceUser",
			len(server.DeletedWithDeviceUser()), len(server.Deleted()))
	}
}
