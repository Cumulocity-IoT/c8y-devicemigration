package registration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/core"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/jsonmodels"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/export"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/importer"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/jsonraw"
)

// DefaultIdentityType is the external identity a device is registered under
// unless it has none of that type.
//
// c8y_Serial is what agents conventionally register themselves with - thin-edge
// uses it for device.id, and it is what certificate auto-registration looks up -
// so it is the identity a device is most likely to present when it reconnects.
const DefaultIdentityType = "c8y_Serial"

// Options is the resolved form of the registration phase.
type Options struct {
	// InputDirectory is the export, read for the same artefacts the import
	// reads: which objects there are, what they are called, and which external
	// identities each one carries.
	InputDirectory string

	// Marker is the run's fragment name, or empty for no marker. The importer
	// builds it as MarkerPrefix + RunID; registration only ever writes it.
	Marker string

	// IdentityType overrides DefaultIdentityType.
	IdentityType string

	// Tenant is the name the credentials file reports, and it is supplied rather
	// than looked up: whoever built the client has already proven it against
	// /tenant/currentTenant, and a second call could disagree with the first
	// without anybody noticing which one the file used.
	Tenant string
}

// Skipped is one exported object registration did not cover, with the reason.
//
// Never silent. A device that gets no credential is a device that will not
// reconnect, and the operator has to hear about it while they can still act -
// not discover it when the fleet does not arrive.
type Skipped struct {
	SourceID string
	Name     string
	Reason   string
}

// Plan is what a registration would do, before anything is generated or sent.
type Plan struct {
	Records []Record
	Skipped []Skipped

	// Conflicts is the devices whose external identity the target tenant already
	// holds. Any at all refuses the run - see conflict.go.
	Conflicts []Conflict
}

// FailedRow is one row the platform refused.
type FailedRow struct {
	ExternalID string
	Reason     string
}

// PartialError reports rows the platform refused.
//
// A type rather than prose because the caller has to do two things at once: hand
// over the credentials that did work, and still fail. Following the import's rule
// for refused data records - report it, count it, carry on, exit non-zero -
// because a registration that silently covered ninety of a hundred devices is
// the worst of both.
type PartialError struct {
	Failed     []FailedRow
	Registered int
}

func (e *PartialError) Error() string {
	return fmt.Sprintf("%d of %d devices were not registered and have no credentials",
		len(e.Failed), len(e.Failed)+e.Registered)
}

// Registrar bulk-registers the devices of an export in the target tenant.
type Registrar struct {
	client *api.Client
	stderr io.Writer
	opts   Options

	rotated int
}

func New(client *api.Client, stderr io.Writer, opts Options) *Registrar {
	if opts.IdentityType == "" {
		opts.IdentityType = DefaultIdentityType
	}
	return &Registrar{client: client, stderr: stderr, opts: opts}
}

// Plan reads the export and decides which devices can be registered.
//
// No passwords and no requests: a caller can print this and ask before anything
// exists in the target tenant, and the answer does not change when it then runs.
// Which identity a device would be registered under is a rule about the export
// alone, and keeping it that way is what lets it be tested - and compared
// against the importer's copy of it - without a platform.
//
// What the *target* already holds is a separate question, asked by Check.
func (r *Registrar) Plan() (*Plan, error) {
	files, err := importer.ObjectFiles(r.opts.InputDirectory)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("No exported objects in %s", r.opts.InputDirectory)
	}

	plan := &Plan{}
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		sourceID := importer.SourceID(file)
		name := jsonraw.Name(body)

		// Only devices. A group or an asset has no device user to create, and
		// registering one would invent a credential for something that cannot
		// authenticate - then leave it behind, because a revert's withDeviceUser
		// only removes the user of an object the platform considers a device.
		if !jsonraw.Has(body, "c8y_IsDevice") {
			plan.Skipped = append(plan.Skipped, Skipped{sourceID, name,
				"not a device, so it has no device user"})
			continue
		}

		identities, err := readIdentities(r.opts.InputDirectory, sourceID)
		if err != nil {
			return nil, err
		}
		chosen, reason := chooseIdentity(identities, r.opts.IdentityType)
		if chosen == nil {
			plan.Skipped = append(plan.Skipped, Skipped{sourceID, name, reason})
			continue
		}

		plan.Records = append(plan.Records, Record{
			SourceID:     sourceID,
			ExternalID:   chosen.ExternalID,
			ExternalType: chosen.Type,
			Name:         name,
			Type:         jsonraw.String(body, "type"),
		})
	}
	return plan, nil
}

// Check is Plan with every chosen identity resolved against the target.
//
// The one call both the run and the dry run make, so that what `--dry-run`
// reports and what a registration refuses cannot come apart. One request per
// registrable device, and nothing is created by any of them.
//
// This cannot be deferred into the run itself: an identity the target already
// holds is the case where registering reaches a device the migration is not
// about, and the only moment to say so is before a password has been generated
// for anything.
func (r *Registrar) Check(ctx context.Context) (*Plan, error) {
	plan, err := r.Plan()
	if err != nil {
		return nil, err
	}
	conflicts, err := FindConflicts(ctx, r.client, plan.Records)
	if err != nil {
		return nil, err
	}
	plan.Conflicts = conflicts
	return plan, nil
}

