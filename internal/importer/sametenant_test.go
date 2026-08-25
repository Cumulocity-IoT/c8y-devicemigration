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

// exportFixture exports the fixture and returns the directory, the server and a
// client pointed at it.
func exportFixture(t *testing.T) (*testfixture.Server, *session.Session, string) {
	t.Helper()

	server := testfixture.New(t, "hierarchy.json")
	work := t.TempDir()
	sessionPath := server.WriteSessionFile(t, work, "fixture.json")
	s, err := session.Read(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
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
	return server, s, out
}

func TestSourceHostIsReadFromTheExport(t *testing.T) {
	// The source host is recoverable because every exported object carries
	// `self`. The source tenant id is not recorded anywhere, which is why the
	// check needs a second step.
	server, _, out := exportFixture(t)

	host, err := importer.SourceHost(out)
	if err != nil {
		t.Fatal(err)
	}
	want := session.NormalisedHost(server.URL)
	if host != want {
		t.Fatalf("source host = %q, want %q from the exported self links", host, want)
	}
}

func TestImportIntoAnotherTenantIsAllowed(t *testing.T) {
	// The common case: a different host is a different tenant, decided without
	// a single request.
	_, _, out := exportFixture(t)

	target := testfixture.New(t, "hierarchy.json")
	work := t.TempDir()
	targetSessionPath := target.WriteSessionFile(t, work, "target.json")
	targetSession, err := session.Read(targetSessionPath)
	if err != nil {
		t.Fatal(err)
	}
	client, err := c8yclient.New(targetSession, "")
	if err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	imp := importer.New(client, &stderr, importer.Options{
		InputDirectory: out,
		MappingFile:    filepath.Join(work, "id_mapping.json"),
	})
	if err := imp.CheckNotSameTenant(context.Background(), targetSession); err != nil {
		t.Fatalf("a different host must be allowed: %v", err)
	}
}

func TestImportBackIntoTheSourceTenantIsRefused(t *testing.T) {
	// Same host, and the exported object's source id resolves in the target to
	// exactly the object the export recorded. That is the source tenant.
	server, _, out := exportFixture(t)

	// A session whose host matches the self links in the export, pointing at
	// the same server, so the confirming lookup finds the source objects.
	work := t.TempDir()
	sessionPath := filepath.Join(work, "same.json")
	body := `{"host":"` + server.URL + `","tenant":"t-fixture","username":"u","password":"p"}`
	if err := os.WriteFile(sessionPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	same, err := session.Read(sessionPath)
	if err != nil {
		t.Fatal(err)
	}

	// Rewrite the exported self links to this server's host, which is what an
	// export taken from it would really hold. The fixture server hands out
	// example.cumulocity.com links, so without this the hosts never match.
	rewriteSelfHost(t, out, server.URL)

	client, err := c8yclient.New(same, "")
	if err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	imp := importer.New(client, &stderr, importer.Options{
		InputDirectory: out,
		MappingFile:    filepath.Join(work, "id_mapping.json"),
	})

	err = imp.CheckNotSameTenant(context.Background(), same)
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
		"same.json",
		"--allowSameTenant",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal is missing %q:\n%s", want, err.Error())
		}
	}
	// nothing was created: the check runs before the first write
	if len(server.Created()) != 0 {
		t.Error("the refusal still created objects")
	}
}

func TestAllowSameTenantOverridesTheRefusal(t *testing.T) {
	// Duplicating a hierarchy within one tenant is unusual but real. The flag
	// makes it a decision rather than an accident.
	server, _, out := exportFixture(t)

	work := t.TempDir()
	sessionPath := filepath.Join(work, "same.json")
	body := `{"host":"` + server.URL + `","tenant":"t-fixture","username":"u","password":"p"}`
	if err := os.WriteFile(sessionPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	same, err := session.Read(sessionPath)
	if err != nil {
		t.Fatal(err)
	}

	client, err := c8yclient.New(same, "")
	if err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	imp := importer.New(client, &stderr, importer.Options{
		InputDirectory:  out,
		MappingFile:     filepath.Join(work, "id_mapping.json"),
		AllowSameTenant: true,
	})
	if err := imp.CheckNotSameTenant(context.Background(), same); err != nil {
		t.Fatalf("--allowSameTenant must permit it: %v", err)
	}
}

func TestASharedHostWithADifferentTenantIsAllowed(t *testing.T) {
	// Several tenants can sit behind one host, so host equality alone is not
	// enough to refuse. The confirming lookup is what tells them apart: here
	// the source ids do not resolve in the target at all.
	_, _, out := exportFixture(t)

	target := testfixture.New(t, "empty.json")
	work := t.TempDir()

	// Same host as the export's self links, but a tenant that has never seen
	// those ids.
	rewriteSelfHost(t, out, target.URL)
	sessionPath := filepath.Join(work, "sibling.json")
	body := `{"host":"` + target.URL + `","tenant":"t-sibling","username":"u","password":"p"}`
	if err := os.WriteFile(sessionPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	sibling, err := session.Read(sessionPath)
	if err != nil {
		t.Fatal(err)
	}

	client, err := c8yclient.New(sibling, "")
	if err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	imp := importer.New(client, &stderr, importer.Options{
		InputDirectory: out,
		MappingFile:    filepath.Join(work, "id_mapping.json"),
	})
	if err := imp.CheckNotSameTenant(context.Background(), sibling); err != nil {
		t.Fatalf("a different tenant on the same host must be allowed: %v", err)
	}
}

// rewriteSelfHost points the exported self links at a different host, which is
// what an export actually taken from that host would hold.
func rewriteSelfHost(t *testing.T, dir, baseURL string) {
	t.Helper()

	host := strings.TrimPrefix(strings.TrimPrefix(baseURL, "https://"), "http://")
	files, err := importer.ObjectFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		updated := strings.ReplaceAll(string(raw), "example.cumulocity.com", host)
		if err := os.WriteFile(file, []byte(updated), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
