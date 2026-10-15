package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/identity"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/inventory/managedobjects"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/importer"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/jsonraw"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/registration"
)

// What a migrated device needs to know, answered to the device itself.
//
// A migrated device has two questions - where its new tenant is, and who it now
// is - and this endpoint answers both in one call, because a device that has to
// ask two services two questions to move has two ways to end up half-migrated.
//
// It is the one place in this service where a caller from outside the owner
// tenant is accepted, and the whole of that exception is here: see
// AuthorizeDevice.

// IssuedFragment records on the target object that a credential has been handed
// over for it.
//
// The fragment, never the secret. It exists for correctness rather than audit: a
// thin-edge device asks every few minutes, so an endpoint that minted a fresh
// password on every call would rotate a device's own credential forever and never
// let it settle.
//
// What it therefore bounds is how often a credential is issued, not to whom. It
// lives on an object owned by the device user it was issued for - which is what
// this route arranges, so that an arriving device can write its own twin - so
// anything able to write that object can clear it and have a second credential
// minted. An administrator of the target tenant doing exactly that is the
// documented way to re-issue. Recorded here because "handed over once" reads as a
// stronger statement than this fragment can make; see docs/microservice.md.
const IssuedFragment = "c8y_DeviceMigrationCredentialsIssued"

// DeviceMigrationResponse is what the calling device is told.
type DeviceMigrationResponse struct {
	// Migrate is false when this device has nothing to do - no succeeded
	// migration carried it. Everything else is then empty, and the device should
	// stay where it is.
	Migrate bool `json:"migrate"`

	// Host and Tenant are where to reconnect. Tenant matters because a
	// basic-auth device authenticates as "<tenant>/<username>".
	Host   string `json:"host,omitempty"`
	Tenant string `json:"tenant,omitempty"`

	Username string `json:"username,omitempty"`

	// Password is present only when this call is what issued it. There is no
	// second chance: it is generated here and stored nowhere.
	Password string `json:"password,omitempty"`

	// AlreadyIssued is true when a credential was handed over for this device
	// before, so this call did not mint another one.
	//
	// The endpoint still answers where to go, because that is what a device
	// retrying a failed move needs on every attempt. Only the credential is
	// once.
	//
	// It says a credential was issued for this device, not that this caller
	// received it, and nothing here can tell the two apart - there is no record
	// of who collected, by decision. A device that lost its credential and one
	// that never had it see the same answer, and the way forward is the same for
	// both: the Message says it.
	AlreadyIssued bool `json:"alreadyIssued,omitempty"`

	// RunID is the migration that carried this device, for diagnosis.
	RunID string `json:"runId,omitempty"`

	// Message is for a human reading a device's log.
	Message string `json:"message,omitempty"`
}

// DeviceCaller is a verified device.
type DeviceCaller struct {
	Tenant   string
	Username string

	// ExternalID is the identity this device authenticates as, taken from the
	// username rather than from the request.
	ExternalID string
}