// Run registers every device the plan covers and returns their credentials.
//
// The credentials are returned even when some rows failed, alongside a
// *PartialError: they exist only in this response, and dropping the ones that
// worked because others did not would mean re-registering - which rotates the
// passwords of the devices that were fine.
func (r *Registrar) Run(ctx context.Context) ([]Credential, error) {
	plan, err := r.Check(ctx)
	if err != nil {
		return nil, err
	}
	for _, skipped := range plan.Skipped {
		fmt.Fprintf(r.stderr, "Not registering %s (%s): %s\n",
			displayName(skipped.Name), skipped.SourceID, skipped.Reason)
	}

	// Before any password is generated and before anything is sent, so a refusal
	// costs the target tenant nothing at all.
	if len(plan.Conflicts) > 0 {
		ReportConflicts(r.stderr, plan.Conflicts)
		return nil, &ConflictError{Conflicts: plan.Conflicts}
	}

	if len(plan.Records) == 0 {
		return nil, fmt.Errorf(
			"None of the %d exported objects can be registered, so no credentials can be issued",
			len(plan.Skipped))
	}

	// Generated up front, so a failure part way through the fleet cannot leave
	// half a file of passwords that were never sent.
	for index := range plan.Records {
		password, err := GeneratePassword()
		if err != nil {
			return nil, err
		}
		plan.Records[index].Password = password
	}

	fmt.Fprintf(r.stderr, "Registering %d %s in %s\n",
		len(plan.Records), plural(len(plan.Records), "device", "devices"), r.opts.Tenant)

	outcome, err := Register(ctx, r.client, r.opts.Marker, plan.Records)
	if err != nil {
		return nil, err
	}
	return r.collect(plan.Records, outcome)
}

// Register uploads a registration file and returns what the platform said.
//
// Separate from the Registrar because a registration does not always come from
// an export directory: Option B mints one device's credential when that device
// asks for it, long after the migration, and needs exactly this call with one
// record and no plan.
//
// The passwords are expected to be set on the records already - Registrar.Run
// generates them, and a single-record caller generates its own - so this
// function does not touch them. It writes the file to memory and never to disk.
func Register(ctx context.Context, client *api.Client, marker string, records []Record) (Outcome, error) {
	var file bytes.Buffer
	if err := WriteRegistrationFile(&file, marker, records); err != nil {
		return Outcome{}, err
	}

	// Name and ContentType are what the endpoint sees; the reader is the file
	// built above rather than a path, so nothing writes a list of passwords to
	// disk on the way.
	result := client.Devices.Registration.CreateBulk(ctx, core.UploadFileOptions{
		Reader:      bytes.NewReader(file.Bytes()),
		Name:        "devices.csv",
		ContentType: "text/csv",
	})
	if result.Err != nil {
		return Outcome{}, fmt.Errorf("bulk registration failed: %w", result.Err)
	}
	return newOutcome(result.Data), nil
}

// Outcome is the platform's answer reduced to what this package reads.
//
// Reduced at the boundary rather than passed through, for two reasons: it makes
// collect a pure function a test can drive without a server, and go-c8y's
// BulkNewDeviceRequestDetails.Status() reads "BulkNewDeviceStatus" where the
// platform sends "bulkNewDeviceStatus", so that accessor always answers empty.
// Nothing here depends on it - which list an entry arrives in is the same
// information - but the next reader should not have to rediscover that.
type Outcome struct {
	// failed maps external id to the platform's stated reason.
	failed map[string]string

	// rotated is the external ids the target already knew, which came back as
	// CREDENTIAL_UPDATED rather than created.
	rotated map[string]bool
}

// Refused returns the platform's reason for refusing an external id, and whether
// it refused it at all.
//
// The reason is the platform's own words and is not interpreted: this endpoint
// answers at least one cause that has nothing to do with passwords with "Error
// during password validation.", so a caller that read meaning into it would send
// somebody somewhere false.
func (o Outcome) Refused(externalID string) (string, bool) {
	reason, refused := o.failed[externalID]
	return reason, refused
}

// Rotated reports whether the target already knew this external id, so nothing
// was created and only the password changed.
func (o Outcome) Rotated(externalID string) bool { return o.rotated[externalID] }

func newOutcome(response jsonmodels.BulkNewDeviceRequests) Outcome {
	out := Outcome{failed: map[string]string{}, rotated: map[string]bool{}}
	for _, detail := range response.FailedCreationList() {
		// The reason is recorded verbatim and never interpreted. This endpoint
		// answers at least one cause that has nothing to do with passwords with
		// "Error during password validation.", so treating the text as a
		// diagnosis would send a reader somewhere false.
		out.failed[detail.DeviceID()] = detail.FailureReason()
	}
	for _, detail := range response.CredentialUpdatedList() {
		out.rotated[detail.DeviceID()] = true
	}
	return out
}

