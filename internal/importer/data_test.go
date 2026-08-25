package importer_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/c8yclient"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/export"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/importer"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/testfixture"
)

var allData = []export.DataType{export.DataMeasurements, export.DataEvents, export.DataAlarms}

func wholeHistory() map[export.DataType]export.Window {
	return map[export.DataType]export.Window{
		export.DataMeasurements: {}, export.DataEvents: {}, export.DataAlarms: {},
	}
}

// migrateWithData exports the fixture including its data and imports it back.
func migrateWithData(t *testing.T, opts importer.Options) (*testfixture.Server, string, string) {
	t.Helper()

	server := testfixture.New(t, "hierarchy.json")
	work := t.TempDir()
	s := server.ReadSession(t, work, "fixture.json")
	client, err := c8yclient.New(s, "")
	if err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(work, "export")
	var exportErr bytes.Buffer
	if err := export.New(client, &exportErr, export.Options{
		RootID: "1", OutputDirectory: out, ChildTypes: export.DefaultChildTypes,
		DataTypes: allData, Windows: wholeHistory(),
	}).Run(context.Background()); err != nil {
		t.Fatalf("export failed: %v\n%s", err, exportErr.String())
	}

	opts.InputDirectory = out
	if opts.MappingFile == "" {
		opts.MappingFile = filepath.Join(work, "id_mapping.json")
	}
	opts.AllowSameTenant = true

	var stderr bytes.Buffer
	err = importer.New(client, &stderr, opts).Run(context.Background())
	if err != nil && opts.MappingFile != "" {
		// some tests expect a failure; report it through stderr instead
		stderr.WriteString("IMPORT ERROR: " + err.Error() + "\n")
	}
	return server, opts.MappingFile, stderr.String()
}

// recordsOfKind returns the created records of one kind.
func recordsOfKind(server *testfixture.Server, kind string) []testfixture.CreatedRecord {
	var kept []testfixture.CreatedRecord
	for _, record := range server.CreatedRecords() {
		if record.Kind == kind {
			kept = append(kept, record)
		}
	}
	return kept
}

func TestEveryRecordIsDelivered(t *testing.T) {
	server, _, stderr := migrateWithData(t, importer.Options{NoFlag: true})

	// the fixture holds 4 measurements, 2 events and 3 alarms across two devices
	for _, tc := range []struct {
		kind string
		want int
	}{
		{"measurements", 4},
		{"events", 2},
		{"alarms", 3},
	} {
		if got := len(recordsOfKind(server, tc.kind)); got != tc.want {
			t.Errorf("%s delivered %d, want %d\n%s", tc.kind, got, tc.want, stderr)
		}
	}
	if !strings.Contains(stderr, "Imported 4 measurements, 2 events and 3 alarms, including 1 attachment") {
		t.Errorf("totals:\n%s", stderr)
	}
}

func TestRecordsAreRetargetedAtTheNewDevice(t *testing.T) {
	// The single most important property here. Each record's source names a
	// device in the tenant the export came from; sending it unchanged would
	// attach the record to the wrong device or to nothing at all.
	server, mappingPath, stderr := migrateWithData(t, importer.Options{NoFlag: true})

	mapping := readMapping(t, mappingPath)
	targets := map[string]bool{}
	for _, newID := range mapping {
		targets[newID] = true
	}

	for _, record := range server.CreatedRecords() {
		if record.DeviceID == "" {
			t.Errorf("a %s record has no source at all: %s", record.Kind, record.Body)
			continue
		}
		if !targets[record.DeviceID] {
			t.Errorf("a %s record points at %s, which is not a device this import created\n%s",
				record.Kind, record.DeviceID, stderr)
		}
	}
}

func TestServerManagedFieldsAreStrippedFromRecords(t *testing.T) {
	server, _, _ := migrateWithData(t, importer.Options{NoFlag: true})

	forbidden := map[string][]string{
		"measurements": {"id", "self"},
		"events":       {"id", "self", "creationTime", "lastUpdated", "c8y_IsBinary"},
		"alarms":       {"id", "self", "creationTime", "lastUpdated", "count", "firstOccurrenceTime", "history"},
	}

	for _, record := range server.CreatedRecords() {
		var object map[string]any
		if err := json.Unmarshal(record.Body, &object); err != nil {
			t.Fatalf("a %s body is not valid JSON: %v", record.Kind, err)
		}
		for _, key := range forbidden[record.Kind] {
			if _, present := object[key]; present {
				t.Errorf("a %s record still carries %s: %s", record.Kind, key, record.Body)
			}
		}
	}
}

