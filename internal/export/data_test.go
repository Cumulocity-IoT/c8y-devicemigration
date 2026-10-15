package export_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/export"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/testfixture"
)

// allData is the usual selection, in the order the export reports it.
var allData = []export.DataType{export.DataMeasurements, export.DataEvents, export.DataAlarms}

// wholeHistory windows everything open, so a test sees the fixture's records
// whatever today's date is. The measurement default of -14d would otherwise
// exclude the fixture's 2026-01 timestamps as time passes.
func wholeHistory() map[export.DataType]export.Window {
	return map[export.DataType]export.Window{
		export.DataMeasurements: {},
		export.DataEvents:       {},
		export.DataAlarms:       {},
	}
}

func TestDataIsNotExportedUnlessAsked(t *testing.T) {
	// A plain export stays fast: a device can hold years of measurements.
	dir, _, err := run(t, "hierarchy.json", export.Options{
		RootID: "1", ChildTypes: export.DefaultChildTypes})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "data")); statErr == nil {
		t.Error("a data directory was written without --dataTypes")
	}
}

func TestRecordsAreWrittenAsNDJSON(t *testing.T) {
	dir, _, err := run(t, "hierarchy.json", export.Options{
		RootID: "2", ChildTypes: export.DefaultChildTypes,
		DataTypes: allData, Windows: wholeHistory(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, tc := range []struct {
		file string
		want int
	}{
		{"measurements.jsonl", 3},
		{"events.jsonl", 2},
		{"alarms.jsonl", 3},
	} {
		raw, readErr := os.ReadFile(filepath.Join(dir, "data", "2", tc.file))
		if readErr != nil {
			t.Errorf("%s: %v", tc.file, readErr)
			continue
		}
		lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
		if len(lines) != tc.want {
			t.Errorf("%s holds %d records, want %d", tc.file, len(lines), tc.want)
		}
		// one record per line, each valid JSON on its own
		for _, line := range lines {
			if !strings.HasPrefix(line, "{") || !strings.HasSuffix(line, "}") {
				t.Errorf("%s has a line that is not one record: %q", tc.file, line)
			}
		}
	}
}

func TestAnEmptyCollectionLeavesNoFile(t *testing.T) {
	// The file's presence has to mean the device actually had records.
	dir, _, err := run(t, "hierarchy.json", export.Options{
		RootID: "4", ChildTypes: export.DefaultChildTypes,
		DataTypes: allData, Windows: wholeHistory(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// device 4 has one measurement and nothing else
	if _, statErr := os.Stat(filepath.Join(dir, "data", "4", "measurements.jsonl")); statErr != nil {
		t.Errorf("the measurement file is missing: %v", statErr)
	}
	for _, absent := range []string{"events.jsonl", "alarms.jsonl"} {
		if _, statErr := os.Stat(filepath.Join(dir, "data", "4", absent)); statErr == nil {
			t.Errorf("%s was written for a device with none", absent)
		}
	}
}

func TestADeviceWithNoDataLeavesNoDirectory(t *testing.T) {
	dir, _, err := run(t, "hierarchy.json", export.Options{
		RootID: "1", ChildTypes: export.DefaultChildTypes,
		DataTypes: allData, Windows: wholeHistory(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// device 3 has no data at all
	if _, statErr := os.Stat(filepath.Join(dir, "data", "3")); statErr == nil {
		t.Error("an empty data directory was left behind for device 3")
	}
	// while the two that do have data are there
	for _, id := range []string{"2", "4"} {
		if _, statErr := os.Stat(filepath.Join(dir, "data", id)); statErr != nil {
			t.Errorf("data/%s is missing: %v", id, statErr)
		}
	}
}

func TestRecordsAreWrittenVerbatim(t *testing.T) {
	// A measurement value is the last thing that should be quietly altered in
	// transit, so the bytes the platform returned are the bytes on disk.
	dir, _, err := run(t, "hierarchy.json", export.Options{
		RootID: "2", ChildTypes: export.DefaultChildTypes,
		DataTypes: allData, Windows: wholeHistory(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "data", "2", "alarms.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// alarm a3's text holds escaped JSON with quotes and newlines, which once
	// got mangled on the way through a shell pipeline
	if !strings.Contains(string(raw), `msgParams`) {
		t.Errorf("the escaped-JSON alarm text did not survive:\n%s", raw)
	}
	if strings.Contains(string(raw), `\\\"msgParams`) {
		t.Errorf("the escaped-JSON alarm text was double-escaped:\n%s", raw)
	}
}

func TestAttachmentsAreDownloadedForEventsThatClaimThem(t *testing.T) {
	dir, stderr, err := run(t, "hierarchy.json", export.Options{
		RootID: "2", ChildTypes: export.DefaultChildTypes,
		DataTypes: allData, Windows: wholeHistory(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// the fixture's event e2 carries c8y_IsBinary; e1 does not
	body, readErr := os.ReadFile(filepath.Join(dir, "data", "2", "binaries", "e2"))
	if readErr != nil {
		t.Fatalf("the attachment was not downloaded: %v", readErr)
	}
	if !strings.Contains(string(body), "attachment of event e2") {
		t.Errorf("the wrong bytes were saved: %q", body)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "data", "2", "binaries", "e1")); statErr == nil {
		t.Error("an attachment was fetched for an event that has none")
	}
	if !strings.Contains(stderr, "1 attachment") {
		t.Errorf("the attachment is not reported:\n%s", stderr)
	}
}

func TestTheProgressLineReportsDataInOrder(t *testing.T) {
	// measurements, events, attachments, alarms - attachments next to the events
	// they belong to rather than at the end.
	_, stderr, err := run(t, "hierarchy.json", export.Options{
		RootID: "2", ChildTypes: export.DefaultChildTypes,
		DataTypes: allData, Windows: wholeHistory(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "Exported Line A (2), 2 asset children, 2 identities, 3 measurements, 2 events, 1 attachment, 3 alarms"
	if !strings.Contains(stderr, want) {
		t.Fatalf("progress line:\nwant %q\ngot:\n%s", want, stderr)
	}
}

func TestTotalsIncludeTheData(t *testing.T) {
	_, stderr, err := run(t, "hierarchy.json", export.Options{
		RootID: "1", ChildTypes: export.DefaultChildTypes,
		DataTypes: allData, Windows: wholeHistory(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "with 4 measurements, 2 events and 3 alarms including 1 attachment to "
	if !strings.Contains(stderr, want) {
		t.Fatalf("totals:\nwant %q\ngot:\n%s", want, stderr)
	}
}

func TestMetadataOnlySkipsTheDataButKeepsTheLimits(t *testing.T) {
	dir, stderr, err := run(t, "hierarchy.json", export.Options{
		RootID: "1", ChildTypes: export.DefaultChildTypes,
		DataTypes: allData, Windows: wholeHistory(), MetadataOnly: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "data")); statErr == nil {
		t.Error("--metadataOnly wrote data")
	}
	// and the totals do not claim data was exported
	if strings.Contains(stderr, "with 0 measurements") {
		t.Errorf("the totals mention data under --metadataOnly:\n%s", stderr)
	}
	if !strings.Contains(stderr, "Exported 9 managed objects and 4 external identities to ") {
		t.Errorf("totals:\n%s", stderr)
	}
}

func TestDataOnlySkipsIdentities(t *testing.T) {
	dir, _, err := run(t, "hierarchy.json", export.Options{
		RootID: "2", DataTypes: allData, Windows: wholeHistory(), DataOnly: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "2_identities.json")); statErr == nil {
		t.Error("--dataOnly wrote an identity file")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "data", "2", "measurements.jsonl")); statErr != nil {
		t.Errorf("--dataOnly did not write the data: %v", statErr)
	}
}

// --- the windows -----------------------------------------------------------

func TestMeasurementsDefaultToTheLastFourteenDays(t *testing.T) {
	// A device can hold years of measurements, so an unbounded default would
	// make the first run of this tool look broken.
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	windows, err := export.ResolveWindows("", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if got := windows[export.DataMeasurements].From; !got.Equal(now.AddDate(0, 0, -14)) {
		t.Errorf("measurement window starts at %v, want 14 days back", got)
	}
	// events and alarms are the current state of a device, and a truncated
	// history is misleading rather than merely incomplete
	if !windows[export.DataEvents].From.IsZero() {
		t.Error("events should default to the whole history")
	}
	if !windows[export.DataAlarms].From.IsZero() {
		t.Error("alarms should default to the whole history")
	}
}

func TestAnExplicitDateFromAppliesToEverything(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	windows, err := export.ResolveWindows("-7d", "", now)
	if err != nil {
		t.Fatal(err)
	}
	for kind, window := range windows {
		if !window.From.Equal(now.AddDate(0, 0, -7)) {
			t.Errorf("%s starts at %v, want 7 days back", kind, window.From)
		}
	}
}

func TestRelativeAndAbsoluteDatesAreBothAccepted(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value string
		want  time.Time
	}{
		{"-30d", now.AddDate(0, 0, -30)},
		{"-2w", now.AddDate(0, 0, -14)},
		{"-6h", now.Add(-6 * time.Hour)},
		{"2026-01-01T00:00:00Z", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"2026-01-01", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		got, err := export.ParseDate(tc.value, now)
		if err != nil {
			t.Errorf("%q: %v", tc.value, err)
			continue
		}
		if !got.Equal(tc.want) {
			t.Errorf("%q -> %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestAnUnparsableDateIsRefusedRatherThanGuessed(t *testing.T) {
	// Quietly exporting the wrong window is worse than not starting.
	for _, bad := range []string{"yesterday", "-30", "30d", "-30y", "soon"} {
		if _, err := export.ParseDate(bad, time.Now()); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestTheWindowIsActuallyApplied(t *testing.T) {
	// The fixture's measurements on device 2 are hourly from 2026-01-01T00:00Z.
	// A window that starts after the first excludes it.
	dir, _, err := run(t, "hierarchy.json", export.Options{
		RootID: "2", DataTypes: []export.DataType{export.DataMeasurements},
		Windows: map[export.DataType]export.Window{
			export.DataMeasurements: {
				From: time.Date(2026, 1, 1, 0, 30, 0, 0, time.UTC),
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "data", "2", "measurements.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("the window kept %d of 3 measurements, want 2:\n%s", len(lines), raw)
	}
}

// --- the record limits ------------------------------------------------------

func TestAPerDeviceRecordLimitIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	// Driven by an inflated count rather than a million real records.
	server := testfixture.New(t, "hierarchy.json")
	server.SetReportedTotal("alarms", 5000)

	dir, stderr, err := runAgainst(t, server, export.Options{
		RootID: "1", ChildTypes: export.DefaultChildTypes,
		DataTypes: []export.DataType{export.DataAlarms}, Windows: wholeHistory(),
	})
	if err == nil {
		t.Fatalf("an oversized device must be refused\n%s", stderr)
	}
	for _, want := range []string{
		"Export limits exceeded:",
		"alarms",
		"5000",
		"1000",
		"highest on device",
		"Nothing was exported",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal is missing %q:\n%s", want, err.Error())
		}
	}
	if _, statErr := os.Stat(dir); statErr == nil {
		t.Error("the output directory survived the refusal")
	}
}

func TestEveryBreachedLimitIsReportedNotJustTheFirst(t *testing.T) {
	// Being told about one limit, fixing it, and then being told about the next
	// is a worse experience than being told both at once.
	server := testfixture.New(t, "hierarchy.json")
	server.SetReportedTotal("measurements", 2000000)
	server.SetReportedTotal("alarms", 5000)

	_, _, err := runAgainst(t, server, export.Options{
		RootID: "1", ChildTypes: export.DefaultChildTypes,
		DataTypes: allData, Windows: wholeHistory(),
	})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"measurements", "alarms", "2000000", "5000"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal is missing %q:\n%s", want, err.Error())
		}
	}
}

func TestIgnoreLimitsExportsDataAnyway(t *testing.T) {
	server := testfixture.New(t, "hierarchy.json")
	server.SetReportedTotal("alarms", 5000)

	_, stderr, err := runAgainst(t, server, export.Options{
		RootID: "2", ChildTypes: export.DefaultChildTypes,
		DataTypes: allData, Windows: wholeHistory(), IgnoreLimits: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "Exporting anyway because --ignoreLimits was given.") {
		t.Errorf("stderr:\n%s", stderr)
	}
}

func TestARootIDThatIsAPathIsRefused(t *testing.T) {
	// The id ends up in every artefact file name, so one containing a path
	// separator does not merely produce an odd name: '../x' would write outside
	// the output directory. It is also the only id a caller supplies.
	for _, tc := range []struct {
		id       string
		contains string
	}{
		{"./1399229", "Did you mean '1399229'?"},
		{"../escaped", "cannot contain a path separator"},
		{"a/b", "cannot contain a path separator"},
		{`a\b`, "cannot contain a path separator"},
		{".", "that is a path, not an id"},
		{"..", "that is a path, not an id"},
		{" 1399229", "leading or trailing whitespace"},
		{"", "No root id given"},
	} {
		err := export.ValidateRootID(tc.id)
		if err == nil {
			t.Errorf("%q should be refused", tc.id)
			continue
		}
		if !strings.Contains(err.Error(), tc.contains) {
			t.Errorf("%q: message is %q, want it to mention %q", tc.id, err.Error(), tc.contains)
		}
	}
}

func TestAPlainRootIDIsAccepted(t *testing.T) {
	// Including the resolver forms the platform accepts, which refusing
	// anything but digits would have taken away.
	for _, id := range []string{"1399229", "1", "name:My Device", "ext:c8y_Serial:ABC-123"} {
		if err := export.ValidateRootID(id); err != nil {
			t.Errorf("%q should be accepted: %v", id, err)
		}
	}
}

func TestABadRootIDIsRefusedBeforeAnyRequest(t *testing.T) {
	server := testfixture.New(t, "hierarchy.json")
	_, _, err := runAgainst(t, server, export.Options{
		RootID: "./1", ChildTypes: export.DefaultChildTypes,
	})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	// Measured from after the client was built, which fetches a token of its own.
	if got := len(server.Requests()) - requestsBefore; got != 0 {
		t.Errorf("the export made %d requests before refusing, want 0", got)
	}
}

func TestAllIsAShortcutForEveryDataType(t *testing.T) {
	// Asking for all three is the common case, and spelling them out invites a
	// typo that silently narrows the migration.
	got, err := export.ResolveDataTypes("all")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := export.AllDataTypes()
	if len(got) != len(want) {
		t.Fatalf("all resolved to %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("all resolved to %v, want %v", got, want)
		}
	}
	// and it is the same set an explicit list gives, in the same order
	explicit, err := export.ResolveDataTypes("measurements,events,alarms")
	if err != nil {
		t.Fatal(err)
	}
	for i := range explicit {
		if explicit[i] != got[i] {
			t.Fatalf("all (%v) and the explicit list (%v) differ", got, explicit)
		}
	}
}

func TestAllIsCaseInsensitiveAndTolerantOfSpace(t *testing.T) {
	for _, value := range []string{"all", "ALL", "All", "  all  "} {
		got, err := export.ResolveDataTypes(value)
		if err != nil {
			t.Errorf("%q: %v", value, err)
			continue
		}
		if len(got) != 3 {
			t.Errorf("%q resolved to %v", value, got)
		}
	}
}

func TestAllCannotBeCombinedWithNamedTypes(t *testing.T) {
	// Ambiguous: does "all,events" mean all, or is one of them a mistake?
	// Refusing says which.
	for _, value := range []string{"all,events", "measurements,all", "all,all"} {
		_, err := export.ResolveDataTypes(value)
		if err == nil {
			t.Errorf("%q should be refused", value)
			continue
		}
		if !strings.Contains(err.Error(), "cannot be combined with other data types") {
			t.Errorf("%q: unhelpful message %q", value, err.Error())
		}
	}
}

func TestAllExportsEveryKind(t *testing.T) {
	resolved, err := export.ResolveDataTypes("all")
	if err != nil {
		t.Fatal(err)
	}
	dir, stderr, err := run(t, "hierarchy.json", export.Options{
		RootID: "2", ChildTypes: export.DefaultChildTypes,
		DataTypes: resolved, Windows: wholeHistory(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, file := range []string{"measurements.jsonl", "events.jsonl", "alarms.jsonl"} {
		if _, statErr := os.Stat(filepath.Join(dir, "data", "2", file)); statErr != nil {
			t.Errorf("%s was not written: %v", file, statErr)
		}
	}
	if !strings.Contains(stderr, "3 measurements, 2 events, 1 attachment, 3 alarms") {
		t.Errorf("stderr:\n%s", stderr)
	}
}
