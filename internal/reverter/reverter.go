package reverter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/confirm"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/inventory/managedobjects"
)

// MarkerPrefix names the fragment an import stamps onto what it created.
const MarkerPrefix = "c8y_DeviceMigration_"

// Options is the resolved form of the command line. Exactly one of MappingFile
// and RunID is used.
type Options struct {
	MappingFile string
	RunID       string

	// Force skips the confirmation. Deleting is the one thing here that cannot
	// be undone, so the default is to ask.
	Force bool
}

// Reverter deletes what an import created.
type Reverter struct {
	client *api.Client
	stderr io.Writer
	stdin  io.Reader
	opts   Options
}

func New(client *api.Client, stderr io.Writer, stdin io.Reader, opts Options) *Reverter {
	return &Reverter{client: client, stderr: stderr, stdin: stdin, opts: opts}
}

// ValidateRunID refuses a run id that could not be part of a fragment name.
func ValidateRunID(runID string) error {
	for _, r := range runID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return fmt.Errorf(
				"Invalid --runId '%s': fragment names may only contain letters, digits and underscores",
				runID)
		}
	}
	return nil
}

// ErrIncomplete means at least one object survived the revert.
var ErrIncomplete = errors.New("the revert is incomplete")

// Run builds a plan, shows it, asks, and then deletes.
func (r *Reverter) Run(ctx context.Context) error {
	plan, err := r.buildPlan(ctx)
	if err != nil {
		return err
	}

	if plan.Empty() {
		if r.opts.RunID != "" {
			fmt.Fprintf(r.stderr, "No managed object carries %s%s, nothing to revert\n",
				MarkerPrefix, r.opts.RunID)
		} else {
			fmt.Fprintln(r.stderr, "The mapping file lists no objects, nothing to revert")
		}
		return nil
	}

	presentCount := r.printPlan(plan)
	if presentCount == 0 {
		fmt.Fprintln(r.stderr, "Nothing to delete")
		return nil
	}

	confirmed := true
	if !r.opts.Force {
		confirmed = r.confirm(presentCount)
	}

	if confirmed {
		r.deletePlanned(ctx, plan)
	}

	deleted, failed := r.reportOutcome(ctx, plan)

	fmt.Fprintf(r.stderr, "Deleted %d of %d managed objects, %d are still present\n",
		deleted, presentCount, failed)
	if failed > 0 {
		fmt.Fprintln(r.stderr, "The revert is incomplete, rerun it to retry the remaining objects")
		return ErrIncomplete
	}
	return nil
}

func (r *Reverter) buildPlan(ctx context.Context) (*Plan, error) {
	if r.opts.RunID != "" {
		if err := ValidateRunID(r.opts.RunID); err != nil {
			return nil, err
		}
		return r.BuildFromMarker(ctx, r.opts.RunID)
	}
	return r.BuildFromMapping(ctx, r.opts.MappingFile)
}

// printPlan shows what will happen and returns how many objects that is.
func (r *Reverter) printPlan(plan *Plan) int {
	fmt.Fprintln(r.stderr, "The following managed objects will be deleted from the active session:")
	for _, entry := range plan.Entries {
		if !entry.Present {
			continue
		}
		if entry.SourceID == NoSourceID {
			// A marker-built plan knows the run but not the origin.
			fmt.Fprintf(r.stderr, "  delete %s (%s)\n", entry.Name, entry.TargetID)
			continue
		}
		fmt.Fprintf(r.stderr, "  delete %s (%s, imported from %s)\n",
			entry.Name, entry.TargetID, entry.SourceID)
	}

	missing := plan.MissingCount()
	if missing > 0 {
		fmt.Fprintln(r.stderr, "")
		fmt.Fprintln(r.stderr, "Already absent, nothing to do for these:")
		for _, entry := range plan.Entries {
			if entry.Present {
				continue
			}
			fmt.Fprintf(r.stderr, "  skip   %s (imported from %s)\n", entry.TargetID, entry.SourceID)
		}
	}

	present := plan.PresentCount()
	fmt.Fprintln(r.stderr, "")
	fmt.Fprintf(r.stderr, "%d managed objects will be deleted, %d are already absent.\n",
		present, missing)
	fmt.Fprintln(r.stderr, "Their external identities and parent/child relationships go with them.")
	fmt.Fprintln(r.stderr, "Deleting a group also propagates to its subgroups, which may remove")
	fmt.Fprintln(r.stderr, "objects that this import did not create.")

	return present
}

