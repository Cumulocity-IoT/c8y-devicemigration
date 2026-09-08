package migrate_test

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/migrate"
)

func credentialsPath(t *testing.T, h *harness) string {
	t.Helper()
	return filepath.Join(h.work, "credentials.csv")
}

func readCredentials(t *testing.T, path string) [][]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// The whole order in one assertion: the objects a migration with credentials
// leaves in the target are owned by their device users, which is only possible
// if registration ran before the import and the import updated what it made.
func TestMigrateWithCredentialsLeavesObjectsOwnedByTheirDevices(t *testing.T) {
	h := setup(t, migrate.Options{})
	path := credentialsPath(t, h)
	h.opts.Credentials = true
	h.opts.CredentialsFile = path

	stderr, err := h.run(t, "")
	if err != nil {
		t.Fatalf("migration failed: %v\n%s", err, stderr)
	}

	rows := h.target.Registrations()
	if len(rows) == 0 {
		t.Fatal("no devices were registered")
	}
	if len(h.target.Updated()) != len(rows) {
		t.Errorf("registered %d devices but updated %d objects", len(rows), len(h.target.Updated()))
	}

	owners := map[string]string{}
	for _, body := range h.target.Created() {
		owners[gjson.GetBytes(body, "name").String()] = gjson.GetBytes(body, "owner").String()
	}
	for _, row := range rows {
		if got, want := owners[row.Name], "device_"+row.ExternalID; got != want {
			t.Errorf("object %q is owned by %q, want %q", row.Name, got, want)
		}
	}
}

// The file is the only copy of the passwords, and it has to match what was sent.
func TestMigrateWritesTheCredentialsFile(t *testing.T) {
	h := setup(t, migrate.Options{})
	path := credentialsPath(t, h)
	h.opts.Credentials = true
	h.opts.CredentialsFile = path

	stderr, err := h.run(t, "")
	if err != nil {
		t.Fatalf("migration failed: %v\n%s", err, stderr)
	}

	rows := readCredentials(t, path)
	if want := []string{"EXTERNAL_ID", "TENANT", "USERNAME", "PASSWORD"}; strings.Join(rows[0], ",") != strings.Join(want, ",") {
		t.Fatalf("header is %v, want %v", rows[0], want)
	}
	if got, want := len(rows)-1, len(h.target.Registrations()); got != want {
		t.Fatalf("file holds %d credentials for %d registered devices", got, want)
	}

	sent := map[string]string{}
	for _, row := range h.target.Registrations() {
		sent[row.ExternalID] = row.Password
	}
	for _, row := range rows[1:] {
		externalID, username, password := row[0], row[2], row[3]
		if sent[externalID] != password {
			t.Errorf("%s was registered with one password and issued another", externalID)
		}
		if want := "device_" + externalID; username != want {
			t.Errorf("username for %s is %q, want %q", externalID, username, want)
		}
	}
	if !strings.Contains(stderr, "This is the only copy") {
		t.Errorf("the file was written without saying it is the only copy:\n%s", stderr)
	}
}

// A list of working device passwords must not be world readable.
func TestCredentialsFileIsNotWorldReadable(t *testing.T) {
	h := setup(t, migrate.Options{})
	path := credentialsPath(t, h)
	h.opts.Credentials = true
	h.opts.CredentialsFile = path

	if _, err := h.run(t, ""); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("credentials file is mode %o, want no group or other access", mode)
	}
}

// Without --credentials nothing is registered and no file appears: the default
// migration is exactly what it always was.
func TestMigrateWithoutCredentialsRegistersNothing(t *testing.T) {
	h := setup(t, migrate.Options{})

	stderr, err := h.run(t, "")
	if err != nil {
		t.Fatalf("migration failed: %v\n%s", err, stderr)
	}
	if len(h.target.Registrations()) != 0 {
		t.Errorf("registered %d devices without --credentials", len(h.target.Registrations()))
	}
	if len(h.target.Updated()) != 0 {
		t.Errorf("updated %d objects without --credentials", len(h.target.Updated()))
	}
	if _, err := os.Stat(credentialsPath(t, h)); err == nil {
		t.Error("a credentials file was written without --credentials")
	}
}

// The confirmation has to say that device users will be created: it is the part
// of the run that reaches outside the inventory.
func TestConfirmationMentionsCredentials(t *testing.T) {
	h := setup(t, migrate.Options{})
	h.opts.Credentials = true
	h.opts.CredentialsFile = credentialsPath(t, h)
	h.opts.Force = false

	stderr, err := h.run(t, "no\n")
	if err == nil {
		t.Fatal("the declined confirmation did not abort")
	}
	if !strings.Contains(stderr, "provisioning a device user for each device") {
		t.Errorf("the prompt did not mention device users:\n%s", stderr)
	}
	if len(h.target.Registrations()) != 0 {
		t.Error("a declined confirmation still registered devices")
	}
}

// A resume never re-registers: that would rotate the passwords the first attempt
// handed over, and the devices in that file were already working.
func TestResumeDoesNotReRegister(t *testing.T) {
	h := setup(t, migrate.Options{Incremental: true, DataTypes: allData})
	path := credentialsPath(t, h)
	h.opts.Credentials = true
	h.opts.CredentialsFile = path

	// Stopped part way through the data phase, after registration.
	h.target.FailDataAfter(1)
	if _, err := h.run(t, ""); err == nil {
		t.Fatal("the interrupted run reported success")
	}
	first := readCredentials(t, path)
	registeredFirst := len(h.target.Registrations())
	if registeredFirst == 0 {
		t.Fatal("the first attempt registered nothing")
	}

	h.target.AllowData()
	h.opts.Resume = h.runID(t)
	stderr, err := h.run(t, "")
	if err != nil {
		t.Fatalf("resume failed: %v\n%s", err, stderr)
	}

	if got := len(h.target.Registrations()); got != registeredFirst {
		t.Errorf("the resume registered again: %d rows now, %d before", got, registeredFirst)
	}
	if !strings.Contains(stderr, "already registered its devices") {
		t.Errorf("the resume did not explain why it issued nothing:\n%s", stderr)
	}
	// And the file from the first attempt is still the valid one.
	if second := readCredentials(t, path); len(second) != len(first) {
		t.Errorf("the credentials file changed on resume")
	}
}
