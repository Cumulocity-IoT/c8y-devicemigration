// Package migrate runs an export and an import against two tenants as one
// operation.
package migrate

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// The journal records which devices' data has arrived.
//
// The mapping is already the record for objects - it is written after every
// create - so this fills the only gap: nothing otherwise says whether a device's
// measurements, events and alarms made it across.
//
// Only the incremental path writes one, because it is the only path with a
// per-device commit point. A chained run imports all the data in one step, so
// there is no moment at which it could honestly claim a device was finished.
type Journal struct {
	path string
}

func NewJournal(mappingFile string) *Journal {
	return &Journal{path: mappingFile + ".journal"}
}

// Path is where the journal lives, which error messages need to name.
func (j *Journal) Path() string { return j.path }

// Start truncates the journal and records which run it belongs to.
func (j *Journal) Start(runID string) error {
	if err := os.WriteFile(j.path, nil, 0o644); err != nil {
		return err
	}
	return j.Note("runId " + runID)
}

// Note appends a line.
func (j *Journal) Note(line string) error {
	file, err := os.OpenFile(j.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(file, line); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// Has reports whether a line is present.
func (j *Journal) Has(line string) bool {
	for _, existing := range j.lines() {
		if existing == line {
			return true
		}
	}
	return false
}

// RunID is the run this journal belongs to, or "" if it names none.
func (j *Journal) RunID() string {
	for _, line := range j.lines() {
		if rest, ok := strings.CutPrefix(line, "runId "); ok {
			return rest
		}
	}
	return ""
}

// Exists reports whether there is a journal at all.
func (j *Journal) Exists() bool {
	_, err := os.Stat(j.path)
	return err == nil
}

// Interrupted returns the devices whose data was in flight when a run stopped:
// a "started" with no matching "data".
//
// Their records are delivered again in full, because there is no per-record
// account of what arrived. Naming them is the honest thing to do.
func (j *Journal) Interrupted() []string {
	started := map[string]bool{}
	var order []string

	for _, line := range j.lines() {
		if id, ok := strings.CutPrefix(line, "started "); ok {
			if !started[id] {
				order = append(order, id)
			}
			started[id] = true
			continue
		}
		if id, ok := strings.CutPrefix(line, "data "); ok {
			delete(started, id)
		}
	}

	var interrupted []string
	for _, id := range order {
		if started[id] {
			interrupted = append(interrupted, id)
		}
	}
	return interrupted
}

func (j *Journal) lines() []string {
	file, err := os.Open(j.path)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines
}
