package importer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/identity"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/inventory/managedobjects"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/op"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/jsonraw"
)

// MarkerPrefix names the fragment stamped onto every created object.
//
// The run identity goes in the fragment NAME and the body stays empty, so one
// indexed --fragmentType query finds everything a run created.
const MarkerPrefix = "c8y_DeviceMigration_"

// Options is the resolved form of the command line.
type Options struct {
	InputDirectory string
	MappingFile    string

	// RunID, when set, becomes part of the marker fragment name. When it is
	// empty and NoFlag is false, one is generated and reported, so every import
	// can be found again by its marker rather than only through the mapping.
	RunID string

	// NoFlag opts out of marking entirely. Without a marker the objects can
	// only be found again through the mapping file, so a lost mapping means a
	// lost migration.
	NoFlag bool

	// Resume continues an import that stopped part way through, using the
	// mapping as the record of what already exists. Objects listed there are
	// not created again, and only the relationships the target is missing are
	// assigned.
	Resume bool

	// DataOnly imports measurements, events and alarms only, resolving each
	// device through an existing mapping. It creates no managed objects and no
	// relationships, so it can add data to a hierarchy migrated earlier.
	DataOnly bool

	// AllowSameTenant permits importing back into the tenant the export came
	// from. Off by default: that duplicates a hierarchy inside itself.
	AllowSameTenant bool
}

// Importer recreates objects, their external identities and their
// relationships.
type Importer struct {
	client *api.Client
	stderr io.Writer
	opts   Options

	mapping        *Mapping
	markerFragment string

	identitiesCreated int
	identitiesFailed  int

	measurementsCreated int
	eventsCreated       int
	alarmsCreated       int
	binariesCreated     int

	// dataFailures counts records that were meant to arrive and did not. The
	// import keeps going - one bad record should not cost a whole migration -
	// but the exit code has to say so, or migrate would journal a device as
	// finished with records missing.
	dataFailures int

	// dataDevices is how many devices DataOnly actually imported data for.
	dataDevices int
}

func New(client *api.Client, stderr io.Writer, opts Options) *Importer {
	return &Importer{
		client:  client,
		stderr:  stderr,
		opts:    opts,
		mapping: NewMapping(opts.MappingFile),
	}
}

// ValidateRunID refuses a run id that could not be a fragment name.
//
// Cumulocity rejects whitespace and punctuation in fragment names, so an ISO
// timestamp cannot be used verbatim - which is why the generated form uses
// underscores.
func ValidateRunID(runID string) error {
	if runID == "" {
		return nil
	}
	for _, r := range runID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return fmt.Errorf("Invalid --runId '%s': letters, digits and underscores only,\n"+
				"because it becomes part of a Cumulocity fragment name.", runID)
		}
	}
	return nil
}

// Run imports everything in the input directory.
func (i *Importer) Run(ctx context.Context) error {
	if err := ValidateRunID(i.opts.RunID); err != nil {
		return err
	}
	if !i.opts.NoFlag {
		if i.opts.RunID == "" {
			i.opts.RunID = GenerateRunID(time.Now())
		}
		i.markerFragment = MarkerPrefix + i.opts.RunID
	}

	if info, err := os.Stat(i.opts.InputDirectory); err != nil || !info.IsDir() {
		return fmt.Errorf("Input directory does not exist: %s", i.opts.InputDirectory)
	}

	// Announced by whatever actually applies it, so the id is in the log of the
	// run that used it - and so it can be recovered later without reading it
	// back off an object's fragments.
	//
	// Not under DataOnly: that creates no objects, so it marks nothing, and
	// saying otherwise would be a lie repeated once per device.
	if i.markerFragment != "" && !i.opts.DataOnly {
		fmt.Fprintf(i.stderr, "Run id %s, marking created objects with %s\n",
			i.opts.RunID, i.markerFragment)
	}

	if i.opts.DataOnly {
		// The mapping is pure input in this mode. It must never be rewritten:
		// an earlier version of this truncated it to {} at startup and took the
		// revert record with it.
		if err := i.LoadMapping(i.opts.MappingFile, i.opts.InputDirectory); err != nil {
			return err
		}
		if err := i.importDataOnly(ctx); err != nil {
			return err
		}
		return i.reportData()
	}

	if i.opts.Resume {
		// The mapping is the input here, so it must be read before anything
		// writes over it.
		if err := i.LoadMapping(i.opts.MappingFile, i.opts.InputDirectory); err != nil {
			return err
		}
		if err := i.PruneDeletedFromMapping(ctx); err != nil {
			return err
		}
	} else {
		// An empty mapping up front, so an immediate failure still leaves a
		// file rather than nothing at all.
		if err := i.mapping.Write(); err != nil {
			return err
		}
	}

	if err := i.importDevices(ctx); err != nil {
		return err
	}
	// A final pass, because a relationship can only be assigned once BOTH ends
	// exist, and the second end may be created much later than the first.
	if err := i.linkChildren(ctx); err != nil {
		return err
	}

	if err := i.mapping.Write(); err != nil {
		return err
	}

	fmt.Fprintf(i.stderr, "Imported %d %s and %d %s, ID mapping written to %s\n",
		i.mapping.Len(), plural(i.mapping.Len(), "device", "devices"),
		i.identitiesCreated, plural(i.identitiesCreated, "external identity", "external identities"),
		i.opts.MappingFile)

	if err := i.reportData(); err != nil {
		return err
	}
	return nil
}

