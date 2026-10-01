package registration_test

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/registration"
)

func records() []registration.Record {
	return []registration.Record{
		{ExternalID: "MIGTEST-001", ExternalType: "c8y_Serial", Name: "Line A", Type: "c8y_Pump", Password: "aB3xY7zQ2tR5wK8"},
	}
}

// Commas are this tool's choice, not the endpoint's requirement - it reads
// either, retested 2026-09-09. Still pinned: the file goes to an operator who
// opens it in a spreadsheet, and the generated passwords exclude a comma on the
// strength of this format.
func TestRegistrationFileIsCommaSeparated(t *testing.T) {
	var out bytes.Buffer
	if err := registration.WriteRegistrationFile(&out, "", records()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\t") {
		t.Fatalf("registration file contains a tab:\n%s", out.String())
	}
	header, _, _ := strings.Cut(out.String(), "\n")
	if want := "ID,AUTH_TYPE,CREDENTIALS,NAME,TYPE,IDTYPE"; header != want {
		t.Fatalf("header is %q, want %q", header, want)
	}
}

// The agent column is absent on purpose: registration stores custom values as
// strings, so there is no way to say "this device has no agent fragment".
func TestRegistrationFileOmitsAgentAndUnusedColumns(t *testing.T) {
	var out bytes.Buffer
	if err := registration.WriteRegistrationFile(&out, "", records()); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"com_cumulocity_model_Agent", "ICCID", "TENANT", "PATH"} {
		if strings.Contains(out.String(), column) {
			t.Errorf("registration file still carries the %s column:\n%s", column, out.String())
		}
	}
}

func TestRegistrationFileRowValues(t *testing.T) {
	var out bytes.Buffer
	if err := registration.WriteRegistrationFile(&out, "", records()); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&out).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("wrote %d rows, want a header and one record", len(rows))
	}
	want := []string{"MIGTEST-001", "BASIC", "aB3xY7zQ2tR5wK8", "Line A", "c8y_Pump", "c8y_Serial"}
	for i, value := range want {
		if rows[1][i] != value {
			t.Errorf("column %s is %q, want %q", rows[0][i], rows[1][i], value)
		}
	}
}

// A dotted header is what makes Cumulocity store the marker as an object. A bare
// column carrying "{}" would arrive as the literal two-character string, so the
// fragment would not match the shape the importer writes.
func TestRegistrationFileMarkerUsesDottedHeader(t *testing.T) {
	const marker = "c8y_DeviceMigration_20260908T101112Z_ab12cd"

	var out bytes.Buffer
	if err := registration.WriteRegistrationFile(&out, marker, records()); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&out).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rows[0][len(rows[0])-1], marker+".phase"; got != want {
		t.Fatalf("marker column is %q, want %q", got, want)
	}
	if got := rows[1][len(rows[1])-1]; got != registration.PhaseRegistered {
		t.Fatalf("marker value is %q, want %q", got, registration.PhaseRegistered)
	}
}

func TestRegistrationFileWithoutMarkerHasNoMarkerColumn(t *testing.T) {
	var out bytes.Buffer
	if err := registration.WriteRegistrationFile(&out, "", records()); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&out).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows[0]) != 6 {
		t.Fatalf("header has %d columns, want 6 without a marker: %v", len(rows[0]), rows[0])
	}
}

// A marker that is not a legal fragment name would also be an illegal column
// header, and a header carrying a comma shifts every row's fields silently.
//
// The empty marker is not in this list: to the writer it means "no marker
// column", which is what --noFlag asks for. ValidateMarker still refuses it,
// because a marker that is supposed to exist and does not is a different mistake.
func TestRegistrationFileRefusesAnUnusableMarker(t *testing.T) {
	for _, marker := range []string{"has space", "has,comma", "has-dash", `has"quote`} {
		var out bytes.Buffer
		if err := registration.WriteRegistrationFile(&out, marker, records()); err == nil {
			t.Errorf("marker %q was accepted", marker)
		}
	}
	if err := registration.ValidateMarker(""); err == nil {
		t.Error("ValidateMarker accepted an empty marker")
	}
}

