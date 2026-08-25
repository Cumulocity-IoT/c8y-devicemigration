package importer

import (
	"context"
	"fmt"
	"os"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/jsonraw"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/session"
)

// probeLimit is how many exported objects are looked up before giving up.
//
// More than one because the obvious way for this check to miss is an object that
// has been deleted from the source since the export; few enough that the cost is
// negligible against what an import is about to do anyway.
const probeLimit = 3

// SameTenantError is the refusal, kept as a type so a caller can tell it from a
// transport failure and a test can assert on it rather than on prose.
type SameTenantError struct {
	InputDirectory string
	SessionName    string

	// SourceID is the object that gave it away, and Name is what that object is
	// called - which is far more use to a reader than an id on its own.
	SourceID string
	Name     string
}

func (e *SameTenantError) Error() string {
	return fmt.Sprintf(
		"The export in %s came from the tenant this session points at.\n"+
			"Importing it there would duplicate the hierarchy inside its own tenant.\n"+
			"  session: %s\n"+
			"  evidence: managed object %s is already %s, exactly as the export recorded it\n"+
			"Pass --allowSameTenant if duplicating it is really what you want.",
		e.InputDirectory, e.SessionName, e.SourceID, e.Name)
}

// CheckNotSameTenant refuses an import back into the tenant the export came
// from, which is a gap the separate export and import commands otherwise have:
// migrate compares its two sessions, but nothing stops the two commands being
// run by hand with the same tenant on both sides.
//
// It works by looking up an exported object by its SOURCE id and asking whether
// the object that comes back is the one the export recorded. Managed object ids
// are tenant-scoped, so an id that resolves to an object with the same name,
// type and creation time is the same object - and therefore the same tenant.
//
// It deliberately does NOT compare the export's `self` host against the
// session's host. That was the first design and it was wrong: `self` carries
// whatever host the platform felt like using - a tenant-id domain rather than a
// friendly one, a custom domain, or in an anonymised export something that was
// never real - so a mismatch says nothing, and gating the check on a match made
// it silently pass on exactly the imports it exists to stop.
func (i *Importer) CheckNotSameTenant(ctx context.Context, target *session.Session) error {
	if i.opts.AllowSameTenant {
		return nil
	}

	files, err := ObjectFiles(i.opts.InputDirectory)
	if err != nil {
		return err
	}

	probed := 0
	for _, file := range files {
		if probed >= probeLimit {
			break
		}

		recorded, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		sourceID := SourceID(file)
		if sourceID == "" {
			continue
		}
		probed++

		live, found := i.getObject(ctx, sourceID)
		if !found {
			// The id does not exist here at all, which is the normal case for a
			// genuinely different tenant.
			continue
		}
		if !sameObject(recorded, live) {
			// Something else lives at that id. A different tenant that happens
			// to use overlapping ids.
			continue
		}

		return &SameTenantError{
			InputDirectory: i.opts.InputDirectory,
			SessionName:    target.Name(),
			SourceID:       sourceID,
			Name:           displayName(jsonraw.Name(recorded)),
		}
	}
	return nil
}

// sameObject reports whether a live object is the one an export recorded.
//
// Name, type and creation time together: the name and type could coincide, but
// the creation time is assigned by the platform and will not also match for an
// unrelated object that happens to share an id.
func sameObject(recorded, live []byte) bool {
	for _, field := range []string{"name", "type", "creationTime"} {
		if jsonraw.String(recorded, field) != jsonraw.String(live, field) {
			return false
		}
	}
	// An object with none of those fields would compare equal to anything, so
	// require at least a creation time, which every managed object has.
	return jsonraw.String(recorded, "creationTime") != ""
}