func TestMeasurementValuesSurviveUnchanged(t *testing.T) {
	// Go's encoding/json would render 21.5 fine but 22.0 as 22, and a
	// measurement value is the last thing that should change in transit.
	server, _, _ := migrateWithData(t, importer.Options{NoFlag: true})

	var seen []string
	for _, record := range recordsOfKind(server, "measurements") {
		seen = append(seen, string(record.Body))
	}
	joined := strings.Join(seen, "\n")

	for _, value := range []string{"21.5", "22.0", "22.5"} {
		if !strings.Contains(joined, value) {
			t.Errorf("measurement value %s did not survive:\n%s", value, joined)
		}
	}
}

func TestMeasurementsAreSentOldestFirst(t *testing.T) {
	// c8y_LatestMeasurements holds the last value RECEIVED, not the newest by
	// timestamp, so an out-of-order backfill leaves the device showing a stale
	// reading as its latest.
	server, _, _ := migrateWithData(t, importer.Options{NoFlag: true})

	var times []string
	for _, record := range recordsOfKind(server, "measurements") {
		var probe struct {
			Time string `json:"time"`
		}
		_ = json.Unmarshal(record.Body, &probe)
		if probe.Time != "" {
			times = append(times, probe.Time)
		}
	}
	for i := 1; i < len(times); i++ {
		if times[i] < times[i-1] {
			t.Fatalf("measurements were not sent oldest first: %v", times)
		}
	}
}

func TestAlarmsAreSentOldestFirst(t *testing.T) {
	// Cumulocity de-duplicates alarms on source and type while one is ACTIVE or
	// ACKNOWLEDGED, so a still-open alarm created before the cleared history of
	// the same type would absorb it and only bump its count.
	server, _, _ := migrateWithData(t, importer.Options{NoFlag: true})

	var times []string
	for _, record := range recordsOfKind(server, "alarms") {
		var probe struct {
			Time string `json:"time"`
		}
		_ = json.Unmarshal(record.Body, &probe)
		times = append(times, probe.Time)
	}
	for i := 1; i < len(times); i++ {
		if times[i] < times[i-1] {
			t.Fatalf("alarms were not sent oldest first: %v", times)
		}
	}
}

func TestMeasurementsGoInBatches(t *testing.T) {
	server, _, _ := migrateWithData(t, importer.Options{NoFlag: true})

	for _, record := range recordsOfKind(server, "measurements") {
		if record.BatchSize > importer.MeasurementBatchSize {
			t.Fatalf("a batch held %d measurements, more than the limit of %d",
				record.BatchSize, importer.MeasurementBatchSize)
		}
	}
}

func TestProcessingModeIsQuiescentForMeasurementsAndEvents(t *testing.T) {
	// Persists the records but suppresses the realtime notifications a backfill
	// would otherwise fire, so smart rules do not react to history as though it
	// were happening now.
	server, _, _ := migrateWithData(t, importer.Options{NoFlag: true})

	for _, kind := range []string{"measurements", "events"} {
		records := recordsOfKind(server, kind)
		if len(records) == 0 {
			t.Fatalf("no %s were delivered at all", kind)
		}
		for _, record := range records {
			if record.ProcessingMode != "QUIESCENT" {
				t.Errorf("a %s record was sent with processing mode %q, want QUIESCENT",
					kind, record.ProcessingMode)
			}
		}
	}
}

func TestAlarmsCarryNoProcessingMode(t *testing.T) {
	// QUIESCENT is documented as applying to measurements and events only, and
	// alarm creation is PERSISTENT regardless, so asking for anything else would
	// be a claim the platform does not honour.
	server, _, _ := migrateWithData(t, importer.Options{NoFlag: true})

	records := recordsOfKind(server, "alarms")
	if len(records) == 0 {
		t.Fatal("no alarms were delivered at all")
	}
	for _, record := range records {
		if record.ProcessingMode != "" {
			t.Errorf("an alarm was sent with processing mode %q, want none", record.ProcessingMode)
		}
	}
}

