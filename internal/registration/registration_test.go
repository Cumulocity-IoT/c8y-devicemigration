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

// The delimiter is the one thing about this file that was established the hard
// way: the endpoint rejects the tab-separated file go-c8y's own writer produces.
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

// A comma in a device name is entirely ordinary, and quoting is what encoding/csv
// is for. This is the case that would have been broken by hand-rolling the rows.
func TestRegistrationFileQuotesCommasInSourceText(t *testing.T) {
	in := records()
	in[0].Name = "Pump, north"

	var out bytes.Buffer
	if err := registration.WriteRegistrationFile(&out, "", in); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"Pump, north"`) {
		t.Fatalf("comma in a name was not quoted:\n%s", out.String())
	}
	rows, err := csv.NewReader(&out).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if rows[1][3] != "Pump, north" {
		t.Fatalf("name round-tripped as %q", rows[1][3])
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
