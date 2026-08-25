package cli

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/c8yclient"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/reverter"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/session"
)

func newRevertCommand(stderr io.Writer) *cobra.Command {
	var (
		mappingFile  string
		runID        string
		force        bool
		sessionValue string
	)

	cmd := &cobra.Command{
		Use:   "revert",
		Short: "Delete the managed objects an import created",
		Long: "Delete the managed objects an import created.\n\n" +
			"Everything to be deleted is listed first, and nothing is touched until that\n" +
			"list has been confirmed. --force skips the question.\n\n" +
			"Deleting a managed object takes its external identities and its parent and\n" +
			"child references with it, so those are not deleted separately. Cumulocity also\n" +
			"propagates a deletion to subgroups when the object is a group, which can\n" +
			"remove objects this import never created; the plan says so before you confirm.\n\n" +
			"--runId finds the objects by their marker fragment instead of a mapping file,\n" +
			"for when the mapping was lost. It only finds objects that were marked.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, _ := cmd.Flags().GetString("sessionHome")
			return runRevert(cmd.Context(), stderr, os.Stdin, sessionValue, home, reverter.Options{
				MappingFile: mappingFile,
				RunID:       runID,
				Force:       force,
			})
		},
	}

	cmd.Flags().StringVar(&mappingFile, "mapping", "",
		"ID mapping file written by import")
	cmd.Flags().StringVar(&runID, "runId", "",
		"Find the objects by their c8y_DeviceMigration_<id> marker fragment instead")
	cmd.Flags().BoolVar(&force, "force", false, "Delete without asking for confirmation")
	cmd.Flags().StringVar(&sessionValue, "session", "",
		"Session to delete from: a path, a full file name including its extension, or a glob. "+
			"Defaults to the active session")

	// One or the other, and never neither: revert must be told what to delete
	// and must never guess.
	cmd.MarkFlagsMutuallyExclusive("mapping", "runId")
	cmd.MarkFlagsOneRequired("mapping", "runId")

	return cmd
}

func runRevert(ctx context.Context, stderr io.Writer, stdin io.Reader,
	sessionValue, home string, opts reverter.Options) error {

	s, _, err := session.Current(sessionValue, home)
	if err != nil {
		return err
	}
	client, err := c8yclient.New(s, session.Passphrase())
	if err != nil {
		return err
	}

	err = reverter.New(client, stderr, stdin, opts).Run(ctx)
	if errors.Is(err, reverter.ErrIncomplete) {
		// Already explained on stderr, in more detail than one line could.
		return &silentError{err: err}
	}
	return err
}