func TestAttachmentsAreUploadedAgainstTheNewEventID(t *testing.T) {
	// The file on disk is named by the OLD event id, because that is the only id
	// the export knew; the upload is addressed by the NEW one.
	server, _, stderr := migrateWithData(t, importer.Options{NoFlag: true})

	uploaded := server.UploadedBinaries()
	if len(uploaded) != 1 {
		t.Fatalf("uploaded %d attachments, want 1\n%s", len(uploaded), stderr)
	}
	// the new id is one the tenant allocated, not the fixture's "e2"
	if uploaded[0] == "e2" {
		t.Errorf("the attachment was uploaded against the source event id %q", uploaded[0])
	}
	if !strings.Contains(stderr, "Attached e2 to event "+uploaded[0]) {
		t.Errorf("the upload is not reported against the new id:\n%s", stderr)
	}
}

func TestAMissingAttachmentIsReportedButNotAFailure(t *testing.T) {
	// The export is what is incomplete. Retrying cannot fix it, and a resume
	// that failed on this would loop forever.
	server := testfixture.New(t, "hierarchy.json")
	work := t.TempDir()
	s := server.ReadSession(t, work, "fixture.json")
	client, err := c8yclient.New(s, "")
	if err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(work, "export")
	var discard bytes.Buffer
	if err := export.New(client, &discard, export.Options{
		RootID: "2", OutputDirectory: out, ChildTypes: export.DefaultChildTypes,
		DataTypes: allData, Windows: wholeHistory(),
	}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	// take the attachment away, leaving the event claiming one
	if err := os.Remove(filepath.Join(out, "data", "2", "binaries", "e2")); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	err = importer.New(client, &stderr, importer.Options{
		InputDirectory: out, MappingFile: filepath.Join(work, "id_mapping.json"),
		NoFlag: true, AllowSameTenant: true,
	}).Run(context.Background())

	if err != nil {
		t.Fatalf("a missing attachment must not fail the import: %v\n%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Event e2 claimed an attachment but none was exported") {
		t.Errorf("stderr:\n%s", stderr.String())
	}
}

func TestARefusedRecordMakesTheImportFail(t *testing.T) {
	// One bad record should not stop the import, but the exit code has to say
	// so, or migrate would journal a device as finished with records missing.
	server := testfixture.New(t, "hierarchy.json")
	work := t.TempDir()
	s := server.ReadSession(t, work, "fixture.json")
	client, err := c8yclient.New(s, "")
	if err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(work, "export")
	var discard bytes.Buffer
	if err := export.New(client, &discard, export.Options{
		RootID: "1", OutputDirectory: out, ChildTypes: export.DefaultChildTypes,
		DataTypes: allData, Windows: wholeHistory(),
	}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	server.FailDataAfter(1) // let one record through, refuse the rest

	var stderr bytes.Buffer
	err = importer.New(client, &stderr, importer.Options{
		InputDirectory: out, MappingFile: filepath.Join(work, "id_mapping.json"),
		NoFlag: true, AllowSameTenant: true,
	}).Run(context.Background())

	if err == nil {
		t.Fatalf("refused records must fail the import\n%s", stderr.String())
	}
	if !strings.Contains(err.Error(), "could not be imported, so this import is incomplete") {
		t.Errorf("error: %v", err)
	}
	// the objects were still created, so the mapping is complete and revert works
	if !strings.Contains(stderr.String(), "Imported Plant Berlin") {
		t.Errorf("the objects were not imported:\n%s", stderr.String())
	}
}

func TestADeviceWithNoDataIsSkippedQuietly(t *testing.T) {
	_, _, stderr := migrateWithData(t, importer.Options{NoFlag: true})

	// device 3 has no data; it must not be reported as importing zero of
	// anything
	if strings.Contains(stderr, "Imported 0 ") {
		t.Errorf("a device with no data was reported anyway:\n%s", stderr)
	}
}

func TestMeasurementsAreSplitIntoBatches(t *testing.T) {
	// The shared fixture has three measurements on a device, which fit in one
	// batch whatever the batch size is - so this needs a device with more than
	// one batch's worth before the batching is observable at all.
	server := testfixture.New(t, "many-measurements.json")
	work := t.TempDir()
	s := server.ReadSession(t, work, "fixture.json")
	client, err := c8yclient.New(s, "")
	if err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(work, "export")
	var discard bytes.Buffer
	if err := export.New(client, &discard, export.Options{
		RootID: "1", OutputDirectory: out, ChildTypes: export.DefaultChildTypes,
		DataTypes: []export.DataType{export.DataMeasurements}, Windows: wholeHistory(),
	}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	if err := importer.New(client, &stderr, importer.Options{
		InputDirectory: out, MappingFile: filepath.Join(work, "id_mapping.json"),
		NoFlag: true, AllowSameTenant: true,
	}).Run(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, stderr.String())
	}

	records := recordsOfKind(server, "measurements")
	if len(records) != 120 {
		t.Fatalf("delivered %d measurements, want 120\n%s", len(records), stderr.String())
	}

	// Every request must stay within the batch size, and there must be more
	// than one request or the batching did not happen.
	batches := map[int]int{}
	for _, record := range records {
		if record.BatchSize > importer.MeasurementBatchSize {
			t.Fatalf("a request carried %d measurements, more than the limit of %d",
				record.BatchSize, importer.MeasurementBatchSize)
		}
		batches[record.BatchSize]++
	}
	if len(records) > importer.MeasurementBatchSize && len(batches) == 1 && records[0].BatchSize == len(records) {
		t.Fatal("all 120 measurements went in one request, so they were not batched")
	}
	if !strings.Contains(stderr.String(), "Imported 120 measurements for Busy Sensor") {
		t.Errorf("stderr:\n%s", stderr.String())
	}
}

func TestAFailedMeasurementBatchAloneFailsTheImport(t *testing.T) {
	// Asserted on its own, because an import that also had events and alarms
	// would fail for their sake and hide a measurement batch that was refused
	// without being counted.
	server := testfixture.New(t, "hierarchy.json")
	work := t.TempDir()
	s := server.ReadSession(t, work, "fixture.json")
	client, err := c8yclient.New(s, "")
	if err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(work, "export")
	var discard bytes.Buffer
	if err := export.New(client, &discard, export.Options{
		RootID: "2", OutputDirectory: out, ChildTypes: export.DefaultChildTypes,
		// measurements only, so nothing else can account for the failure
		DataTypes: []export.DataType{export.DataMeasurements}, Windows: wholeHistory(),
	}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	server.FailDataAfter(0) // refuse every measurement

	var stderr bytes.Buffer
	err = importer.New(client, &stderr, importer.Options{
		InputDirectory: out, MappingFile: filepath.Join(work, "id_mapping.json"),
		NoFlag: true, AllowSameTenant: true,
	}).Run(context.Background())

	if err == nil {
		t.Fatalf("a refused measurement batch must fail the import\n%s", stderr.String())
	}
	if !strings.Contains(err.Error(), "could not be imported, so this import is incomplete") {
		t.Errorf("error: %v", err)
	}
	if !strings.Contains(stderr.String(), "Failed to import measurements for Line A") {
		t.Errorf("the failure was not reported per device:\n%s", stderr.String())
	}
	if len(recordsOfKind(server, "measurements")) != 0 {
		t.Error("measurements were delivered despite being refused")
	}
}

func TestDataOnlyAnnouncesNoRunID(t *testing.T) {
	// It creates no objects, so it marks nothing. Saying otherwise would be a
	// lie, and an incremental migration would repeat it once per device.
	server := testfixture.New(t, "hierarchy.json")
	work := t.TempDir()
	s := server.ReadSession(t, work, "fixture.json")
	client, err := c8yclient.New(s, "")
	if err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(work, "export")
	var discard bytes.Buffer
	if err := export.New(client, &discard, export.Options{
		RootID: "1", OutputDirectory: out, ChildTypes: export.DefaultChildTypes,
		DataTypes: allData, Windows: wholeHistory(),
	}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	mapping := filepath.Join(work, "id_mapping.json")
	var first bytes.Buffer
	if err := importer.New(client, &first, importer.Options{
		InputDirectory: out, MappingFile: mapping,
		RunID: "RUN_ABC", AllowSameTenant: true,
	}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	var second bytes.Buffer
	if err := importer.New(client, &second, importer.Options{
		InputDirectory: out, MappingFile: mapping,
		RunID: "RUN_ABC", DataOnly: true, AllowSameTenant: true,
	}).Run(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, second.String())
	}

	if strings.Contains(second.String(), "Run id") {
		t.Errorf("--dataOnly announced a run id:\n%s", second.String())
	}
}