// AuthorizeDevice establishes that the caller is a device of a subscribed tenant.
//
// The deliberate exception to the rule Authorize applies everywhere else, and it
// is narrow on purpose: one endpoint, device users, no parameters. What makes it
// safe enough to own:
//
//   - The identity comes from the platform, not the request. The device declares
//     nothing, so there is nothing to tamper with - and in particular no device
//     id to substitute, which is what the API-key-and-self-declared-id shape this
//     replaces could not prevent.
//   - The external id is read out of the username. Nothing here consults an
//     object's `owner`, which is writable by anyone who administers the tenant and
//     is therefore no evidence at all.
//
// What this establishes is that the caller holds the device user named
// device_<externalId> in a subscribed tenant. What it cannot establish is that
// the holder is the physical device, and the limits of that are recorded rather
// than mitigated:
//
//   - `device_` is the platform's naming convention, not a protected namespace,
//     and the platform does not restrict who may hold such a name within a
//     tenant. Where the user already exists and a device is using it, taking it
//     over means deleting it or resetting its password, which stops that device
//     authenticating and is conspicuous. Where it does *not* exist the name is
//     simply free - and it does not exist for every migrated device, because a
//     child device behind a gateway authenticates as the gateway, and a device
//     registered by hand may hold a username of any shape.
//   - ROLE_DEVICE is required as well, and a tenant administrator can grant it.
//     A bar rather than a boundary - it costs nothing and it means two deliberate
//     steps instead of one.
//
// Together those put the trust for a migration that opted into this route in the
// source tenant's user administration, for as long as credentials are
// outstanding. That is the decision the per-migration flag exists to make
// deliberate; see docs/microservice.md.
//
// The consequence for devices with no device user of their own is a functional
// one and it is the more likely to be met: they cannot collect at all, and the
// endpoint tells them there is nothing to do.
func (s *Service) AuthorizeDevice(ctx context.Context, r *http.Request) (*DeviceCaller, error) {
	creds, ok := credentialsFrom(r)
	if !ok {
		return nil, fmt.Errorf(
			"%w: no credentials on the request. A device authenticates with the "+
				"credentials it already has for its own tenant", ErrUnauthenticated)
	}
	if creds.tenant == "" {
		return nil, fmt.Errorf(
			"%w: the credentials name no tenant, so authenticate as tenant/username",
			ErrUnauthenticated)
	}
	if err := checkXSRF(r, creds); err != nil {
		return nil, err
	}

	// Proven through the owner tenant's client with the caller's credentials
	// overridden per request, exactly as Authorize does - this needs no client
	// of the caller's own tenant, and would work for a tenant this service
	// cannot otherwise reach.
	owner, err := s.tenant(s.ownerTenant)
	if err != nil {
		return nil, err
	}
	verify := api.WithAuth(ctx, creds.auth)
	if creds.tfaToken != "" {
		verify = withTFAToken(verify, creds.tfaToken)
	}
	result := owner.client.Users.CurrentUser.Get(verify)
	if result.Err != nil {
		if result.HTTPStatus == http.StatusUnauthorized || result.HTTPStatus == http.StatusForbidden {
			return nil, fmt.Errorf("%w: the platform rejected the credentials", ErrUnauthenticated)
		}
		return nil, fmt.Errorf("could not verify the caller: %w", result.Err)
	}

	externalID, isDevice := strings.CutPrefix(creds.username, importer.DeviceUserPrefix)
	if !isDevice || externalID == "" {
		return nil, &ForbiddenError{Reason: fmt.Sprintf(
			"%s is not a device user, and this endpoint answers only to the device "+
				"it is about. A migration is started by a person through POST /migrations",
			creds.username)}
	}
	if !hasRole(result.Data.Bytes(), "ROLE_DEVICE") {
		return nil, &ForbiddenError{Reason: fmt.Sprintf(
			"%s does not hold ROLE_DEVICE", creds.username)}
	}

	return &DeviceCaller{
		Tenant:     creds.tenant,
		Username:   creds.username,
		ExternalID: externalID,
	}, nil
}

func (s *Service) handleDeviceSelfMigration(w http.ResponseWriter, r *http.Request) {
	caller, err := s.AuthorizeDevice(r.Context(), r)
	if err != nil {
		writeError(w, r, err)
		return
	}

	// After the caller is proven, so the allowance belongs to a verified identity
	// rather than to whatever name a request put forward - otherwise one caller
	// could spend a device's allowance by claiming to be it. What it guards is
	// the search below, which costs this service platform calls in other tenants
	// under its own service users.
	if !s.deviceAsks.allow(caller.Tenant + "/" + caller.Username) {
		// Retry-After in seconds, because the caller is a device and the useful
		// thing to tell it is when to come back rather than why.
		w.Header().Set("Retry-After", strconv.Itoa(int(deviceAskInterval.Seconds())))
		writeError(w, r, &RateLimitedError{Reason: fmt.Sprintf(
			"%s is asking more often than a device needs to. The answer to this call does "+
				"not change between asks: wait %s and ask again",
			caller.Username, deviceAskInterval)})
		return
	}

	response, err := s.answerDevice(r.Context(), caller)
	if err != nil {
		writeError(w, r, err)
		return
	}
	// No-store, because the body can carry a working device password and must
	// not sit in a proxy after the one call that produced it.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r, http.StatusOK, response)
}

