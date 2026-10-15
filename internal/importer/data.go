package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	apicontext "github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/context"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/core"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/events/eventbinaries"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/jsonraw"
)

// Batching and pacing for the data import.
//
// Deliberately cautious, and smallest for measurements because they arrive in
// the largest numbers. Cumulocity's limits that matter here are roughly a 1 MB
// request document, a soft 1000-element array and a hard five-minute request
// timeout; 50 measurements stays far inside all three even for records carrying
// many fragments.
const (
	MeasurementBatchSize = 50
)

// Server-managed fields, stripped before a record is recreated.
//
// The target tenant assigns its own, and `source` in particular must go: it
// names a device in the tenant the export came from, and sending it would
// attach the record to the wrong device or to nothing at all. retarget
// replaces it with the new device.
var (
	measurementStrip = []string{"id", "self"}
	eventStrip       = []string{"id", "self", "creationTime", "lastUpdated", "c8y_IsBinary", "source"}
	alarmStrip       = []string{"id", "self", "creationTime", "lastUpdated", "count",
		"firstOccurrenceTime", "history", "source"}
)

// importDeviceData recreates one device's records.
//
// Measurements, then events, then alarms, and each of them oldest first. Both
// orderings matter:
//
//   - c8y_LatestMeasurements on the managed object holds the last value
//     RECEIVED, not the newest by timestamp, so an out-of-order backfill leaves
//     the device showing a stale reading as its latest.
//   - Cumulocity de-duplicates alarms on source and type while one is ACTIVE or
//     ACKNOWLEDGED, so a still-open alarm created before the cleared history of
//     the same type would absorb it and only bump its count.
func (i *Importer) importDeviceData(ctx context.Context, inputDirectory, oldID, newID, name string) error {
	dataDirectory := filepath.Join(inputDirectory, "data", oldID)
	if info, err := os.Stat(dataDirectory); err != nil || !info.IsDir() {
		return nil
	}

	if err := i.importMeasurements(ctx, dataDirectory, newID, name); err != nil {
		return err
	}
	if err := i.importEvents(ctx, dataDirectory, newID, name); err != nil {
		return err
	}
	return i.importAlarms(ctx, dataDirectory, newID, name)
}

// readRecords loads a .jsonl file oldest first.
//
// Sorted by the record's own time rather than by file order, because the export
// wrote them in whatever order the platform returned them.
func readRecords(path string) ([][]byte, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var records [][]byte
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		records = append(records, []byte(line))
	}

	sort.SliceStable(records, func(a, b int) bool {
		return jsonraw.String(records[a], "time") < jsonraw.String(records[b], "time")
	})
	return records, nil
}

// retarget strips the server-managed fields and points the record at the device
// it now belongs to.
func retarget(record []byte, strip []string, newID string) ([]byte, error) {
	out := record
	var err error
	for _, key := range strip {
		if out, err = jsonraw.Delete(out, key); err != nil {
			return nil, err
		}
	}
	return jsonraw.SetRaw(out, "source", []byte(`{"id":`+quote(newID)+`}`))
}

func quote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func (i *Importer) importMeasurements(ctx context.Context, dataDirectory, newID, name string) error {
	records, err := readRecords(filepath.Join(dataDirectory, "measurements.jsonl"))
	if err != nil || len(records) == 0 {
		return err
	}

	// QUIESCENT persists the records but suppresses the realtime notifications
	// a backfill would otherwise fire, which is what stops smart rules and
	// subscriptions reacting to history as though it were happening now.
	ctx = apicontext.WithProcessingModeQuiescent(ctx)

	created := 0
	for start := 0; start < len(records); start += MeasurementBatchSize {
		end := min(start+MeasurementBatchSize, len(records))

		batch := make([][]byte, 0, end-start)
		for _, record := range records[start:end] {
			retargeted, retargetErr := retarget(record, measurementStrip, newID)
			if retargetErr != nil {
				return retargetErr
			}
			batch = append(batch, retargeted)
		}

		body := []byte(`{"measurements":[` + string(joinRaw(batch)) + `]}`)
		if result := i.client.Measurements.CreateList(ctx, json.RawMessage(body)); result.Err != nil {
			// The whole batch failed, so every record in it is missing.
			i.dataFailures += end - start
			fmt.Fprintf(i.stderr, "Failed to import measurements for %s (%s)\n", name, newID)
			return nil
		}
		created += end - start
	}

	i.measurementsCreated += created
	fmt.Fprintf(i.stderr, "Imported %d %s for %s (%s)\n",
		created, plural(created, "measurement", "measurements"), name, newID)
	return nil
}