// reportData prints the data totals and turns a failed record into a failure.
func (i *Importer) reportData() error {
	// A single device's totals can only restate the per-device lines above, so
	// they are left out. Across several devices they still add something.
	if i.opts.DataOnly && i.dataDevices <= 1 {
		return i.dataFailure()
	}

	if i.measurementsCreated > 0 || i.eventsCreated > 0 || i.alarmsCreated > 0 {
		line := fmt.Sprintf("Imported %d %s, %d %s and %d %s",
			i.measurementsCreated, plural(i.measurementsCreated, "measurement", "measurements"),
			i.eventsCreated, plural(i.eventsCreated, "event", "events"),
			i.alarmsCreated, plural(i.alarmsCreated, "alarm", "alarms"))
		if i.binariesCreated > 0 {
			line += fmt.Sprintf(", including %d %s",
				i.binariesCreated, plural(i.binariesCreated, "attachment", "attachments"))
		}
		fmt.Fprintln(i.stderr, line)
	}

	if i.identitiesFailed > 0 {
		fmt.Fprintf(i.stderr, "%d external identities could not be created\n", i.identitiesFailed)
	}
	return i.dataFailure()
}

// dataFailure is last, and it fails: everything else has already said what did
// land, and a caller that read a nil error here would treat a part-delivered
// device as finished.
func (i *Importer) dataFailure() error {
	if i.dataFailures > 0 {
		return fmt.Errorf("%d %s could not be imported, so this import is incomplete",
			i.dataFailures, plural(i.dataFailures, "record", "records"))
	}
	return nil
}

// importDataOnly adds data to devices that already exist in the target.
//
// This is what lets a large migration move data one device at a time instead of
// staging all of it first.
func (i *Importer) importDataOnly(ctx context.Context) error {
	entries, err := filepath.Glob(filepath.Join(i.opts.InputDirectory, "data", "*"))
	if err != nil {
		return err
	}

	for _, entry := range entries {
		info, statErr := os.Stat(entry)
		if statErr != nil || !info.IsDir() {
			continue
		}
		oldID := filepath.Base(entry)

		newID := i.mapping.NewID(oldID)
		if newID == "" {
			fmt.Fprintf(i.stderr, "Skipping data for (%s), it is not in the mapping\n", oldID)
			continue
		}
		name := i.mapping.Name(oldID)
		if name == "" {
			name = "device " + oldID
		}

		if err := i.importDeviceData(ctx, i.opts.InputDirectory, oldID, newID, name); err != nil {
			return err
		}
		i.dataDevices++
	}
	return nil
}

// ObjectFiles lists the exported objects in the order they will be created.
//
// Glob order, which is what commands/import walks, and therefore the order the
// mapping file records.
func ObjectFiles(inputDirectory string) ([]string, error) {
	return filepath.Glob(filepath.Join(inputDirectory, "exported_device_*.json"))
}

// SourceID recovers the source id from an exported object's file name.
func SourceID(path string) string {
	base := filepath.Base(path)
	base = strings.TrimPrefix(base, "exported_device_")
	return strings.TrimSuffix(base, ".json")
}

func (i *Importer) importDevices(ctx context.Context) error {
	files, err := ObjectFiles(i.opts.InputDirectory)
	if err != nil {
		return err
	}

	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			return err
		}

		oldID := SourceID(file)
		name := displayName(jsonraw.Name(body))

		// On a resume the mapping is the record of what already exists.
		// Anything listed there is left alone, which is what stops a resume
		// from creating a second copy of every object the first attempt made.
		if i.opts.Resume {
			if existing := i.mapping.NewID(oldID); existing != "" {
				fmt.Fprintf(i.stderr, "Already imported %s (%s -> %s)\n", name, oldID, existing)
				continue
			}
		}

		stripped, err := jsonraw.StripServerManaged(body)
		if err != nil {
			return err
		}
		if i.markerFragment != "" {
			if stripped, err = jsonraw.AddFragment(stripped, i.markerFragment); err != nil {
				return err
			}
		}

		result := i.client.ManagedObjects.Create(ctx, stripped)
		newID := result.Data.ID()
		if result.Err != nil || newID == "" {
			// The message stays the same whether the request failed or came
			// back without an id: either way there is nothing to map.
			fmt.Fprintf(i.stderr, "Failed to create a managed object for %s (%s)\n", name, oldID)
			if result.Err != nil {
				return fmt.Errorf("creating %s (%s): %w", name, oldID, result.Err)
			}
			return fmt.Errorf("creating %s (%s): the platform returned no id", name, oldID)
		}

		i.mapping.Add(oldID, newID, name)
		// After every object, so an interrupted import is revertable.
		if err := i.mapping.Write(); err != nil {
			return err
		}
		fmt.Fprintf(i.stderr, "Imported %s (%s -> %s)\n", name, oldID, newID)

		if err := i.importIdentities(ctx, oldID, newID, name); err != nil {
			return err
		}
		if err := i.importDeviceData(ctx, i.opts.InputDirectory, oldID, newID, name); err != nil {
			return err
		}
	}
	return nil
}

