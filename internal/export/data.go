package export

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/alarms"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/events"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/inventory/managedobjects"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/measurements"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/pagination"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/jsondoc"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/jsonraw"
)

// dataPageSize is large because the point of these requests is to drain a
// collection, not to show a page of it.
const dataPageSize = 2000

// wantsData reports whether a kind was asked for.
func (e *Exporter) wantsData(kind DataType) bool {
	for _, wanted := range e.opts.DataTypes {
		if wanted == kind {
			return true
		}
	}
	return false
}

// exportDeviceData writes one device's records and returns the progress-line
// fragment describing them.
//
// The clauses are reported in the order measurements, events, attachments,
// alarms - attachments after the events they belong to rather than at the end.
func (e *Exporter) exportDeviceData(ctx context.Context, deviceID, outputDirectory string) (string, error) {
	if len(e.opts.DataTypes) == 0 {
		return "", nil
	}

	dataDirectory := filepath.Join(outputDirectory, "data", deviceID)
	var summary strings.Builder

	if e.wantsData(DataMeasurements) {
		count, err := e.writeRecords(ctx, DataMeasurements, deviceID, dataDirectory)
		if err != nil {
			return "", err
		}
		if count > 0 {
			if err := e.enforceRecordLimit(DataMeasurements, MaxMeasurementsPerDevice, deviceID, count); err != nil {
				return "", err
			}
			e.measurementCount += count
			fmt.Fprintf(&summary, ", %d %s", count, plural(count, "measurement", "measurements"))
		}
	}

	if e.wantsData(DataEvents) {
		count, err := e.writeRecords(ctx, DataEvents, deviceID, dataDirectory)
		if err != nil {
			return "", err
		}
		if count > 0 {
			if err := e.enforceRecordLimit(DataEvents, MaxEventsPerDevice, deviceID, count); err != nil {
				return "", err
			}
			e.eventCount += count
			fmt.Fprintf(&summary, ", %d %s", count, plural(count, "event", "events"))

			attachments, err := e.exportEventBinaries(ctx, dataDirectory)
			if err != nil {
				return "", err
			}
			if attachments > 0 {
				e.binaryCount += attachments
				fmt.Fprintf(&summary, ", %d %s", attachments,
					plural(attachments, "attachment", "attachments"))
			}
		}
	}

	if e.wantsData(DataAlarms) {
		count, err := e.writeRecords(ctx, DataAlarms, deviceID, dataDirectory)
		if err != nil {
			return "", err
		}
		if count > 0 {
			if err := e.enforceRecordLimit(DataAlarms, MaxAlarmsPerDevice, deviceID, count); err != nil {
				return "", err
			}
			e.alarmCount += count
			fmt.Fprintf(&summary, ", %d %s", count, plural(count, "alarm", "alarms"))
		}
	}

	// Leave no empty directory behind for a device with no data at all.
	_ = os.Remove(dataDirectory)

	return summary.String(), nil
}

// writeRecords drains one collection into a .jsonl file and returns how many
// records it held.
//
// Records are written verbatim, one per line: whatever the platform returned,
// byte for byte. Re-encoding would rewrite numbers, and a measurement value is
// the last thing that should be quietly altered in transit.
//
// An empty file is removed rather than left behind, so its presence means the
// device actually had records of that kind.
func (e *Exporter) writeRecords(ctx context.Context, kind DataType, deviceID, dataDirectory string) (int, error) {
	if err := os.MkdirAll(dataDirectory, 0o755); err != nil {
		return 0, err
	}
	path := filepath.Join(dataDirectory, string(kind)+".jsonl")

	file, err := os.Create(path)
	if err != nil {
		return 0, err
	}

	count := 0
	writeErr := e.eachRecord(ctx, kind, deviceID, func(record jsondoc.JSONDoc) error {
		if _, err := file.Write(append(record.Bytes(), '\n')); err != nil {
			return err
		}
		count++
		return nil
	})
	closeErr := file.Close()

	if writeErr != nil {
		_ = os.Remove(path)
		return 0, writeErr
	}
	if closeErr != nil {
		return 0, closeErr
	}

	if count == 0 {
		_ = os.Remove(path)
		return 0, nil
	}
	e.artefacts.record(path)
	return count, nil
}

// eachRecord walks one collection, following every page.
func (e *Exporter) eachRecord(ctx context.Context, kind DataType, deviceID string,
	visit func(jsondoc.JSONDoc) error) error {

	window := e.opts.Windows[kind]
	paging := pagination.PaginationOptions{PageSize: dataPageSize}
	source := managedobjects.DeviceRef(deviceID)

	switch kind {
	case DataMeasurements:
		iterator := e.client.Measurements.ListAll(ctx, measurements.ListOptions{
			Source: source, DateFrom: window.From, DateTo: window.To,
			PaginationOptions: paging,
		})
		for record, err := range iterator.Items() {
			if err != nil {
				return fmt.Errorf("cannot read measurements of %s: %w", deviceID, err)
			}
			if err := visit(record.GetJSONDoc()); err != nil {
				return err
			}
		}
		return iterator.Err()

	case DataEvents:
		iterator := e.client.Events.ListAll(ctx, events.ListOptions{
			Source: source, DateFrom: window.From, DateTo: window.To,
			PaginationOptions: paging,
		})
		for record, err := range iterator.Items() {
			if err != nil {
				return fmt.Errorf("cannot read events of %s: %w", deviceID, err)
			}
			if err := visit(record.GetJSONDoc()); err != nil {
				return err
			}
		}
		return iterator.Err()

	case DataAlarms:
		iterator := e.client.Alarms.ListAll(ctx, alarms.ListOptions{
			Source: source, DateFrom: window.From, DateTo: window.To,
			PaginationOptions: paging,
		})
		for record, err := range iterator.Items() {
			if err != nil {
				return fmt.Errorf("cannot read alarms of %s: %w", deviceID, err)
			}
			if err := visit(record.GetJSONDoc()); err != nil {
				return err
			}
		}
		return iterator.Err()
	}
	return fmt.Errorf("unknown data type %q", kind)
}

