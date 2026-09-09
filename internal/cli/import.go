package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/c8yclient"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/importer"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/registration"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/session"
)

func newImportCommand(stderr io.Writer) *cobra.Command {
	var (
		inputDirectory  string
		mappingFile     string
		runID           string
		noFlag          bool
		allowSameTenant bool
		adoptRegistered bool
		stampOwner      bool
		identityType    string
		sessionValue    string
	)

	cmd := &cobra.Command{
		Use:   "import",
		Short: "Recreate an exported hierarchy in a tenant",
		Long: "Recreate an exported hierarchy in a tenant.\n\n" +
			"The migration includes all properties and fragments of the concerned managed\n" +
			"objects and all external identities. Device protocols are not migrated.\n\n" +
			"Existing objects are never matched or updated: every import creates new\n" +
			"managed objects, so importing the same export twice produces two copies.\n" +
			"The one exception is --adoptRegistered, which updates the objects a prior\n" +
			"`register` created for the same --runId, and only those.\n\n" +
			"A migrated device cannot write to its own managed object until it owns it,\n" +
			"because ROLE_DEVICE carries writes only together with ownership. Pass\n" +
			"--stampDeviceOwner to name each device as the owner of its own object up\n" +
			"front, which works even though the device user does not exist yet.\n" +
			"Importing back into the tenant the export came from is refused unless\n" +
			"--allowSameTenant is given.\n\n" +
			"Every created object is marked with an empty c8y_DeviceMigration_<runId>\n" +
			"fragment, and the run id is reported, so the objects of one import can be\n" +
			"found again with a single --fragmentType query even if the mapping is lost.\n" +
			"Pass --runId to choose it, or --noFlag to mark nothing.\n\n" +
			"Without --session the active go-c8y-cli session is used.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, _ := cmd.Flags().GetString("sessionHome")
			return runImport(cmd.Context(), stderr, sessionValue, home, importer.Options{
				InputDirectory:  inputDirectory,
				MappingFile:     mappingFile,
				RunID:           runID,
				NoFlag:          noFlag,
				AllowSameTenant: allowSameTenant,
				AdoptRegistered: adoptRegistered,

				StampDeviceOwner: stampOwner,
				IdentityType:     identityType,
			})
		},
	}

	cmd.Flags().StringVar(&inputDirectory, "input-directory", "",
		"Directory holding the export (required)")
	cmd.Flags().StringVar(&mappingFile, "mapping", "id_mapping.json",
		"File to write the old ID to new ID mapping to")
	cmd.Flags().StringVar(&runID, "runId", "",
		"Mark every created managed object with an empty c8y_DeviceMigration_<id> "+
			"fragment. Letters, digits and underscores only. One is generated and "+
			"reported if this is not given")
	cmd.Flags().BoolVar(&noFlag, "noFlag", false,
		"Do not mark created managed objects at all. Without a marker the objects "+
			"can only be found again through the mapping file")
	cmd.Flags().BoolVar(&allowSameTenant, "allowSameTenant", false,
		"Import even when the export came from this same tenant")
	cmd.Flags().BoolVar(&adoptRegistered, "adoptRegistered", false,
		"Update the objects `register` created for this run instead of creating new "+
			"ones. Needs the same --runId, and only ever updates an object carrying "+
			"that run's marker")
	cmd.Flags().BoolVar(&stampOwner, "stampDeviceOwner", false,
		"Make each migrated device the owner of its own managed object, so it can "+
			"write to it once it has credentials. Needed when credentials are issued "+
			"after the import; not needed with --adoptRegistered, which already "+
			"creates each object owned by its device user")
	cmd.Flags().StringVar(&identityType, "identityType", registration.DefaultIdentityType,
		"External identity type the stamped owner is derived from")
	cmd.Flags().StringVar(&sessionValue, "session", "",
		"Session to write to: a path, a full file name including its extension, or a glob. "+
			"Defaults to the active session")

	_ = cmd.MarkFlagRequired("input-directory")
	cmd.MarkFlagsMutuallyExclusive("runId", "noFlag")
	// Both arrange the same thing by different means, and asking for both says
	// the caller does not know which order their credentials arrive in.
	cmd.MarkFlagsMutuallyExclusive("adoptRegistered", "stampDeviceOwner")

	return cmd
}

func runImport(ctx context.Context, stderr io.Writer, sessionValue, home string, opts importer.Options) error {
	s, _, err := session.Current(sessionValue, home)
	if err != nil {
		return err
	}
	client, err := c8yclient.New(s, session.Passphrase())
	if err != nil {
		return err
	}

	imp := importer.New(client, stderr, opts)

	// Before anything is created, so a refusal leaves the tenant untouched.
	if err := imp.CheckNotSameTenant(ctx, s); err != nil {
		return fmt.Errorf("%w", err)
	}
	return imp.Run(ctx)
}
