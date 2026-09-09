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
// docs/platform-behaviour.md records how that was established and what it
// costs; the constraints encoded here are the platform behaviours that pass
// silently if they are got wrong.
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
// format cannot grow a field to say so, being compared byte for byte against the
// shell implementation's. An object still reading "registered" is one the import
// never reached.
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
// requirement: retested 2026-09-09, the platform reads tabs and commas alike,
// including go-c8y's own tab-separated output. Commas because the file is handed
// to an operator who will open it in a spreadsheet, and because the generated
// passwords already exclude a comma for that reason.
//
// The reason this package writes the file itself survives that correction, and
// it was never the delimiter. model.BulkRegistrationRecord has a fixed column
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

// validate refuses a record the registration file cannot carry.
//
// The index is in the message because an external id is not always something an
// operator recognises, and "row 4" is findable in the file either way.
//
// A newline is the one character that cannot be rescued. Everything else is
// escaped by encoding/csv per RFC 4180, and a comma in a device name is entirely
// ordinary - but a line-oriented parser that does not implement quoting would
// read a newline as the end of the row whatever we write, and the failure would
// look like a device that registered with the wrong values rather than a rejected
// file.
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
	if strings.ContainsAny(r.Password, ",\"\r\n") {
		// Generated passwords cannot contain any of these, so reaching this
		// means a caller supplied its own.
		return fmt.Errorf("row %d (%s) has a password containing a comma, quote or newline, which the registration file cannot carry", index+1, r.ExternalID)
	}
	for field, value := range map[string]string{
		"external id":      r.ExternalID,
		"external id type": r.ExternalType,
		"name":             r.Name,
		"type":             r.Type,
	} {
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("row %d (%s) has a %s containing a line break, which the registration file cannot carry", index+1, r.ExternalID, field)
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