// collect turns the platform's answer into credentials.
func (r *Registrar) collect(records []Record, out Outcome) ([]Credential, error) {
	failed, rotated := out.failed, out.rotated

	var (
		credentials []Credential
		failures    []FailedRow
	)
	for _, record := range records {
		if reason, refused := failed[record.ExternalID]; refused {
			fmt.Fprintf(r.stderr, "Refused %s: %s\n", record.ExternalID, reason)
			failures = append(failures, FailedRow{record.ExternalID, reason})
			continue
		}
		if rotated[record.ExternalID] {
			// The target already knew this external id, so nothing was created
			// and the object it belongs to was left untouched - owner included.
			// Check refuses exactly this before a password is generated, so
			// reaching it here means the target gained the identity between that
			// lookup and this upload.
			//
			// Reported as a failure and not handed over. The device whose password
			// was just replaced is one the target already had, which this run has
			// no claim on: the credentials file would otherwise carry a working
			// credential for a device this migration did not create. Withholding it
			// is the same answer the check gives a moment earlier.
			r.rotated++
			reason := "the target tenant already had this external identity, so no object was " +
				"created and an existing device's password was replaced. Its new credential is " +
				"deliberately not reported here"
			fmt.Fprintf(r.stderr, "Refused %s: %s\n", record.ExternalID, reason)
			failures = append(failures, FailedRow{record.ExternalID, reason})
			continue
		}
		credentials = append(credentials, Credential{
			ExternalID: record.ExternalID,
			Tenant:     r.opts.Tenant,
			Username:   DeviceUsername(record.ExternalID),
			Password:   record.Password,
		})
	}

	fmt.Fprintf(r.stderr, "Registered %d of %d %s\n",
		len(credentials), len(records), plural(len(records), "device", "devices"))
	if r.rotated > 0 {
		fmt.Fprintf(r.stderr,
			"%d %s turned out to be already known to the target between the check and the "+
				"upload, so an existing device's password was replaced. Those devices belong "+
				"to the target tenant rather than to this migration - whoever operates them "+
				"has to re-issue their credentials there.\n",
			r.rotated, plural(r.rotated, "device", "devices"))
	}

	if len(failures) > 0 {
		return credentials, &PartialError{Failed: failures, Registered: len(credentials)}
	}
	return credentials, nil
}

// Rotated is how many devices the target already knew.
func (r *Registrar) Rotated() int { return r.rotated }

// readIdentities reads the export's identity artefact for one object.
//
// An absent file means the device has no external identities at all, which the
// export writes by omitting the file rather than writing an empty list - so
// os.ErrNotExist is an answer here, not a failure.
func readIdentities(inputDirectory, sourceID string) ([]export.Identity, error) {
	path := filepath.Join(inputDirectory, sourceID+"_identities.json")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var identities []export.Identity
	if err := json.Unmarshal(raw, &identities); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return identities, nil
}

// SelectIdentity returns the index of the identity to register a device under,
// or -1 when there is no unambiguous choice.
//
// Preferred type first. Failing that, a device with exactly one identity is
// unambiguous and that one is used. A device with several identities and none of
// the preferred type gets -1 rather than a guess: the artefact's order is the
// platform's, not a preference, and binding a device user to an identity nobody
// chose would be discovered when the device reconnects as something else.
//
// Exported and expressed over types alone so that anything predicting what a
// registration would do - the estimate behind the "device credentials" count in
// the migration dialogue - decides it with this function rather than a second
// copy of the rule. A count that disagreed with the run would be worse than no
// count.
func SelectIdentity(types []string, preferred string) int {
	for index, identityType := range types {
		if identityType == preferred {
			return index
		}
	}
	if len(types) == 1 {
		return 0
	}
	return -1
}

// Registrable reports whether an object would be given a device user.
//
// The whole rule in one place: a device, with an identity that can be chosen
// without guessing.
func Registrable(isDevice bool, identityTypes []string, preferred string) bool {
	if !isDevice {
		return false
	}
	if preferred == "" {
		preferred = DefaultIdentityType
	}
	return SelectIdentity(identityTypes, preferred) >= 0
}

// chooseIdentity picks the identity a device is registered under, and explains a
// refusal in terms the operator can act on.
func chooseIdentity(identities []export.Identity, preferred string) (*export.Identity, string) {
	if len(identities) == 0 {
		return nil, "has no external identity, so a registered device user could never be matched to it"
	}

	types := make([]string, 0, len(identities))
	for _, identity := range identities {
		types = append(types, identity.Type)
	}
	if index := SelectIdentity(types, preferred); index >= 0 {
		return &identities[index], ""
	}

	return nil, fmt.Sprintf(
		"has %d external identities (%v) and none of type %s, so which one should own "+
			"the device user is not ours to guess - pass --identityType to choose",
		len(identities), types, preferred)
}

func plural(n int, singular, many string) string {
	if n == 1 {
		return singular
	}
	return many
}

// displayName keeps a progress line readable when an object has no name.
func displayName(name string) string {
	if name == "" {
		return "<unnamed>"
	}
	return name
}
