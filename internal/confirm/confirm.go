// Package confirm asks the operator to approve a destructive operation, in the
// shape the c8y binary asks.
//
// Aligning on c8y's dialog rather than inventing one matters because these
// commands sit next to c8y in the same workflows: someone who has answered its
// prompt a hundred times should not have to read a different one here.
//
// The real dialog, verified against a live tenant, is:
//
//	Confirm (job: 1)
//	Delete on https://…/inventory/managedObjects/359973220
//	[y] Yes  [a] Yes to All  [n] No  [l] No to All
//
// It is rendered on STDERR, not stdout, so a caller that redirects stdout still
// sees it - and a caller that captures stderr hides it and appears to hang.
package confirm

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Options is the dialog's answer set. "Yes to All" and "No to All" are offered
// even for a single question so the keys mean the same thing they mean in c8y;
// with one job they simply agree with Yes and No.
const Options = "[y] Yes  [a] Yes to All  [n] No  [l] No to All"

// Answer is what the operator chose.
type Answer int

const (
	No Answer = iota
	Yes
)

// Ask prints the dialog and reads one line.
//
// action is the line between the header and the options, describing what is
// about to happen - c8y puts the request there, so this puts the operation.
func Ask(stderr io.Writer, stdin io.Reader, action string) Answer {
	fmt.Fprintln(stderr, "Confirm (job: 1)")
	fmt.Fprintln(stderr, action)
	fmt.Fprintln(stderr, Options)

	if stdin == nil {
		return refuse(stderr)
	}
	scanner := bufio.NewScanner(stdin)
	if !scanner.Scan() {
		return refuse(stderr)
	}

	switch strings.ToLower(strings.TrimSpace(scanner.Text())) {
	case "y", "yes", "a", "yestoall":
		return Yes
	default:
		// Anything else declines, including an empty line: the safe answer is
		// the default, and a typo must never be read as consent.
		return No
	}
}

// refuse explains a missing answer rather than treating end-of-input as No in
// silence, because the usual cause is a script that forgot --force.
func refuse(stderr io.Writer) Answer {
	fmt.Fprintln(stderr, "No answer received on stdin, pass --force to continue without being asked")
	return No
}
