package importer

import (
	"encoding/json"
	"fmt"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/jsonraw"
)

// DeviceUserPrefix is what the platform names a device's own user.
//
// The same convention the platform applies when it creates one, and the same
// string go-c8y's users.ByDeviceUser builds.
const DeviceUserPrefix = "device_"

// stampDeviceOwner sets a migrated object's owner to the device user that will
// eventually authenticate as it.
//
// Why an import would do this at all: a device authenticates with ROLE_DEVICE,
// and that role carries writes only together with **source-ownership**. A
// migrated object is owned by whoever imported it, so a device that arrives and
// binds to its own twin can *read* it and cannot write it - measured on a live
// tenant as 403 on updating the object and 403 on posting a measurement against
// it. The fleet looks migrated and silently sends nothing.
//
// Why it can be done before the device exists: `owner` accepts the name of a
// user the tenant does not have yet, on create as well as on update, and the
// platform lines the two up when that user is finally created. So the import
// stamps the name, and whatever creates the credential later - a device asking
// for one, or a certificate auto-registering - lands on an object already
// waiting for it. Verified 2026-09-08; see
// docs/platform-behaviour.md.
//
// Returns the body unchanged when there is no unambiguous device user to name,
// which is the ordinary answer for a group and for a device the export carried no
// identity for.
func stampDeviceOwner(body []byte, identities []exportedIdentity, identityType string) ([]byte, bool, error) {
	if !jsonraw.Has(body, "c8y_IsDevice") {
		// A group has no device user, and naming one would invent an owner that
		// nothing will ever be.
		return body, false, nil
	}

	externalID := selectExternalID(identities, identityType)
	if externalID == "" {
		return body, false, nil
	}

	owner, err := json.Marshal(DeviceUserPrefix + externalID)
	if err != nil {
		return nil, false, err
	}
	stamped, err := jsonraw.SetRaw(body, "owner", owner)
	if err != nil {
		return nil, false, fmt.Errorf("cannot stamp the device owner: %w", err)
	}
	return stamped, true, nil
}

// selectExternalID is the identity a device would authenticate as, or "" when
// there is no unambiguous choice.
//
// The same rule the registration phase applies, spelled out here rather than
// imported: the importer cannot depend on the package that provisions
// credentials, because it also has to import for migrations that provision none.
// Both are one line, and there is a test that they agree.
func selectExternalID(identities []exportedIdentity, identityType string) string {
	if identityType == "" {
		identityType = DefaultIdentityType
	}
	for _, identity := range identities {
		if identity.Type == identityType {
			return identity.ExternalID
		}
	}
	// One identity of some other type is unambiguous; several with none of the
	// wanted type is not, and guessing would name an owner the device never
	// becomes.
	if len(identities) == 1 {
		return identities[0].ExternalID
	}
	return ""
}

// DefaultIdentityType is the external identity a device is assumed to
// authenticate under.
//
// c8y_Serial, because that is what an agent presents: thin-edge uses it for
// device.id, certificate auto-registration looks it up, and bulk registration
// defaults to it.
const DefaultIdentityType = "c8y_Serial"
