package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/inventory/managedobjects"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/inventory/managedobjects/childadditions"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/inventory/managedobjects/childassets"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/inventory/managedobjects/childdevices"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/pagination"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/jsonmodels"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/jsonraw"
)

// MarkerQuery is the inventory query that selects one run's objects.
//
// Expressed as a query, never as the fragmentType parameter: Cumulocity ignores
// fragmentType whenever a query parameter is present, and paging the inventory
// adds one. See docs/go-port/BEHAVIOUR-CONTRACT.md - getting this wrong once
// produced a plan to delete 811 objects for a nine-object import.
func MarkerQuery(fragment string) string {
	return "$filter=has(" + fragment + ")"
}

// LoadMapping reads an existing mapping in the order it was written, and fills
// in the names from the export so progress lines say "Line A" rather than
// "device 2".
func (i *Importer) LoadMapping(path, inputDirectory string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("--resume needs an existing mapping file, and %s cannot be read", path)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("Mapping file is not a JSON object: %s", path)
	}
	if delim, ok := opening.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("Mapping file is not a JSON object: %s", path)
	}

	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("Mapping file is not a JSON object: %s", path)
		}
		oldID, ok := keyToken.(string)
		if !ok {
			return fmt.Errorf("Mapping file is not a JSON object: %s", path)
		}
		var newID string
		if err := decoder.Decode(&newID); err != nil {
			return fmt.Errorf("Mapping file is not a JSON object: %s", path)
		}

		// The mapping records ids only, so the name comes from the export.
		name := ""
		objectPath := filepath.Join(inputDirectory, "exported_device_"+oldID+".json")
		if body, readErr := os.ReadFile(objectPath); readErr == nil {
			name = displayName(jsonraw.Name(body))
		}
		i.mapping.Add(oldID, newID, name)
	}
	return nil
}

// PruneDeletedFromMapping drops mapping entries whose objects are gone.
//
// A resume trusts the mapping for what already exists, so an object deleted in
// the meantime would be skipped and never recreated. The run marker is what
// makes checking cheap: one indexed query returns everything the run still has,
// and anything in the mapping outside that set is stale.
//
// With no marker - an import run with --noFlag - there is nothing to check
// against and the mapping is taken at its word.
func (i *Importer) PruneDeletedFromMapping(ctx context.Context) error {
	if i.markerFragment == "" {
		return nil
	}

	live := map[string]bool{}
	iterator := i.client.ManagedObjects.ListAll(ctx, managedobjects.ListOptions{
		Query:             MarkerQuery(i.markerFragment),
		PaginationOptions: pagination.PaginationOptions{PageSize: 2000},
	})
	for object, err := range iterator.Items() {
		if err != nil {
			return fmt.Errorf("cannot find objects carrying %s: %w", i.markerFragment, err)
		}
		live[object.ID()] = true
	}
	if err := iterator.Err(); err != nil {
		return fmt.Errorf("cannot find objects carrying %s: %w", i.markerFragment, err)
	}

	dropped := i.mapping.Retain(func(oldID, newID, name string) bool {
		if live[newID] {
			return true
		}
		fmt.Fprintf(i.stderr, "%s (%s) is gone from the target, it will be created again\n",
			name, newID)
		return false
	})

	if dropped > 0 {
		return i.mapping.Write()
	}
	return nil
}

// alreadyAssigned reports which children a parent already has of one type, so a
// resume assigns only what is missing.
//
// Asked rather than assumed: this avoids depending on how Cumulocity answers a
// duplicate assignment, which is not something to guess at. The library happens
// to ignore a 409, but relying on that would leave the progress output claiming
// it assigned relationships that already existed.
func (i *Importer) alreadyAssigned(ctx context.Context, parentID, childType string) (map[string]bool, error) {
	assigned := map[string]bool{}
	paging := pagination.PaginationOptions{PageSize: 2000}

	var iterator *pagination.Iterator[jsonmodels.ManagedObject]
	switch childType {
	case "asset":
		iterator = i.client.ManagedObjects.ChildAssets.ListAll(ctx, parentID,
			childassets.ListOptions{PaginationOptions: paging})
	case "device":
		iterator = i.client.ManagedObjects.ChildDevices.ListAll(ctx, parentID,
			childdevices.ListOptions{PaginationOptions: paging})
	case "addition":
		iterator = i.client.ManagedObjects.ChildAdditions.ListAll(ctx, parentID,
			childadditions.ListOptions{PaginationOptions: paging})
	default:
		return nil, fmt.Errorf("unknown child type %q", childType)
	}

	for child, err := range iterator.Items() {
		if err != nil {
			return nil, fmt.Errorf("cannot list %s children of %s: %w", childType, parentID, err)
		}
		assigned[child.ID()] = true
	}
	if err := iterator.Err(); err != nil {
		return nil, fmt.Errorf("cannot list %s children of %s: %w", childType, parentID, err)
	}
	return assigned, nil
}