// joinRaw concatenates raw JSON documents into an array body without decoding
// them, so their numbers survive.
func joinRaw(records [][]byte) []byte {
	var b strings.Builder
	for index, record := range records {
		if index > 0 {
			b.WriteString(",")
		}
		b.Write(record)
	}
	return []byte(b.String())
}

func (i *Importer) importEvents(ctx context.Context, dataDirectory, newID, name string) error {
	records, err := readRecords(filepath.Join(dataDirectory, "events.jsonl"))
	if err != nil || len(records) == 0 {
		return err
	}

	ctx = apicontext.WithProcessingModeQuiescent(ctx)
	created := 0

	for _, record := range records {
		oldEventID := jsonraw.String(record, "id")
		hasAttachment := jsonraw.Has(record, "c8y_IsBinary")

		body, retargetErr := retarget(record, eventStrip, newID)
		if retargetErr != nil {
			return retargetErr
		}

		result := i.client.Events.CreateRaw(ctx, json.RawMessage(body))
		newEventID := result.Data.ID()
		if result.Err != nil || newEventID == "" {
			i.dataFailures++
			fmt.Fprintf(i.stderr, "Failed to create event %s for %s (%s)\n", oldEventID, name, newID)
			continue
		}
		created++

		if !hasAttachment {
			continue
		}
		if err := i.uploadAttachment(ctx, dataDirectory, oldEventID, newEventID, name, newID); err != nil {
			return err
		}
	}

	i.eventsCreated += created
	if created > 0 {
		fmt.Fprintf(i.stderr, "Imported %d %s for %s (%s)\n",
			created, plural(created, "event", "events"), name, newID)
	}
	return nil
}

// uploadAttachment sends the file the export downloaded.
//
// The file on disk is named by the OLD event id, because that is the only id the
// export knew; the upload is addressed by the NEW one.
func (i *Importer) uploadAttachment(ctx context.Context, dataDirectory,
	oldEventID, newEventID, name, newID string) error {

	path := filepath.Join(dataDirectory, "binaries", oldEventID)
	if _, err := os.Stat(path); err != nil {
		// Not counted as a failure: the export is what is incomplete, retrying
		// cannot fix it, and a resume that failed on this would loop forever.
		fmt.Fprintf(i.stderr, "Event %s claimed an attachment but none was exported\n", oldEventID)
		return nil
	}

	result := i.client.Events.Binaries.Create(ctx, newEventID, eventbinaries.UploadFileOptions(
		core.UploadFileOptions{FilePath: path, Name: filepath.Base(path)}))
	if result.Err != nil {
		i.dataFailures++
		fmt.Fprintf(i.stderr, "Failed to attach %s to event %s\n", filepath.Base(path), newEventID)
		return nil
	}

	i.binariesCreated++
	fmt.Fprintf(i.stderr, "Attached %s to event %s of %s (%s)\n",
		filepath.Base(path), newEventID, name, newID)
	return nil
}

func (i *Importer) importAlarms(ctx context.Context, dataDirectory, newID, name string) error {
	records, err := readRecords(filepath.Join(dataDirectory, "alarms.jsonl"))
	if err != nil || len(records) == 0 {
		return err
	}

	// No processing mode. QUIESCENT is documented as applying to measurements
	// and events only, and alarm creation is PERSISTENT regardless - so asking
	// for anything else would be a claim the platform does not honour.
	created, failed := 0, 0

	for _, record := range records {
		body, retargetErr := retarget(record, alarmStrip, newID)
		if retargetErr != nil {
			return retargetErr
		}
		if result := i.client.Alarms.CreateRaw(ctx, json.RawMessage(body)); result.Err != nil {
			failed++
			continue
		}
		created++
	}

	i.alarmsCreated += created
	i.dataFailures += failed

	if created > 0 {
		fmt.Fprintf(i.stderr, "Imported %d %s for %s (%s)\n",
			created, plural(created, "alarm", "alarms"), name, newID)
	}
	if failed > 0 {
		fmt.Fprintf(i.stderr, "Failed to import %d %s for %s (%s)\n",
			failed, plural(failed, "alarm", "alarms"), name, newID)
	}
	return nil
}