// exportEventBinaries downloads the attachment of every event that claims one.
//
// Named by the SOURCE event id, because that is the only id the export knows.
// The import addresses the upload by the new event id and finds the file by the
// old one.
func (e *Exporter) exportEventBinaries(ctx context.Context, dataDirectory string) (int, error) {
	raw, err := os.ReadFile(filepath.Join(dataDirectory, "events.jsonl"))
	if err != nil {
		return 0, err
	}

	count := 0
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		// Only events carrying c8y_IsBinary have one, so this costs a request
		// per attachment rather than a request per event.
		if !jsonraw.Has([]byte(line), "c8y_IsBinary") {
			continue
		}
		eventID := jsonraw.String([]byte(line), "id")
		if eventID == "" {
			continue
		}

		binariesDirectory := filepath.Join(dataDirectory, "binaries")
		if err := os.MkdirAll(binariesDirectory, 0o755); err != nil {
			return 0, err
		}
		path := filepath.Join(binariesDirectory, eventID)

		if err := e.downloadAttachment(ctx, eventID, path); err != nil {
			return 0, err
		}
		e.artefacts.record(path)
		count++
	}
	return count, nil
}

// downloadAttachment streams one attachment to disk.
//
// Streamed rather than read into memory: the response body is an io.Reader
// precisely because an attachment can be large, and buffering it would make the
// memory this tool needs depend on the biggest file in the tenant.
func (e *Exporter) downloadAttachment(ctx context.Context, eventID, path string) error {
	result := e.client.Events.Binaries.Get(ctx, eventID)
	if result.Err != nil {
		return fmt.Errorf("cannot download the attachment of event %s: %w", eventID, result.Err)
	}

	reader := result.Data.Reader()
	defer func() { _ = reader.Close() }()

	file, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, reader); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("cannot save the attachment of event %s: %w", eventID, err)
	}
	return file.Close()
}

// enforceRecordLimit is the backstop behind the pre-flight count.
//
// The pre-flight asks how many records there are before anything is written; a
// device that gained records in between still must not produce an unbounded
// export.
func (e *Exporter) enforceRecordLimit(kind DataType, limit int, deviceID string, count int) error {
	if e.opts.IgnoreLimits || count <= limit {
		return nil
	}
	e.artefacts.revert()
	return fmt.Errorf("Device %s returned %d %s, more than the limit of %d\nThe partial export was removed.",
		deviceID, count, kind, limit)
}

// countRecords asks how many records a device has, without fetching them.
func (e *Exporter) countRecords(ctx context.Context, kind DataType, deviceID string) (int, error) {
	window := e.opts.Windows[kind]
	// One element and the statistics, which is the cheapest question the API
	// answers.
	paging := pagination.PaginationOptions{
		PageSize: 1, WithTotalElements: true, WithTotalPages: true,
	}
	source := managedobjects.DeviceRef(deviceID)

	switch kind {
	case DataMeasurements:
		result := e.client.Measurements.List(ctx, measurements.ListOptions{
			Source: source, DateFrom: window.From, DateTo: window.To, PaginationOptions: paging,
		})
		return totalFrom(result.Err, result.TotalElements(), result.TotalPages(), kind, deviceID)
	case DataEvents:
		result := e.client.Events.List(ctx, events.ListOptions{
			Source: source, DateFrom: window.From, DateTo: window.To, PaginationOptions: paging,
		})
		return totalFrom(result.Err, result.TotalElements(), result.TotalPages(), kind, deviceID)
	case DataAlarms:
		result := e.client.Alarms.List(ctx, alarms.ListOptions{
			Source: source, DateFrom: window.From, DateTo: window.To, PaginationOptions: paging,
		})
		return totalFrom(result.Err, result.TotalElements(), result.TotalPages(), kind, deviceID)
	}
	return 0, fmt.Errorf("unknown data type %q", kind)
}

// totalFrom prefers totalElements and falls back to totalPages: not every
// version reports the first.
func totalFrom(err error, totalElements, totalPages int64, kind DataType, deviceID string) (int, error) {
	if err != nil {
		return 0, fmt.Errorf("cannot count %s of %s: %w", kind, deviceID, err)
	}
	if totalElements > 0 {
		return int(totalElements), nil
	}
	if totalPages > 0 {
		return int(totalPages), nil
	}
	return 0, nil
}
