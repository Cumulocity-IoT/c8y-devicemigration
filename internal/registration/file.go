// Package registration creates the device users a migrated fleet authenticates
// with, by bulk-registering the devices in the target tenant before the import
// runs.
//
// The order is the whole design, and it is not a preference. Bulk registration
// creates a managed object, a device user and an external identity in one call,
// and the object belongs to the new device user - but only for an external id the
// tenant does not already know. Registering a device the import has already
// created merely rotates a password and leaves the object untouched, owner
// included, so the device would arrive unable to write its own twin. Registering
// first is what makes ownership correct by construction.
//
// The constraints encoded here are the platform behaviours that pass silently
// if they are got wrong.
package registration

import (
	"encoding/csv"
	"fmt"
	"io"
	"strings"
)

// PhaseRegistered is the value the marker fragment carries between the two
// phases.
//
// The marker's name identifies the run; this says how far the run got with this
// object. It matters because a target id now exists from registration, so the
// mapping file can no longer mean "the import finished this object" - and its
// format, a plain map of old to new ids, has no room for a field to say so. An
// object still reading "registered" is one the import never reached.
const PhaseRegistered = "registered"

// AuthTypeBasic is the only authentication type this package registers.
//
// CERTIFICATES exists in the API and belongs to Option C, which moves a trust
// anchor rather than issuing credentials, and therefore never comes through here.
const AuthTypeBasic = "BASIC"

// registrationColumns is the header of the file Cumulocity's bulk registration
// reads.
//
// Comma-separated, which is this tool's choice rather than the endpoint's
// requirement: the platform reads tabs and commas alike, including go-c8y's own
// tab-separated output. Commas because the file is handed to an operator who
// will open it in a spreadsheet, and because the generated passwords already
// exclude a comma for that reason.
//
// The reason this package writes the file itself is not the delimiter.
// model.BulkRegistrationRecord has a fixed column
// list with no way to add the dotted marker column, and its SetBasicAuth forces
// com_cumulocity_model_Agent.active, which is exactly the column that has to be
// absent rather than false.
//
// ICCID, TENANT, PATH and com_cumulocity_model_Agent.active are all omitted. The
// first three have nothing to say about a migrated device, and the agent column is
// omitted on purpose: registration writes every custom value as a *string*, so
// `false` would arrive as {"active": "false"}, which is not the same thing as a
// device having no agent fragment at all. The import's PUT supplies the source's
// own fragment instead, and merge semantics leave nothing behind to correct.
var registrationColumns = []string{
	"ID",          // external id
	"AUTH_TYPE",   // BASIC
	"CREDENTIALS", // the generated password
	"NAME",
	"TYPE",
	"IDTYPE", // external id type, e.g. c8y_Serial
}

// credentialsColumns is the header of the file handed to the operator.
//
// Its own format, not the registration file's: what an operator needs is which
// device, in which tenant, with which username and password, and the username is
// derived rather than supplied so it cannot drift from the platform's convention.
var credentialsColumns = []string{"EXTERNAL_ID", "TENANT", "USERNAME", "PASSWORD"}

// Record is one device to register.
type Record struct {
	// SourceID is the object this row came from in the source tenant. Not
	// written to the registration file, which has no column for it: it is here so
	// that a refusal can name a device in the tenant the operator is looking at,
	// rather than only by an external id they may not recognise.
	SourceID string

	// ExternalID and ExternalType are the identity the export carried over, and
	// the reason registration can be matched to the imported object at all.
	ExternalID   string
	ExternalType string

	Name string
	Type string

	Password string
}

// Credential is one line of the credentials file.
type Credential struct {
	ExternalID string
	Tenant     string
	Username   string
	Password   string
}

// DeviceUsername is the user bulk registration creates for an external id.
//
// The `device_` prefix is the platform's convention, which go-c8y also encodes in
// users.ByDeviceUser. Derived in one place because the credentials file, the
// ownership check and the revert all have to agree on it, and a mismatch would
// look like a missing user rather than a naming bug.
func DeviceUsername(externalID string) string { return "device_" + externalID }

// WriteRegistrationFile writes the bulk registration file for records.
//
// marker is the run's fragment name, or empty to write no marker column. When it
// is set, a `<marker>.phase` column is appended: the dotted header is what makes
// Cumulocity store the value as an object rather than a scalar, so the fragment
// ends up shaped like the one the import writes and the `has(<marker>)` query both
// --resume and revert use finds the object from the moment it exists. A bare
// column carrying `{}` would not do: braces in a data row arrive as the literal
// two-character string.
func WriteRegistrationFile(w io.Writer, marker string, records []Record) error {
	if len(records) == 0 {
		return fmt.Errorf("nothing to register")
	}
	if marker != "" {
		if err := ValidateMarker(marker); err != nil {
			return err
		}
	}

	header := append([]string{}, registrationColumns...)
	if marker != "" {
		header = append(header, marker+".phase")
	}

	writer := csv.NewWriter(w)
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("cannot write the registration header: %w", err)
	}

	for index, record := range records {
		if err := record.validate(index); err != nil {
			return err
		}
		row := []string{
			record.ExternalID,
			AuthTypeBasic,
			record.Password,
			record.Name,
			record.Type,
			record.ExternalType,
		}
		if marker != "" {
			row = append(row, PhaseRegistered)
		}
		if err := writer.Write(row); err != nil {
			return fmt.Errorf("cannot write a registration row for %s: %w", record.ExternalID, err)
		}
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("cannot write the registration file: %w", err)
	}
	return nil
}

