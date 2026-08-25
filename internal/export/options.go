// Package export walks a device hierarchy and writes it to disk.
//
// The artefact layout is a contract, not an implementation detail: the bash
// implementation in commands/export writes the same files, and
// docs/go-port/BEHAVIOUR-CONTRACT.md records exactly what "the same" means down
// to the trailing newline. A differential test diffs the two.
package export

import (
	"errors"
	"fmt"
	"strings"
)

// Limits are checked before anything is written, so a refusal leaves nothing
// behind. Values mirror commands/export:147.
const (
	MaxChildObjects = 25

	MaxMeasurementsPerDevice = 1000000
	MaxEventsPerDevice       = 1000
	MaxAlarmsPerDevice       = 1000

	DefaultMeasurementDateFrom = "-14d"
)

// ChildType is one of the three relationship kinds Cumulocity distinguishes.
type ChildType string

const (
	ChildAsset    ChildType = "asset"
	ChildDevice   ChildType = "device"
	ChildAddition ChildType = "addition"
)

// DefaultChildTypes matches the bash default of "asset,device": additions are
// left out because they reach configuration and firmware objects that are not
// part of a device hierarchy.
var DefaultChildTypes = []ChildType{ChildAsset, ChildDevice}

// Options is the resolved form of the command line.
type Options struct {
	RootID string

	OutputDirectory  string
	OutputCompressed string

	ChildTypes []ChildType

	// DataTypes selects which device data to include. Empty means none, which
	// is the default: a plain export stays fast, and a device can hold years of
	// measurements.
	DataTypes []DataType

	// Windows is the time range per data type, from ResolveWindows.
	Windows map[DataType]Window

	IgnoreLimits bool

	// MetadataOnly writes objects and identities but no data, while still
	// checking the limits against whatever DataTypes selects - so an oversized
	// migration is refused before anything is created.
	MetadataOnly bool

	// DataOnly writes the data and skips the external identities.
	DataOnly bool
}

// ResolveChildTypes parses a --childTypes value.
//
// Mirrors commands/export:326 including its error messages, which are part of
// the contract.
func ResolveChildTypes(value string, noChildren bool) ([]ChildType, error) {
	if noChildren {
		return nil, nil
	}
	if strings.TrimSpace(value) == "" {
		// only reachable if someone passes --childTypes "" explicitly; the flag
		// default is non-empty
		return nil, errors.New(
			"No child types given, expected a comma separated list of asset, device or addition")
	}

	var resolved []ChildType
	for _, token := range strings.Split(value, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		switch ChildType(token) {
		case ChildAsset, ChildDevice, ChildAddition:
			resolved = append(resolved, ChildType(token))
		default:
			return nil, fmt.Errorf(
				"Unsupported child type '%s', expected asset, device or addition", token)
		}
	}
	if len(resolved) == 0 {
		return nil, errors.New(
			"No child types given, expected a comma separated list of asset, device or addition")
	}
	return resolved, nil
}

// ResolveDataTypes parses a --dataTypes value.
//
// Operations get their own explanation rather than a generic "unsupported",
// because wanting them is reasonable and the reason they are excluded is not
// obvious.
func ResolveDataTypes(value string) ([]DataType, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}

	var resolved []DataType
	for _, token := range strings.Split(value, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		switch DataType(token) {
		case DataMeasurements, DataEvents, DataAlarms:
			resolved = append(resolved, DataType(token))
		case "operations":
			return nil, errors.New(
				"Operations are not migrated: they cannot be created with a status, so every\n" +
					"recreated operation would be PENDING and the target device would execute it")
		default:
			return nil, fmt.Errorf(
				"Unsupported data type '%s', expected measurements, events or alarms", token)
		}
	}
	if len(resolved) == 0 {
		return nil, errors.New(
			"No data types given, expected a comma separated list of measurements, events or alarms")
	}
	return resolved, nil
}

// plural mirrors the bash helper of the same name, because every count in the
// output reads as prose.
func plural(n int, singular, many string) string {
	if n == 1 {
		return singular
	}
	return many
}
