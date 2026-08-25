package export

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// violation is one limit that the hierarchy exceeds.
type violation struct {
	// rank fixes the row order, so the table reads the same way every time
	// regardless of which limits were breached.
	rank int

	// label is the limit's name as the table shows it.
	label string

	// devices is how many devices breached it, rendered as "-" for limits that
	// are properties of the hierarchy rather than of a device.
	devices string

	highest int
	allowed int

	// worst names the device with the highest count, for the note under the
	// table. Empty for limits that are properties of the hierarchy.
	worst string
}

// checkLimits refuses an export that would be too large, before anything is
// written.
//
// Every violation is collected before any is reported: being told about the
// child limit, fixing it, and then being told about the measurement limit is a
// worse experience than being told both at once.
func (e *Exporter) checkLimits(ctx context.Context) error {
	planned, err := e.collectPlannedIDs(ctx, e.opts.RootID, map[string]bool{})
	if err != nil {
		return err
	}

	var violations []violation

	// The root is not a child of anything, so it does not count against a
	// limit on child objects.
	childObjects := len(planned) - 1
	if childObjects > MaxChildObjects {
		violations = append(violations, violation{
			rank:    1,
			label:   "child objects",
			devices: "-",
			highest: childObjects,
			allowed: MaxChildObjects,
		})
	}

	// Every requested kind, for every device that will be exported, before a
	// single record is written.
	for _, kind := range e.opts.DataTypes {
		limit := recordLimit(kind)
		worstDevice, worst, devices := "", 0, 0

		for id := range planned {
			count, err := e.countRecords(ctx, kind, id)
			if err != nil {
				return err
			}
			if count > limit {
				devices++
				if count > worst {
					worst, worstDevice = count, id
				}
			}
		}
		if devices > 0 {
			violations = append(violations, violation{
				rank:    rankOf(kind),
				label:   string(kind),
				devices: fmt.Sprintf("%d", devices),
				highest: worst,
				allowed: limit,
				worst:   worstDevice,
			})
		}
	}

	if len(violations) == 0 {
		return nil
	}

	// Ordered so the table reads the same way whichever limits were breached.
	sort.Slice(violations, func(i, j int) bool { return violations[i].rank < violations[j].rank })

	report := e.formatViolations(violations)
	if e.opts.IgnoreLimits {
		fmt.Fprint(e.stderr, report)
		fmt.Fprintln(e.stderr)
		fmt.Fprintln(e.stderr, "Exporting anyway because --ignoreLimits was given.")
		return nil
	}

	return errors.New(report +
		"\nNothing was exported. Narrow the export with --childTypes, --dataTypes\n" +
		"or --dateFrom, or pass --ignoreLimits to export regardless.")
}

// collectPlannedIDs walks the hierarchy without writing anything, so the size of
// an export is known before it starts.
//
// It keeps its own visited set, separate from the one the real pass uses: the
// two walks are independent, and sharing one would make the second a no-op.
func (e *Exporter) collectPlannedIDs(ctx context.Context, id string, seen map[string]bool) (map[string]bool, error) {
	if seen[id] {
		return seen, nil
	}
	seen[id] = true

	for _, childType := range e.opts.ChildTypes {
		children, err := e.listChildren(ctx, id, childType)
		if err != nil {
			return nil, err
		}
		for _, childID := range children {
			if _, err := e.collectPlannedIDs(ctx, childID, seen); err != nil {
				return nil, err
			}
		}
	}
	return seen, nil
}

// formatViolations renders the table. The column widths are the bash version's,
// because the output is compared byte for byte.
func (e *Exporter) formatViolations(violations []violation) string {
	var b strings.Builder

	b.WriteString("Export limits exceeded:\n")
	b.WriteString("\n")
	fmt.Fprintf(&b, "  %-22s %8s %14s %14s\n", "limit", "devices", "highest", "allowed")
	fmt.Fprintf(&b, "  %-22s %8s %14s %14s\n",
		"----------------------", "--------", "--------------", "--------------")

	for _, v := range violations {
		fmt.Fprintf(&b, "  %-22s %8s %14d %14d\n", v.label, v.devices, v.highest, v.allowed)
	}

	// Which device is worst, so the operator knows where to look.
	var notes []string
	for _, v := range violations {
		if v.worst != "" {
			notes = append(notes, fmt.Sprintf("  %s: highest on device %s", v.label, v.worst))
		}
	}
	if len(notes) > 0 {
		b.WriteString("\n")
		b.WriteString(strings.Join(notes, "\n"))
		b.WriteString("\n")
	}

	return b.String()
}

// recordLimit is the per-device cap for one kind of data.
func recordLimit(kind DataType) int {
	switch kind {
	case DataMeasurements:
		return MaxMeasurementsPerDevice
	case DataEvents:
		return MaxEventsPerDevice
	case DataAlarms:
		return MaxAlarmsPerDevice
	}
	return 0
}

// rankOf fixes the row order: children first, then the data kinds in the order
// they are fetched.
func rankOf(kind DataType) int {
	switch kind {
	case DataMeasurements:
		return 2
	case DataEvents:
		return 3
	case DataAlarms:
		return 4
	}
	return 9
}

// warnIgnoringLimits is bold on a terminal and plain when redirected, so a log
// file does not fill up with escape codes.
func (e *Exporter) warnIgnoringLimits() {
	bold, reset := "", ""
	if isTerminal(e.stderr) {
		bold, reset = "\033[1m", "\033[0m"
	}
	for _, line := range []string{
		"WARNING: --ignoreLimits is set, so the export limits are not enforced.",
		"An unbounded export can take a very long time and produce a very large",
		"result. It can also put avoidable load on the source tenant.",
	} {
		fmt.Fprintf(e.stderr, "%s%s%s\n", bold, line, reset)
	}
}
