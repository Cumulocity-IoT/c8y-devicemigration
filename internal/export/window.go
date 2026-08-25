package export

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DataType is one kind of device data.
type DataType string

const (
	DataMeasurements DataType = "measurements"
	DataEvents       DataType = "events"
	DataAlarms       DataType = "alarms"
)

// Window is the time range one kind of data is fetched over.
//
// Zero values mean unbounded, which is what the platform does with an absent
// dateFrom or dateTo.
type Window struct {
	From time.Time
	To   time.Time
}

// ParseDate accepts the forms the bash implementation passes through to c8y:
// a relative offset like -14d or -30d, or an absolute timestamp.
//
// The bash version never parsed these - it handed them to c8y, which did. The
// library takes a time.Time, so the parsing is ours now, and it is deliberately
// narrow: a value it does not understand is refused rather than guessed at,
// because quietly exporting the wrong window is worse than not starting.
func ParseDate(value string, now time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}

	if offset, ok := parseRelative(value); ok {
		return now.Add(-offset), nil
	}

	// Absolute, in the shapes Cumulocity itself emits and accepts.
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.000Z0700",
		"2006-01-02T15:04:05",
		"2006-01-02",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}

	return time.Time{}, fmt.Errorf(
		"Unsupported date '%s', expected a relative offset like -30d or an absolute time like 2026-01-01T00:00:00Z",
		value)
}

// parseRelative understands -N followed by a unit, which is the form the help
// text advertises and the one the limit suggestion produces.
func parseRelative(value string) (time.Duration, bool) {
	if !strings.HasPrefix(value, "-") || len(value) < 3 {
		return 0, false
	}
	number, unit := value[1:len(value)-1], value[len(value)-1]

	count, err := strconv.Atoi(number)
	if err != nil || count < 0 {
		return 0, false
	}

	switch unit {
	case 's':
		return time.Duration(count) * time.Second, true
	case 'm':
		return time.Duration(count) * time.Minute, true
	case 'h':
		return time.Duration(count) * time.Hour, true
	case 'd':
		return time.Duration(count) * 24 * time.Hour, true
	case 'w':
		return time.Duration(count) * 7 * 24 * time.Hour, true
	default:
		return 0, false
	}
}

// ResolveWindows works out the range each kind of data is fetched over.
//
// Measurements default to the last 14 days when no --dateFrom is given, because
// a device can hold years of them and an unbounded default would make the first
// run of this tool look broken. Events and alarms default to the whole history:
// they are the current state of a device, and a truncated alarm history is
// misleading rather than merely incomplete.
func ResolveWindows(dateFrom, dateTo string, now time.Time) (map[DataType]Window, error) {
	from, err := ParseDate(dateFrom, now)
	if err != nil {
		return nil, err
	}
	to, err := ParseDate(dateTo, now)
	if err != nil {
		return nil, err
	}

	measurementFrom := from
	if dateFrom == "" {
		measurementFrom, err = ParseDate(DefaultMeasurementDateFrom, now)
		if err != nil {
			return nil, err
		}
	}

	return map[DataType]Window{
		DataMeasurements: {From: measurementFrom, To: to},
		DataEvents:       {From: from, To: to},
		DataAlarms:       {From: from, To: to},
	}, nil
}
