package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/c8yclient"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/export"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/migrate"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/session"
)

func newMigrateCommand(stderr io.Writer) *cobra.Command {
	var (
		rootID        string
		sourceSession string
		targetSession string
		childTypes    string
		noChildren    bool
		dataTypes     string
		dateFrom      string
		dateTo        string
		ignoreLimits  bool
		mappingFile   string
		incremental   bool
		resume        string
		keepStaging   bool
		noFlag        bool
		force         bool
	)

	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Migrate a device hierarchy from one tenant to another in one command",
		Long: "Migrate a device hierarchy from one tenant to another in one command.\n\n" +
			"Each side is pointed at its own session, so nothing has to be staged by hand\n" +
			"or switched between. Both are resolved and their credentials proved before\n" +
			"anything is created, and two sessions naming the same tenant are refused.\n\n" +
			"With --incremental the hierarchy moves first and then the data one device at\n" +
			"a time, so the staging directory never holds more than one device's worth.\n" +
			"That is also the only form that can be resumed: --resume needs the journal an\n" +
			"incremental run writes beside the mapping file.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, _ := cmd.Flags().GetString("sessionHome")

			// Checked here so a mistyped id costs nothing: the pre-flight
			// below makes authenticated calls to both tenants.
			if err := export.ValidateRootID(rootID); err != nil {
				return err
			}
			resolvedChildTypes, err := export.ResolveChildTypes(childTypes, noChildren)
			if err != nil {
				return err
			}
			resolvedDataTypes, err := export.ResolveDataTypes(dataTypes)
			if err != nil {
				return err
			}
			windows, err := export.ResolveWindows(dateFrom, dateTo, time.Now())
			if err != nil {
				return err
			}

			return runMigrate(cmd.Context(), stderr, os.Stdin,
				sourceSession, targetSession, home, migrate.Options{
					RootID:       rootID,
					MappingFile:  mappingFile,
					ChildTypes:   resolvedChildTypes,
					DataTypes:    resolvedDataTypes,
					Windows:      windows,
					IgnoreLimits: ignoreLimits,
					Incremental:  incremental,
					Resume:       resume,
					NoFlag:       noFlag,
					KeepStaging:  keepStaging,
					Force:        force,
				})
		},
	}

	cmd.Flags().StringVar(&rootID, "rootId", "", "ID of the root device to migrate (required)")
	cmd.Flags().StringVar(&sourceSession, "sourceSession", "", "Session to read from (required)")
	cmd.Flags().StringVar(&targetSession, "targetSession", "", "Session to write to (required)")
	cmd.Flags().StringVar(&childTypes, "childTypes", "asset,device",
		"Comma separated child relationship types to follow: asset, device, addition")
	cmd.Flags().BoolVar(&noChildren, "no-children", false, "Migrate only the root device")
	cmd.Flags().StringVar(&dataTypes, "dataTypes", "",
		"Comma separated device data to include: measurements, events, alarms, or all")
	cmd.Flags().StringVar(&dateFrom, "dateFrom", "",
		"Only include data at or after this time. Measurements default to the last 14 days")
	cmd.Flags().StringVar(&dateTo, "dateTo", "", "Only include data at or before this time")
	cmd.Flags().BoolVar(&ignoreLimits, "ignoreLimits", false,
		"Migrate even if an export limit is exceeded")
	cmd.Flags().StringVar(&mappingFile, "mapping", "id_mapping.json",
		"File to write the old ID to new ID mapping to")
	cmd.Flags().BoolVar(&incremental, "incremental", false,
		"Move the hierarchy first and then the data one device at a time, so the "+
			"staging directory only ever holds a single device's data")
	cmd.Flags().StringVar(&resume, "resume", "",
		"Continue the run with this id, skipping the devices whose data already "+
			"arrived. Implies --incremental")
	cmd.Flags().BoolVar(&keepStaging, "keepStaging", false,
		"Keep the staging directory instead of removing it")
	cmd.Flags().BoolVar(&noFlag, "noFlag", false,
		"Do not mark created managed objects with the run fragment")
	cmd.Flags().BoolVar(&force, "force", false, "Do not ask for confirmation")

	_ = cmd.MarkFlagRequired("rootId")
	_ = cmd.MarkFlagRequired("sourceSession")
	_ = cmd.MarkFlagRequired("targetSession")
	cmd.MarkFlagsMutuallyExclusive("childTypes", "no-children")

	return cmd
}

func runMigrate(ctx context.Context, stderr io.Writer, stdin io.Reader,
	sourceValue, targetValue, home string, opts migrate.Options) error {

	// The pre-flight resolves both sessions, refuses the same tenant twice and
	// proves both sets of credentials, before anything is created.
	checked, err := preflight(ctx, stderr, sourceValue, targetValue, home)
	if err != nil {
		return err
	}

	passphrase := session.Passphrase()
	source, err := c8yclient.New(checked.Source, passphrase)
	if err != nil {
		return err
	}
	target, err := c8yclient.New(checked.Target, passphrase)
	if err != nil {
		return err
	}

	err = migrate.New(source, target, checked.Source, checked.Target,
		stderr, stdin, opts).Run(ctx)

	if errors.Is(err, migrate.ErrAborted) {
		// Already explained on stderr.
		return &silentError{err: err}
	}
	if err != nil {
		return fmt.Errorf("%w", err)
	}
	return nil
}