// confirm asks once, rather than per object, in the shape c8y asks.
//
// One question rather than c8y's per-request dialog: there is no such layer
// here, and asking once about a plan that has just been printed in full is
// clearer than asking N times. The answer keys are c8y's, so they mean what
// someone used to c8y expects.
func (r *Reverter) confirm(presentCount int) bool {
	action := fmt.Sprintf("Delete %d %s",
		presentCount, plural(presentCount, "managed object", "managed objects"))
	return confirm.Ask(r.stderr, r.stdin, action) == confirm.Yes
}

// deletePlanned deletes each object, tolerating one that has already gone.
//
// Propagation means an object can be removed by its parent before its own
// request is sent, and the library's Delete already treats that 404 as success.
// Errors are not reported here: what actually happened is established below by
// looking, which is the only account that survives propagation and a declined
// confirmation alike.
func (r *Reverter) deletePlanned(ctx context.Context, plan *Plan) {
	for _, entry := range plan.Entries {
		if !entry.Present {
			continue
		}
		// No Cascade: the bash implementation does not pass it either, so
		// Cumulocity's own propagation rules apply and nothing extra is taken.
		//
		// WithDeviceUser, though, because a migration that provisioned
		// credentials created a device user alongside every device it
		// registered. Deleting the object and leaving the user would strand a
		// credential that still authenticates against the target - so the
		// revert would look complete while the fleet could still connect.
		//
		// Set unconditionally rather than only for runs that registered
		// anything. The platform applies it when the object is a device, so it
		// is a no-op on a group, on an asset, and on a device that never had a
		// user - and tracking which objects had one would mean keeping a record
		// that could disagree with the tenant.
		_ = r.client.ManagedObjects.Delete(ctx, entry.TargetID, managedobjects.DeleteOptions{
			WithDeviceUser: true,
		})
	}
}

// reportOutcome establishes the truth by re-reading every object.
//
// Not by trusting exit codes: an object may have been taken by propagation from
// its parent, or left alone because the confirmation was declined, and only
// looking tells those apart.
func (r *Reverter) reportOutcome(ctx context.Context, plan *Plan) (deleted, failed int) {
	type outcome struct {
		entry   Entry
		present bool
	}
	var outcomes []outcome

	for _, entry := range plan.Entries {
		if !entry.Present {
			continue
		}
		result := r.client.ManagedObjects.Get(ctx, entry.TargetID, managedobjects.GetOptions{})
		stillThere := result.Err == nil && !result.IsNotFound()
		if stillThere {
			failed++
		} else {
			deleted++
		}
		outcomes = append(outcomes, outcome{entry: entry, present: stillThere})
	}

	// A declined confirmation leaves everything in place. Listing every object
	// as still present would bury that, so say it once instead.
	if deleted == 0 {
		fmt.Fprintln(r.stderr, "Nothing was deleted")
		return deleted, failed
	}

	for _, o := range outcomes {
		if o.present {
			fmt.Fprintf(r.stderr, "Still present: %s (%s)\n", o.entry.Name, o.entry.TargetID)
			continue
		}
		fmt.Fprintf(r.stderr, "Deleted %s (%s)\n", o.entry.Name, o.entry.TargetID)
	}
	return deleted, failed
}

func plural(n int, singular, many string) string {
	if n == 1 {
		return singular
	}
	return many
}

// displayName mirrors read_name in the bash commands.
func displayName(name string) string {
	name = strings.ReplaceAll(name, "\t", " ")
	name = strings.ReplaceAll(name, "\n", " ")
	if strings.TrimSpace(name) == "" {
		return "<unnamed>"
	}
	return name
}