// answerDevice works out where a device should go and who it should be.
func (s *Service) answerDevice(ctx context.Context, caller *DeviceCaller) (*DeviceMigrationResponse, error) {
	migration, target, err := s.findMigrationOf(ctx, caller)
	if err != nil {
		return nil, err
	}
	if migration == nil {
		// Not an error. A device asking on a timer wants "nothing to do" as an
		// ordinary answer, not something its log will report as a failure.
		return &DeviceMigrationResponse{
			Migrate: false,
			Message: "no succeeded migration carried this device with device self-service " +
				"enabled, so stay where you are. A migration that provisioned credentials " +
				"for an operator to distribute is deliberately not collectable here",
		}, nil
	}

	if jsonraw.Has(target.body, IssuedFragment) {
		// Where to go, but no second password. A device retrying a move it
		// failed needs the endpoint on every attempt; the credential is once,
		// or a device that cannot keep it would rotate its own password forever.
		return &DeviceMigrationResponse{
			Migrate:       true,
			Host:          target.host,
			Tenant:        migration.Request.TargetTenant,
			Username:      registration.DeviceUsername(caller.ExternalID),
			AlreadyIssued: true,
			RunID:         migration.RunID,
			Message: "credentials for this device were already issued once and are not " +
				"stored, so they cannot be handed over again. To re-issue them, an " +
				"administrator removes the " + IssuedFragment + " fragment from managed " +
				"object " + target.id + " in " + migration.Request.TargetTenant,
		}, nil
	}

	credential, err := s.issueFor(ctx, caller, migration, target)
	if err != nil {
		return nil, err
	}
	return &DeviceMigrationResponse{
		Migrate:  true,
		Host:     target.host,
		Tenant:   credential.Tenant,
		Username: credential.Username,
		Password: credential.Password,
		RunID:    migration.RunID,
		Message:  "reconnect to this host and authenticate as tenant/username",
	}, nil
}

// targetObject is the migrated copy of the calling device.
type targetObject struct {
	id   string
	body []byte
	host string
}

// findMigrationOf looks for the succeeded migration that carried this device.
//
// The chain is: the caller's own tenant, the job records of migrations out of
// that tenant, and then this device's external identity in each of their target
// tenants. The object found there must carry that run's marker, which is what
// makes it *this migration's* copy rather than something the target happened to
// know under the same id.
//
// Newest first, so a device migrated more than once is told about the most recent
// move rather than an old one.
func (s *Service) findMigrationOf(
	ctx context.Context, caller *DeviceCaller) (*Job, *targetObject, error) {

	// Which migrations could answer for a device of this tenant at all, derived
	// once and held briefly rather than re-read per device: every condition it
	// turns on is terminal, so the answer cannot go stale while a device is
	// asking. See devicelimits.go.
	jobs, err := s.collectableFor(ctx, caller.Tenant)
	if err != nil {
		return nil, nil, err
	}
	if len(jobs) == 0 {
		// Answered without touching another tenant. A device whose tenant has
		// nothing to collect is the common case once a fleet has moved, and it
		// should not cost an identity lookup and an object read per migration to
		// be told so.
		return nil, nil, nil
	}

	for _, job := range jobs {
		target, err := s.tenant(job.Request.TargetTenant)
		if err != nil {
			// A target this service can no longer reach cannot be offered to a
			// device. Skipped rather than fatal: an older job should not stop a
			// newer one being found.
			continue
		}

		resolved := target.client.Identity.Get(ctx, identity.IdentityOptions{
			Type:       registration.DefaultIdentityType,
			ExternalID: caller.ExternalID,
		})
		if resolved.Err != nil {
			continue
		}
		objectID := resolved.Data.ManagedObjectID()
		if objectID == "" {
			continue
		}

		object := target.client.ManagedObjects.Get(ctx, objectID, managedobjects.GetOptions{})
		if object.Err != nil {
			continue
		}
		body := []byte(object.Data.Raw())
		if !jsonraw.Has(body, importer.MarkerPrefix+job.RunID) {
			continue
		}

		return job, &targetObject{
			id:   objectID,
			body: body,
			host: s.hostOf(ctx, job.Request.TargetTenant),
		}, nil
	}
	return nil, nil, nil
}

