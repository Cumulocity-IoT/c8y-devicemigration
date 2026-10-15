// Package reverter deletes the managed objects an import created.
//
// It works in two phases: build a plan and print it, then delete. Nothing is
// deleted before the plan has been shown, because the plan is the only chance
// anyone gets to notice that it is wrong.
package reverter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/inventory/managedobjects"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/pagination"
)

// NoSourceID marks a plan entry with no known origin.
//
// A plan built from the run marker has none: the marker body is empty by design,
// so it says which run created an object but not which object it came from.
const NoSourceID = "-"

// Entry is one object the revert intends to delete.
type Entry struct {
	// SourceID is the id in the tenant the object came from, or NoSourceID.
	SourceID string

	// TargetID is the object to delete.
	TargetID string

	Name string

	// Present is whether the object was there when the plan was built. An
	// object that is already gone is reported and skipped rather than tried.
	Present bool

	// HasDeviceUser is whether this object is owned by a device user, and so
	// whether its delete should take that user with it.
	//
	// Read from the object's owner rather than assumed, because the flag that
	// does it is not harmless when it does not apply: a *device* with no device
	// user of its own answers withDeviceUser=true with a 409 and stays where it
	// is. Every object an import creates without provisioning credentials is
	// exactly that, so setting the flag unconditionally breaks the ordinary
	// revert while appearing to make it more thorough.
	HasDeviceUser bool
}

// Plan is what the revert intends to do.
type Plan struct {
	Entries []Entry
}

// PresentCount is how many objects will actually be deleted.
func (p *Plan) PresentCount() int {
	n := 0
	for _, entry := range p.Entries {
		if entry.Present {
			n++
		}
	}
	return n
}

// MissingCount is how many were already gone.
func (p *Plan) MissingCount() int { return len(p.Entries) - p.PresentCount() }

// Empty reports whether there is nothing to consider at all.
func (p *Plan) Empty() bool { return len(p.Entries) == 0 }

// ReadMapping loads an id mapping file, in the order it was written.
//
// Order matters because the plan reads in the order the import created things.
func ReadMapping(path string) (pairs [][2]string, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("Mapping file does not exist: %s", path)
	}

	notAnObject := fmt.Errorf("Mapping file is not a JSON object: %s", path)

	// Streamed rather than unmarshalled into a map, because a map would sort
	// the keys and lose the creation order.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil {
		return nil, notAnObject
	}
	if delim, ok := opening.(json.Delim); !ok || delim != '{' {
		return nil, notAnObject
	}

	for decoder.More() {
		// In an object, Token yields the key; Decode would try to read a value.
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, notAnObject
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, notAnObject
		}

		var value string
		if err := decoder.Decode(&value); err != nil {
			return nil, notAnObject
		}
		pairs = append(pairs, [2]string{key, value})
	}
	return pairs, nil
}

// BuildFromMapping looks up every object the mapping names.
//
// A failed lookup is the signal that an object is already gone, so it must not
// be silenced: that is how a revert tells an absent object from a present one.
func (r *Reverter) BuildFromMapping(ctx context.Context, path string) (*Plan, error) {
	pairs, err := ReadMapping(path)
	if err != nil {
		return nil, err
	}

	plan := &Plan{}
	for _, pair := range pairs {
		oldID, newID := pair[0], pair[1]

		result := r.client.ManagedObjects.Get(ctx, newID, managedobjects.GetOptions{})
		if result.Err != nil || result.IsNotFound() {
			plan.Entries = append(plan.Entries, Entry{
				SourceID: oldID, TargetID: newID, Name: "<not found>", Present: false,
			})
			continue
		}
		plan.Entries = append(plan.Entries, Entry{
			SourceID: oldID, TargetID: newID,
			Name: displayName(result.Data.Name()), Present: true,
			HasDeviceUser: ownedByDeviceUser(result.Data.Owner()),
		})
	}
	return plan, nil
}

// DeviceUserPrefix is what the platform names a device's own user.
//
// The same convention registration.DeviceUsername builds, and go-c8y's
// users.ByDeviceUser encodes. Repeated here rather than imported so the reverter
// does not depend on the package that provisions credentials - it has to undo
// migrations made by versions that had no such package at all.
const DeviceUserPrefix = "device_"

// ownedByDeviceUser reports whether an owner is a device's own user.
func ownedByDeviceUser(owner string) bool {
	return strings.HasPrefix(owner, DeviceUserPrefix)
}

// MarkerQuery is the inventory query that selects one run's objects.
//
// Expressed as a query rather than through the fragmentType parameter, and this
// is not a style choice: Cumulocity IGNORES fragmentType whenever a `query`
// parameter is present, and paging the inventory adds one - the library ANDs an
// "_id gt '<last>'" keyset into the query to walk pages in order. Setting
// fragmentType alongside it means the filter is silently dropped and the search
// returns the whole tenant, so a revert would plan to delete everything in it.
// Putting the condition inside the query is also what
// `c8y inventory find --fragmentType` does.
func MarkerQuery(fragment string) string {
	return "$filter=has(" + fragment + ")"
}

// BuildFromMarker asks the tenant which objects carry a run's marker fragment.
//
// One indexed query, because the run identity is in the fragment NAME rather
// than its body. There is no source id to report: the body is empty by design.
func (r *Reverter) BuildFromMarker(ctx context.Context, runID string) (*Plan, error) {
	fragment := MarkerPrefix + runID

	iterator := r.client.ManagedObjects.ListAll(ctx, managedobjects.ListOptions{
		Query:             MarkerQuery(fragment),
		PaginationOptions: pagination.PaginationOptions{PageSize: 2000},
	})

	plan := &Plan{}
	for object, err := range iterator.Items() {
		if err != nil {
			return nil, fmt.Errorf("cannot find objects carrying %s: %w", fragment, err)
		}
		// Found by asking the tenant, so it is present by construction.
		plan.Entries = append(plan.Entries, Entry{
			SourceID: NoSourceID, TargetID: object.ID(),
			Name: displayName(object.Name()), Present: true,
			HasDeviceUser: ownedByDeviceUser(object.Owner()),
		})
	}
	if err := iterator.Err(); err != nil {
		return nil, fmt.Errorf("cannot find objects carrying %s: %w", fragment, err)
	}
	return plan, nil
}
