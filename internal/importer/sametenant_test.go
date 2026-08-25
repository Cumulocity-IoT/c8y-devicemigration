package importer_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/c8yclient"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/export"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/importer"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/session"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/testfixture"
)

// The guard exists because migrate compares its two sessions but nothing stops
// export and import being run by hand with the same tenant on both sides. The
// result is the whole hierarchy duplicated inside its own tenant.

// exportFrom exports the fixture and returns the server, its session and the
// export directory.
func exportFrom(t *testing.T, server *testfixture.Server) (*session.Session, string) {
	t.Helper()

	work := t.TempDir()
	s := server.ReadSession(t, work, "fixture.json")
	client, err := c8yclient.New(s, "")
	if err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(work, "export")
	var discard bytes.Buffer
	if err := export.New(client, &discard, export.Options{
		RootID: "1", OutputDirectory: out, ChildTypes: export.DefaultChildTypes,
	}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, out
}

func checkAgainst(t *testing.T, s *session.Session, inputDirectory string,
	allowSameTenant bool) error {
	t.Helper()

	client, err := c8yclient.New(s, "")
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	imp := importer.New(client, &stderr, importer.Options{
		InputDirectory:  inputDirectory,
		MappingFile:     filepath.Join(t.TempDir(), "id_mapping.json"),
		AllowSameTenant: allowSameTenant,
	})
	return imp.CheckNotSameTenant(context.Background(), s)
}

func TestImportBackIntoTheSourceTenantIsRefused(t *testing.T) {
	server := testfixture.New(t, "hierarchy.json")
	s, out := exportFrom(t, server)

	err := checkAgainst(t, s, out, false)
	if err == nil {
		t.Fatal("importing back into the source tenant must be refused")
	}

	var sameTenant *importer.SameTenantError
	if !errors.As(err, &sameTenant) {
		t.Fatalf("got %T, want *importer.SameTenantError", err)
	}
	for _, want := range []string{
		"came from the tenant this session points at",
		"duplicate the hierarchy inside its own tenant",
		"fixture.json",
		"evidence: managed object 1 is already Plant Berlin",
		"--allowSameTenant",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal is missing %q:\n%s", want, err.Error())
		}
	}
	if len(server.Created()) != 0 {
		t.Error("the refusal still created objects")
	}
}

func TestRefusedEvenWhenTheSelfHostDoesNotMatch(t *testing.T) {
	// The bug this check had. `self` carries whatever host the platform felt
	// like using - a tenant-id domain, a custom domain, or in an anonymised
	// export something that was never real - so comparing it against the
	// session's host and only then probing meant the check silently passed on
	// exactly the imports it exists to stop.
	server := testfixture.New(t, "hierarchy.json")
	s, out := exportFrom(t, server)

	rewriteSelfHost(t, out, server.URL, "https://somewhere-else.example.com")

	err := checkAgainst(t, s, out, false)
	if err == nil {
		t.Fatal("a mismatched self host must not stop the check: the objects are still here")
	}
	var sameTenant *importer.SameTenantError
	if !errors.As(err, &sameTenant) {
		t.Fatalf("got %T, want *importer.SameTenantError", err)
	}
}

func TestImportIntoAnotherTenantIsAllowed(t *testing.T) {
	// A tenant that has never seen those ids, which is the normal case.
	source := testfixture.New(t, "hierarchy.json")
	_, out := exportFrom(t, source)

	target := testfixture.New(t, "empty.json")
	work := t.TempDir()
	targetSession := target.ReadSession(t, work, "target.json")

	if err := checkAgainst(t, targetSession, out, false); err != nil {
		t.Fatalf("a different tenant must be allowed: %v", err)
	}
}

func TestADifferentObjectAtTheSameIDIsAllowed(t *testing.T) {
	// Two tenants can use overlapping ids. What matters is whether the object
	// there is the one the export recorded, not whether the id resolves.
	source := testfixture.New(t, "hierarchy.json")
	_, out := exportFrom(t, source)

	// A tenant whose id 1 is a different object entirely.
	target := testfixture.New(t, "many-measurements.json")
	work := t.TempDir()
	targetSession := target.ReadSession(t, work, "target.json")

	if err := checkAgainst(t, targetSession, out, false); err != nil {
		t.Fatalf("a different object at the same id must be allowed: %v", err)
	}
}

func TestAllowSameTenantOverridesTheRefusal(t *testing.T) {
	// Duplicating a hierarchy within one tenant is unusual but real. The flag
	// makes it a decision rather than an accident.
	server := testfixture.New(t, "hierarchy.json")
	s, out := exportFrom(t, server)

	if err := checkAgainst(t, s, out, true); err != nil {
		t.Fatalf("--allowSameTenant must permit it: %v", err)
	}
}

func TestTheCheckSurvivesADeletedFirstObject(t *testing.T) {
	// The obvious way for the probe to miss is an object that has been deleted
	// from the source since the export, which is why more than one is tried.
	server := testfixture.New(t, "hierarchy.json")
	s, out := exportFrom(t, server)

	// Hide the object the first probe would look at. The fixture server serves
	// the source hierarchy, so removing it from the fixture is what "deleted
	// since the export" looks like.
	server.HideFromHierarchy("1")

	err := checkAgainst(t, s, out, false)
	if err == nil {
		t.Fatal("one deleted object must not blind the check")
	}
}

func TestTheCheckCostsAtMostAHandfulOfRequests(t *testing.T) {
	// It runs before every import, so it must not walk the whole export.
	source := testfixture.New(t, "hierarchy.json")
	_, out := exportFrom(t, source)

	target := testfixture.New(t, "empty.json")
	work := t.TempDir()
	targetSession := target.ReadSession(t, work, "target.json")

	if err := checkAgainst(t, targetSession, out, false); err != nil {
		t.Fatal(err)
	}
	// nine objects in the export, at most three looked up
	if got := target.CountRequests("/inventory/managedObjects/"); got > 3 {
		t.Errorf("the check made %d lookups, want at most 3", got)
	}
}

func TestAnExportWithNoObjectsIsNothingToCheck(t *testing.T) {
	server := testfixture.New(t, "hierarchy.json")
	work := t.TempDir()
	s := server.ReadSession(t, work, "fixture.json")

	empty := filepath.Join(work, "empty-export")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkAgainst(t, s, empty, false); err != nil {
		t.Fatalf("an export with no objects has nothing to check: %v", err)
	}
}

// rewriteSelfHost points the exported self links at a different host, which is
// what an anonymised export or a tenant with a custom domain looks like.
//
// A plain substitution of the host, deliberately: an earlier version matched on
// `"self": "https://` and so did nothing at all against the compact JSON the
// fixture server emits, which made the test that depends on it vacuous.
func rewriteSelfHost(t *testing.T, dir, from, to string) {
	t.Helper()

	fromHost := strings.TrimPrefix(strings.TrimPrefix(from, "https://"), "http://")
	toHost := strings.TrimPrefix(strings.TrimPrefix(to, "https://"), "http://")

	files, err := importer.ObjectFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no exported objects to rewrite")
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), fromHost) {
			t.Fatalf("%s does not mention %q, so rewriting it would prove nothing",
				filepath.Base(file), fromHost)
		}
		updated := strings.ReplaceAll(string(raw), fromHost, toHost)
		if err := os.WriteFile(file, []byte(updated), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
