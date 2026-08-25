// Package cli wires the commands together.
//
// Every command writes its progress to stderr and nothing to stdout, matching
// the bash implementation: that keeps the output identical regardless of a
// user's c8y output and view settings, which otherwise decide whether a whole
// managed object is rendered for every device touched.
package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// Main runs the CLI and returns the process exit code.
//
// Exit codes mirror the bash commands: 0 on success, 1 for a usage error or a
// refusal. There is no equivalent of the docopt digest mismatch (70).
func Main(args []string, stdout, stderr io.Writer) int {
	root := newRootCommand(stdout, stderr)
	root.SetArgs(args)

	if err := root.Execute(); err != nil {
		// cobra has already printed usage errors; anything else is ours to
		// report, and a multi-line refusal must keep its shape.
		var quiet *silentError
		if !errors.As(err, &quiet) {
			fmt.Fprintln(stderr, err)
		}
		return 1
	}
	return 0
}

// silentError marks an error whose message has already been written to stderr,
// so Main does not print it twice.
type silentError struct{ err error }

func (e *silentError) Error() string { return e.err.Error() }
func (e *silentError) Unwrap() error { return e.err }

func newRootCommand(stdout, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:   "c8y-devicemigration",
		Short: "Migrate a device hierarchy between Cumulocity tenants",
		Long: "Migrate a device hierarchy between Cumulocity tenants.\n\n" +
			"Sessions are the ones go-c8y-cli writes. A --session value is a path, a\n" +
			"full file name including its extension, or a glob matched against the\n" +
			"session home. A bare name without .json does not resolve, and a glob\n" +
			"matching more than one file is refused rather than guessed at.",
		SilenceUsage:  true,
		SilenceErrors: true,
		// no action of its own: help is the right answer to a bare invocation
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	root.SetOut(stdout)
	root.SetErr(stderr)

	// Shared by every command that talks to a tenant. Deliberately not read
	// from `c8y settings list`: this tool must work without the c8y binary.
	root.PersistentFlags().String("sessionHome", "",
		"Directory to resolve session names against (default $C8Y_SESSION_HOME, or ~/.cumulocity)")

	root.AddCommand(newExportCommand(stderr))
	root.AddCommand(newImportCommand(stderr))
	root.AddCommand(newPreflightCommand(stderr))

	return root
}