// A comma in a device name is entirely ordinary, and this used to assert that
// encoding/csv quoting carried it. Measured 2026-10-01, it does not: the endpoint
// splits on every comma, quoted or not, and the row that results is *accepted*
// whenever the shift starts after the password column - the device arriving under
// a different external id type, with nothing said. See
// docs/platform-behaviour.md.
//
// So the row is refused instead. The test is kept pointing at the same case,
// because the case is the ordinary one and the old expectation is the thing most
// likely to be reintroduced by somebody reasoning from the RFC.
func TestRegistrationFileRefusesCommasInSourceText(t *testing.T) {
	in := records()
	in[0].Name = "Pump, north"

	var out bytes.Buffer
	err := registration.WriteRegistrationFile(&out, "", in)
	if err == nil {
		t.Fatalf("a comma in a name was written:\n%s", out.String())
	}
	// Named, because the operator has to know which device and which field.
	for _, want := range []string{in[0].ExternalID, "name", "comma"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// Quotes are not escaping either, and this is the other half of the same
// measurement: `"Pump ""North"""` on the wire - RFC 4180's encoding of
// `Pump "North"` - arrived as `Pump ""North`, one quote trimmed off the front and
// three off the back, the doubled pair untouched.
func TestRegistrationFileRefusesQuotesInSourceText(t *testing.T) {
	in := records()
	in[0].Name = `Pump "North"`

	var out bytes.Buffer
	if err := registration.WriteRegistrationFile(&out, "", in); err == nil {
		t.Fatalf("a quote in a name was written:\n%s", out.String())
	}
}

// A newline cannot be rescued: a reader that does not implement RFC 4180 quoting
// would treat it as the end of the row and register a device with wrong values.
func TestRegistrationFileRefusesLineBreaks(t *testing.T) {
	for field, mutate := range map[string]func(*registration.Record){
		"external id":   func(r *registration.Record) { r.ExternalID = "MIG\n001" },
		"name":          func(r *registration.Record) { r.Name = "Line\nA" },
		"type":          func(r *registration.Record) { r.Type = "c8y\nPump" },
		"external type": func(r *registration.Record) { r.ExternalType = "c8y\nSerial" },
	} {
		in := records()
		mutate(&in[0])
		var out bytes.Buffer
		if err := registration.WriteRegistrationFile(&out, "", in); err == nil {
			t.Errorf("a line break in the %s was accepted", field)
		}
	}
}

func TestRegistrationFileRefusesIncompleteRecords(t *testing.T) {
	cases := map[string]registration.Record{
		"no external id":   {ExternalType: "c8y_Serial", Password: "aB3xY7zQ2tR5wK8"},
		"no external type": {ExternalID: "MIGTEST-001", Password: "aB3xY7zQ2tR5wK8"},
		"no password":      {ExternalID: "MIGTEST-001", ExternalType: "c8y_Serial"},
		"comma in password": {
			ExternalID: "MIGTEST-001", ExternalType: "c8y_Serial", Password: "aB3,xY7zQ2tR5wK8",
		},
	}
	for name, record := range cases {
		var out bytes.Buffer
		if err := registration.WriteRegistrationFile(&out, "", []registration.Record{record}); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestRegistrationFileRefusesAnEmptyRun(t *testing.T) {
	var out bytes.Buffer
	if err := registration.WriteRegistrationFile(&out, "", nil); err == nil {
		t.Fatal("an empty registration file was written")
	}
}

func TestDeviceUsername(t *testing.T) {
	if got, want := registration.DeviceUsername("MIGTEST-001"), "device_MIGTEST-001"; got != want {
		t.Fatalf("username is %q, want %q", got, want)
	}
}

func TestCredentialsFile(t *testing.T) {
	var out bytes.Buffer
	err := registration.WriteCredentialsFile(&out, []registration.Credential{{
		ExternalID: "MIGTEST-001",
		Tenant:     "t1186848933",
		Username:   registration.DeviceUsername("MIGTEST-001"),
		Password:   "aB3xY7zQ2tR5wK8",
	}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&out).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"EXTERNAL_ID", "TENANT", "USERNAME", "PASSWORD"}; !equal(rows[0], want) {
		t.Fatalf("header is %v, want %v", rows[0], want)
	}
	want := []string{"MIGTEST-001", "t1186848933", "device_MIGTEST-001", "aB3xY7zQ2tR5wK8"}
	if !equal(rows[1], want) {
		t.Fatalf("row is %v, want %v", rows[1], want)
	}
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
