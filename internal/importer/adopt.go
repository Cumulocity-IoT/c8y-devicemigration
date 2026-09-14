package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/identity"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/inventory/managedobjects"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/jsonraw"
)

// PhaseImported is what the marker carries once the import has filled an
// adopted object in.
//
// The registration phase writes "registered". An object still reading that is
// one a run created and never got to, which is the only record of a run that
// died between the two phases: the mapping cannot say so, because a target id
// exists from registration onwards, and its format is fixed by the shell
// implementation's.
const PhaseImported = "imported"

// phaseField is the marker's only subfield.
const phaseField = "phase"

// adopted is an object a registration phase created for this run.
type adopted struct {
	// TargetID is the object to update instead of creating one.
	TargetID string

	// ExternalID and ExternalType are the identity that found it. Recorded so
	// importIdentities can skip creating the one that already exists - the
	// registration made it, and re-creating it would report a collision for
	// something that is working exactly as intended.
	ExternalID   string
	ExternalType string
}

// findAdopted looks for an object this run's registration created for a source
// object.
//
// Returns nil when there is nothing to adopt, which is the ordinary answer for a
// group, for a device the export carried no identity for, and for every object
// when no registration ran.
//
// Two gates, and the second is the one that matters. The option says the caller
// expects a registration to have run, which is what buys the identity lookup a
// request per device. The **marker** is what makes adoption safe: the resolved
// object must carry this run's fragment, so an update can only ever land on an
// object this run's registration created. Without that check an import would
// silently overwrite whatever the target happened to already know under the same
// external id, and "existing objects in the target tenant are never matched or
// updated" is a documented guarantee, not an implementation detail.
func (i *Importer) findAdopted(ctx context.Context, sourceID string) (*adopted, error) {
	if !i.opts.AdoptRegistered || i.markerFragment == "" {
		return nil, nil
	}

	identities, err := readIdentityArtefact(i.opts.InputDirectory, sourceID)
	if err != nil {
		return nil, err
	}

	for _, external := range identities {
		result := i.client.Identity.Get(ctx, identity.IdentityOptions{
			Type:       external.Type,
			ExternalID: external.ExternalID,
		})
		if result.Err != nil {
			// A 404 is the common case and says only that this identity is not
			// in the target - so try the next one, and fall through to creating
			// the object if none of them resolve. A transport failure looks the
			// same here on purpose: adopting is an optimisation over creating,
			// and creating is always safe.
			continue
		}
		targetID := result.Data.ManagedObjectID()
		if targetID == "" {
			continue
		}

		carries, err := i.carriesRunMarker(ctx, targetID)
		if err != nil {
			return nil, err
		}
		if !carries {
			// The target already knew this external id, but not from this run.
			// Left alone: the import creates its own object and the identity
			// create then reports the collision, exactly as it did before any
			// of this existed.
			fmt.Fprintf(i.stderr,
				"External identity %s:%s already exists in the target on object %s, "+
					"which this run did not create - importing %s as a new object\n",
				external.Type, external.ExternalID, targetID, sourceID)
			return nil, nil
		}

		return &adopted{
			TargetID:     targetID,
			ExternalID:   external.ExternalID,
			ExternalType: external.Type,
		}, nil
	}
	return nil, nil
}

// carriesRunMarker reports whether an object carries this run's marker fragment.
func (i *Importer) carriesRunMarker(ctx context.Context, targetID string) (bool, error) {
	result := i.client.ManagedObjects.Get(ctx, targetID, managedobjects.GetOptions{})
	if result.Err != nil {
		return false, fmt.Errorf("cannot read %s to check whether this run created it: %w",
			targetID, result.Err)
	}
	return jsonraw.Has([]byte(result.Data.Raw()), i.markerFragment), nil
}

// updateAdopted fills a registered object in with the exported content.
//
// A PUT, and the platform merges rather than replaces - which is what makes this
// work at all. The object arrives from registration owned by its own device user,
// and `owner` is among the server-managed fields StripServerManaged removes, so
// the update cannot hand the device's twin back to the service user. That strip
// list was incidental before register-first; it is load-bearing now.
func (i *Importer) updateAdopted(ctx context.Context, target *adopted, body []byte, sourceID, name string) error {
	stripped, err := jsonraw.StripServerManaged(body)
	if err != nil {
		return err
	}
	// Overwrites the "registered" the registration phase wrote, so the marker
	// says which phase last touched this object.
	stripped, err = jsonraw.SetRaw(stripped, i.markerFragment, phaseMarker(PhaseImported))
	if err != nil {
		return err
	}

	result := i.client.ManagedObjects.Update(ctx, target.TargetID, stripped)
	if result.Err != nil {
		return fmt.Errorf("updating the registered object for %s (%s): %w", name, sourceID, result.Err)
	}

	i.adoptedCount++
	i.mapping.Add(sourceID, target.TargetID, name)
	// Written after every object, so an import that dies part way through still
	// leaves a complete record - the same reason the create path does it.
	if err := i.mapping.Write(); err != nil {
		return err
	}
	fmt.Fprintf(i.stderr, "Updated the registered object for %s (%s -> %s)\n",
		name, sourceID, target.TargetID)
	return nil
}

// phaseMarker is the marker fragment's body.
//
// An object rather than a bare string, because the registration file can only
// produce one through a dotted header and the two have to be the same shape - a
// reader comparing "registered" with "imported" should not have to care which
// phase wrote it. MarkerQuery uses has(), so neither form affects finding it.
func phaseMarker(phase string) []byte {
	body, err := json.Marshal(map[string]string{phaseField: phase})
	if err != nil {
		// Marshalling a map of two constants cannot fail.
		panic(err)
	}
	return body
}

// readIdentityArtefact reads the export's identity file for one object.
//
// An absent file means the object has no external identities: the export omits
// the file rather than writing an empty list, so os.ErrNotExist is an answer.
func readIdentityArtefact(inputDirectory, sourceID string) ([]exportedIdentity, error) {
	path := filepath.Join(inputDirectory, sourceID+"_identities.json")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var identities []exportedIdentity
	if err := jsonUnmarshal(raw, &identities); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return identities, nil
}

// exportedIdentity is one entry of an identity artefact.
type exportedIdentity struct {
	Type       string `json:"type"`
	ExternalID string `json:"externalId"`
}
