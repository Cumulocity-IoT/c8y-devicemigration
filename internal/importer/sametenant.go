package importer

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/jsonraw"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/session"
)

// SourceHost recovers the host the export was taken from.
//
// Every exported object carries `self`, which is a full URL, so the source host
// is there for the reading. The source tenant *id* is not recorded anywhere in
// the artefacts, which is why the check below needs a second step.
func SourceHost(inputDirectory string) (string, error) {
	files, err := ObjectFiles(inputDirectory)
	if err != nil {
		return "", err
	}
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		self := jsonraw.String(body, "self")
		if self == "" {
			continue
		}
		// https://<host>/inventory/managedObjects/<id>
		rest := strings.TrimPrefix(strings.TrimPrefix(self, "https://"), "http://")
		host, _, _ := strings.Cut(rest, "/")
		if host != "" {
			return session.NormalisedHost(host), nil
		}
	}
	return "", nil
}

// SameTenantError is the refusal, kept as a type so a caller can tell it from a
// transport failure and a test can assert on it rather than on prose.
type SameTenantError struct {
	Host           string
	InputDirectory string
	SessionName    string
	SourceID       string
	TargetID       string
}

func (e *SameTenantError) Error() string {
	return fmt.Sprintf(
		"The export in %s came from the tenant this session points at (%s).\n"+
			"Importing it there would duplicate the hierarchy inside its own tenant.\n"+
			"  export from: %s\n"+
			"  session:     %s\n"+
			"Managed object %s is already the object the export recorded.\n"+
			"Pass --allowSameTenant if duplicating it is really what you want.",
		e.InputDirectory, e.Host, e.Host, e.SessionName, e.SourceID)
}

// CheckNotSameTenant refuses an import back into the tenant the export came
// from, which is a gap the separate export and import commands otherwise have:
// migrate compares its two sessions, but nothing stops the two commands being
// run by hand with the same tenant on both sides.
//
// Two steps, and only the first costs nothing:
//
//  1. compare the host recorded in the export with the session's host. A
//     different host is a different tenant, and that is the common case.
//  2. the same host may still be a different tenant - several tenants can share
//     one - so confirm with a single request. Look up one exported object by its
//     SOURCE id in the target tenant and compare the `self` it returns with the
//     one the export recorded. Ids are tenant-scoped, so a match means this is
//     the tenant the export came from.
func (i *Importer) CheckNotSameTenant(ctx context.Context, target *session.Session) error {
	if i.opts.AllowSameTenant {
		return nil
	}

	sourceHost, err := SourceHost(i.opts.InputDirectory)
	if err != nil {
		return err
	}
	if sourceHost == "" {
		// An export with no `self` anywhere cannot be placed. Nothing to check.
		return nil
	}
	if sourceHost != session.NormalisedHost(target.Host) {
		return nil
	}

	// Same host. Confirm before refusing, so two tenants sharing a host are not
	// blocked from a perfectly good migration.
	files, err := ObjectFiles(i.opts.InputDirectory)
	if err != nil || len(files) == 0 {
		return err
	}
	body, err := os.ReadFile(files[0])
	if err != nil {
		return err
	}
	sourceID := SourceID(files[0])
	recordedSelf := jsonraw.String(body, "self")

	liveSelf, found := i.Get(ctx, sourceID)
	if !found || liveSelf != recordedSelf {
		// The id does not resolve, or resolves to something else: a different
		// tenant that happens to share the host.
		return nil
	}

	return &SameTenantError{
		Host:           sourceHost,
		InputDirectory: i.opts.InputDirectory,
		SessionName:    target.Name(),
		SourceID:       sourceID,
	}
}