// WriteCredentialsFile writes the credentials handed back to the operator.
func WriteCredentialsFile(w io.Writer, credentials []Credential) error {
	writer := csv.NewWriter(w)
	if err := writer.Write(credentialsColumns); err != nil {
		return fmt.Errorf("cannot write the credentials header: %w", err)
	}
	for _, credential := range credentials {
		if err := writer.Write([]string{
			credential.ExternalID,
			credential.Tenant,
			credential.Username,
			credential.Password,
		}); err != nil {
			return fmt.Errorf("cannot write the credentials row for %s: %w", credential.ExternalID, err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("cannot write the credentials file: %w", err)
	}
	return nil
}

// unencodable is the characters no field of the registration file can carry.
//
// Measured against the endpoint rather than against RFC 4180, and the result is
// that the file has no escaping at all:
//
//   - **A comma always separates a field**, including inside a quoted one. A row
//     whose quoted external id held a comma was split there, putting the rest of
//     the id into AUTH_TYPE, and the platform answered "has unknown auth type".
//     That one failed loudly only because AUTH_TYPE happens to be validated:
//     nothing after CREDENTIALS is, so the same shift in a *name* was accepted,
//     and the device was registered under the wrong external id type entirely.
//   - **Quotes are trimmed, never unescaped.** `"Pump ""North"""` on the wire -
//     which is what RFC 4180 encoding of `Pump "North"` produces - arrived as
//     `Pump ""North`: one quote off the front, three off the back, the doubled
//     pair left as it was.
//   - **A line break ends the row** whatever is written around it.
//
// So encoding/csv quoting buys nothing here, and a value carrying any of these is
// refused rather than escaped, altered or dropped. Altering one was considered
// and rejected: a name is overwritten by the import moments later and could in
// principle be stripped, but a tool that silently changes what it was asked to
// migrate is a worse thing to own than one that says it cannot.
const unencodable = ",\"\r\n"

// FieldProblem says why a value cannot be carried in the registration file, or
// returns empty when it can.
//
// Exported because two callers need the same answer at different moments: the
// registration phase skips such a device while it is still planning, and the file
// writer refuses it outright. One rule, so a device that plans cannot then fail
// to write.
func FieldProblem(what, value string) string {
	if !strings.ContainsAny(value, unencodable) {
		return ""
	}
	return fmt.Sprintf("its %s contains a comma, a quote or a line break, and the device "+
		"registration file has no way to carry one: the platform would read the row with "+
		"its columns shifted", what)
}

// fields is what a record puts in the file, named as an operator would name them.
//
// A slice rather than a map so the first problem reported for a record is always
// the same one, whichever field carries it.
func (r Record) fields() [][2]string {
	return [][2]string{
		{"external id", r.ExternalID},
		{"external id type", r.ExternalType},
		{"name", r.Name},
		{"type", r.Type},
	}
}

// validate refuses a record the registration file cannot carry.
//
// The index is in the message because an external id is not always something an
// operator recognises, and "row 4" is findable in the file either way.
//
// This is the backstop rather than the thing an operator meets: the registration
// phase skips an unencodable device while planning, with a reason naming it. What
// reaches here is a caller that built its own record - the device self-service
// endpoint does - and refusing is the only safe answer, because a shifted row is
// accepted by the platform rather than rejected.
func (r Record) validate(index int) error {
	if strings.TrimSpace(r.ExternalID) == "" {
		return fmt.Errorf("row %d has no external id, so the device it registers could never be found again", index+1)
	}
	if strings.TrimSpace(r.ExternalType) == "" {
		return fmt.Errorf("row %d (%s) has no external id type", index+1, r.ExternalID)
	}
	if r.Password == "" {
		return fmt.Errorf("row %d (%s) has no password", index+1, r.ExternalID)
	}
	if strings.ContainsAny(r.Password, unencodable) {
		// Generated passwords cannot contain any of these, so reaching this
		// means a caller supplied its own.
		return fmt.Errorf("row %d (%s) has a password containing a comma, quote or newline, which the registration file cannot carry", index+1, r.ExternalID)
	}
	for _, field := range r.fields() {
		if problem := FieldProblem(field[0], field[1]); problem != "" {
			return fmt.Errorf("row %d (%s): %s", index+1, r.ExternalID, problem)
		}
	}
	return nil
}

// ValidateMarker refuses a marker that could not be a Cumulocity fragment name.
//
// The same alphabet the importer enforces on a run id, checked again here because
// this marker also becomes a *column header*, and a header carrying a comma or a
// quote would silently shift every row's fields rather than being rejected.
func ValidateMarker(marker string) error {
	if marker == "" {
		return fmt.Errorf("the run marker is empty")
	}
	for _, r := range marker {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return fmt.Errorf("invalid run marker '%s': letters, digits and underscores only, "+
				"because it becomes both a Cumulocity fragment name and a registration file column", marker)
		}
	}
	return nil
}