// hostOf is the hostname a device should connect to for a tenant.
//
// The tenant's own domain when the platform reports one, because that is what a
// device puts in its configuration. Falls back to this service's base URL host,
// which is the platform every subscribed tenant shares.
func (s *Service) hostOf(ctx context.Context, tenant string) string {
	for _, described := range s.TenantDescriptions(ctx) {
		if described.ID == tenant && described.Domain != "" {
			return described.Domain
		}
	}
	return hostFromURL(s.baseURL)
}

func hostFromURL(raw string) string {
	host := strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
	return strings.TrimSuffix(strings.SplitN(host, "/", 2)[0], "/")
}

// issueFor mints this device's credential and records that it did.
//
// Minted here rather than during the migration, which is what lets this service
// hold nothing: it has no persistent storage and no secret store, and a fleet's
// passwords would otherwise have to survive from the migration until every device
// next asked - days, in the ordinary case.
//
// The object it lands on was made ready at import time, its owner already naming
// the user this call creates. Without that the device would bind to its twin and
// be unable to write it, because ROLE_DEVICE carries writes only together with
// ownership.
func (s *Service) issueFor(ctx context.Context, caller *DeviceCaller,
	migration *Job, target *targetObject) (*registration.Credential, error) {

	client, err := s.tenant(migration.Request.TargetTenant)
	if err != nil {
		return nil, err
	}

	password, err := registration.GeneratePassword()
	if err != nil {
		return nil, err
	}
	record := registration.Record{
		ExternalID:   caller.ExternalID,
		ExternalType: registration.DefaultIdentityType,
		Name:         jsonraw.Name(target.body),
		Type:         jsonraw.String(target.body, "type"),
		Password:     password,
	}

	// No marker column: the object exists already and carries this run's marker
	// from the import. Registration is here only to create the user.
	outcome, err := registration.Register(ctx, client.client, "", []registration.Record{record})
	if err != nil {
		return nil, err
	}
	if reason, refused := outcome.Refused(caller.ExternalID); refused {
		// The platform's own words, uninterpreted - this endpoint reports causes
		// that have nothing to do with passwords as "Error during password
		// validation."
		return nil, fmt.Errorf("the target tenant refused to register %s: %s",
			caller.ExternalID, reason)
	}

	// Recorded before the password is returned. A credential handed over without
	// the record would be re-issued on the device's next tick, rotating the one
	// it has just been given.
	if err := s.recordIssued(ctx, client, target.id); err != nil {
		return nil, err
	}

	return &registration.Credential{
		ExternalID: caller.ExternalID,
		Tenant:     migration.Request.TargetTenant,
		Username:   registration.DeviceUsername(caller.ExternalID),
		Password:   password,
	}, nil
}

// recordIssued stamps the issuance fragment onto the target object.
func (s *Service) recordIssued(ctx context.Context, client *tenantClient, objectID string) error {
	body, err := json.Marshal(map[string]any{
		IssuedFragment: map[string]string{"issuedAt": time.Now().UTC().Format(time.RFC3339)},
	})
	if err != nil {
		return err
	}
	result := client.client.ManagedObjects.Update(ctx, objectID, body)
	if result.Err != nil {
		return fmt.Errorf("could not record that credentials were issued for %s: %w",
			objectID, result.Err)
	}
	return nil
}
