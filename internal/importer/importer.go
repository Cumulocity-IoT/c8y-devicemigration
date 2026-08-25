package importer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

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

	// RunID, when set, becomes part of the marker fragment name.
	RunID string

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
	if i.opts.RunID != "" {
		i.markerFragment = MarkerPrefix + i.opts.RunID
	}

	if info, err := os.Stat(i.opts.InputDirectory); err != nil || !info.IsDir() {
		return fmt.Errorf("Input directory does not exist: %s", i.opts.InputDirectory)
	}

	// An empty mapping up front, so an immediate failure still leaves a file
	// rather than nothing at all.
	if err := i.mapping.Write(); err != nil {
		return err
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

	if i.identitiesFailed > 0 {
		fmt.Fprintf(i.stderr, "%d external identities could not be created\n", i.identitiesFailed)
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

		for _, oldChildID := range childIDs {
			newChildID := i.mapping.NewID(oldChildID)
			if newChildID == "" {
				fmt.Fprintf(i.stderr,
					"Skipping child (%s) of %s (%s), the child was not part of the import\n",
					oldChildID, parentName, newParentID)
				continue
			}
			childName := i.mapping.Name(oldChildID)

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

// Get is only used by the same-tenant check, and is here so that check can be
// tested without reaching into the client.
func (i *Importer) Get(ctx context.Context, id string) (string, bool) {
	result := i.client.ManagedObjects.Get(ctx, id, managedobjects.GetOptions{})
	if result.Err != nil {
		return "", false
	}
	return result.Data.Self(), true
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