func (i *Importer) importIdentities(ctx context.Context, oldID, newID, name string) error {
	path := filepath.Join(i.opts.InputDirectory, oldID+"_identities.json")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	var identities []struct {
		Type       string `json:"type"`
		ExternalID string `json:"externalId"`
	}
	if err := jsonUnmarshal(raw, &identities); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	for _, external := range identities {
		result := i.client.Identity.Create(ctx, newID, identity.IdentityOptions{
			Type:       external.Type,
			ExternalID: external.ExternalID,
		})
		// StatusDuplicate as well as an error: the library's Identity.Create
		// calls IgnoreConflict, so a 409 arrives with a nil Err. Left alone,
		// that would report an external id the target tenant already knows as
		// freshly created - which is the opposite of what an operator needs to
		// hear, because a collision means the target already knows this device
		// under that id.
		if result.Err != nil || result.Status == op.StatusDuplicate {
			// Not fatal: it says nothing about the rest of the import.
			i.identitiesFailed++
			fmt.Fprintf(i.stderr,
				"Could not add external identity %s:%s to %s (%s), it may already exist in the target tenant\n",
				external.Type, external.ExternalID, name, newID)
			continue
		}
		i.identitiesCreated++
		fmt.Fprintf(i.stderr, "Added external identity %s:%s to %s (%s)\n",
			external.Type, external.ExternalID, name, newID)
	}
	return nil
}

// linkChildren rebuilds the relationships, once every object exists.
func (i *Importer) linkChildren(ctx context.Context) error {
	files, err := filepath.Glob(filepath.Join(i.opts.InputDirectory, "*_child_*.json"))
	if err != nil {
		return err
	}

	for _, file := range files {
		oldParentID, childType, ok := parseChildIndexName(file)
		if !ok {
			continue
		}

		newParentID := i.mapping.NewID(oldParentID)
		if newParentID == "" {
			fmt.Fprintf(i.stderr, "Skipping children of (%s), the parent was not part of the import\n",
				oldParentID)
			continue
		}
		parentName := i.mapping.Name(oldParentID)

		raw, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		var childIDs []string
		if err := jsonUnmarshal(raw, &childIDs); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}

		var assigned map[string]bool
		if i.opts.Resume {
			assigned, err = i.alreadyAssigned(ctx, newParentID, childType)
			if err != nil {
				return err
			}
		}

		for _, oldChildID := range childIDs {
			newChildID := i.mapping.NewID(oldChildID)
			if newChildID == "" {
				fmt.Fprintf(i.stderr,
					"Skipping child (%s) of %s (%s), the child was not part of the import\n",
					oldChildID, parentName, newParentID)
				continue
			}
			childName := i.mapping.Name(oldChildID)

			if assigned[newChildID] {
				fmt.Fprintf(i.stderr, "Already assigned %s (%s) as %s child of %s (%s)\n",
					childName, newChildID, childType, parentName, newParentID)
				continue
			}

			if err := i.assign(ctx, newParentID, childType, newChildID); err != nil {
				return err
			}
			fmt.Fprintf(i.stderr, "Assigned %s (%s) as %s child of %s (%s)\n",
				childName, newChildID, childType, parentName, newParentID)
		}
	}
	return nil
}

func (i *Importer) assign(ctx context.Context, parentID, childType, childID string) error {
	var err error
	switch childType {
	case "asset":
		err = i.client.ManagedObjects.ChildAssets.Assign(ctx, parentID, childID).Err
	case "device":
		err = i.client.ManagedObjects.ChildDevices.Assign(ctx, parentID, childID).Err
	case "addition":
		err = i.client.ManagedObjects.ChildAdditions.Assign(ctx, parentID, childID).Err
	default:
		return fmt.Errorf("unknown child type %q", childType)
	}
	if err != nil {
		return fmt.Errorf("assigning %s as %s child of %s: %w", childID, childType, parentID, err)
	}
	return nil
}

// parseChildIndexName splits "<parent>_child_<type>.json".
func parseChildIndexName(path string) (parent, childType string, ok bool) {
	base := strings.TrimSuffix(filepath.Base(path), ".json")
	parent, childType, ok = strings.Cut(base, "_child_")
	return parent, childType, ok
}

// getObject fetches one managed object, or reports that it is not there.
//
// Used by the same-tenant check, and returning the raw body rather than a field
// keeps the comparison in one place.
func (i *Importer) getObject(ctx context.Context, id string) ([]byte, bool) {
	result := i.client.ManagedObjects.Get(ctx, id, managedobjects.GetOptions{})
	if result.Err != nil || result.IsNotFound() {
		return nil, false
	}
	return result.Data.Bytes(), true
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
