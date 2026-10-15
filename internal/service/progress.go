package service

import (
	"io"
	"log/slog"
	"regexp"
	"strings"
	"sync"
)

// ProgressLines is how many of a job's most recent output lines are kept for the
// job record and its status response. A migration of the largest hierarchy the
// limits allow prints a few hundred lines, so this holds most runs in full while
// keeping a bounded amount in memory and in the managed object.
const ProgressLines = 200

// runIDPattern finds the run id in the engine's own output.
//
// The engine generates the id itself and has no option to accept one, so the
// only way to learn it is to read the line it announces. Both branches print the
// same prefix: the importer's "Run id X, marking created objects with ..." and
// migrate's "Run id X, created objects are not marked".
var runIDPattern = regexp.MustCompile(`^Run id (\S+),`)

// progress is the io.Writer the engine writes to instead of a terminal.
//
// Everything goes to the container's stdout, because that is the only log the
// platform shows. A bounded tail is also kept for the job record, and the run id
// is picked out on the way past.
type progress struct {
	jobID string

	mu      sync.Mutex
	partial strings.Builder
	lines   []string
	runID   string
}

func newProgress(jobID string) *progress {
	return &progress{jobID: jobID, lines: make([]string, 0, ProgressLines)}
}

// Write splits whatever the engine writes into lines. The engine writes with
// Fprintf and Fprintln, so a write is not guaranteed to be a whole line - the
// partial buffer is what keeps a split write from becoming two log lines.
func (p *progress) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.partial.Write(b)
	buffered := p.partial.String()

	for {
		line, rest, found := strings.Cut(buffered, "\n")
		if !found {
			break
		}
		p.record(line)
		buffered = rest
	}

	p.partial.Reset()
	p.partial.WriteString(buffered)
	return len(b), nil
}

// Flush records anything the engine left without a trailing newline.
func (p *progress) Flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.partial.Len() > 0 {
		p.record(p.partial.String())
		p.partial.Reset()
	}
}

func (p *progress) record(line string) {
	if line == "" {
		return
	}

	// The job id is on every line, because one container's log carries every
	// job it has run.
	slog.Info(line, "job", p.jobID)

	if p.runID == "" {
		if match := runIDPattern.FindStringSubmatch(line); match != nil {
			p.runID = match[1]
		}
	}

	if len(p.lines) == ProgressLines {
		p.lines = append(p.lines[:0], p.lines[1:]...)
	}
	p.lines = append(p.lines, line)
}

// Lines returns the tail of the output.
func (p *progress) Lines() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.lines...)
}

// RunID is the run id the engine announced, or "" if it has not yet.
func (p *progress) RunID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.runID
}

// interface check: the engine takes an io.Writer and nothing more
var _ io.Writer = (*progress)(nil)
