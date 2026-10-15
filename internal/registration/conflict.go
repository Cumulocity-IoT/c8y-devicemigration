package registration

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/identity"
)

// Refusing to register an external identity the target tenant already holds.
//
// Bulk registration creates nothing for an external id the target already knows.
// It sets a new password on the device user that id belongs to and leaves the
// object alone, which the platform reports as CREDENTIAL_UPDATED rather than as
// a failure. For a migration that is the wrong outcome twice over: a device the
// migration is not about stops working with the credential it holds, and the
// credential that replaced it belongs to a run that device has nothing to do
// with.
//
// So every identity a registration is about to create is resolved against the
// target first, and one match refuses the whole run. There is deliberately no
// override and no per-device skip: both resolutions that are actually safe are
// outside this tool - migrate without provisioning credentials, or settle the
// duplicate identity in the two tenants first - and a flag would turn a refusal
// nobody can get wrong into a prompt somebody clicks through.
//
// The check is the registration phase's, not the import's. An import that meets
// the same identity creates its own object and reports the collision, which has
// always been safe: it writes to nothing that already existed.

// Conflict is one device whose external identity the target tenant already
// knows.
type Conflict struct {
	// SourceID is the exported object, so the device can be found in the tenant
	// it came from.
	SourceID string
	Name     string

	ExternalID   string
	ExternalType string

	// TargetID is the object already carrying that identity in the target, which
	// is what somebody has to look at to decide what the duplicate means.
	TargetID string
}

// ConflictError refuses a registration that would have reached an existing
// device.
//
// A type rather than prose because three callers need the list and not just the
// sentence: the command line prints a line per device, the service turns it into
// a refusal a screen can render, and a test asserts on which devices were named.
type ConflictError struct {
	Conflicts []Conflict
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf(
		"%d of the %s to be registered already exist in the target tenant under the same "+
			"external identity. Registering them would replace the credentials those "+
			"existing devices authenticate with, so nothing was registered and nothing "+
			"was created. Migrate without provisioning credentials, or resolve the "+
			"duplicate identities first",
		len(e.Conflicts), plural(len(e.Conflicts), "device", "devices"))
}

// FindConflicts resolves each record's external identity against the target.
//
// One lookup per device, which is the same per-device cost the import already
// pays to adopt registered objects. Ordered by the records so that two runs over
// one export report the same devices in the same order.
func FindConflicts(ctx context.Context, client *api.Client, records []Record) ([]Conflict, error) {
	var conflicts []Conflict

	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		result := client.Identity.Get(ctx, identity.IdentityOptions{
			Type:       record.ExternalType,
			ExternalID: record.ExternalID,
		})
		if result.Err != nil {
			if result.HTTPStatus == http.StatusNotFound {
				// The ordinary answer, and the one that lets a device be
				// registered: the target has never heard of this identity.
				continue
			}
			// Deliberately not treated as "no conflict". This check exists to
			// refuse, and a lookup that did not answer is not evidence that there
			// is nothing there - carrying on would register the one device the
			// check is meant to protect. Contrast findAdopted, where failing open
			// is safe because the fallback is creating a new object.
			return nil, fmt.Errorf(
				"cannot check whether %s:%s already exists in the target tenant, so no "+
					"device was registered: %w",
				record.ExternalType, record.ExternalID, result.Err)
		}

		conflicts = append(conflicts, Conflict{
			SourceID:     record.SourceID,
			Name:         record.Name,
			ExternalID:   record.ExternalID,
			ExternalType: record.ExternalType,
			TargetID:     result.Data.ManagedObjectID(),
		})
	}
	return conflicts, nil
}

// ReportConflicts prints one line per conflict, then what it means.
//
// Shared by the registration phase and the command line's dry run so that the
// two say the same thing: a dry run that described the refusal differently from
// the run would be read as a different check.
func ReportConflicts(w io.Writer, conflicts []Conflict) {
	for _, conflict := range conflicts {
		fmt.Fprintf(w, "Already in the target: %s (%s) as %s:%s, on object %s\n",
			displayName(conflict.Name), conflict.SourceID,
			conflict.ExternalType, conflict.ExternalID, conflict.TargetID)
	}
}
