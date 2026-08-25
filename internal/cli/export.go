package cli

import (
	"context"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/c8yclient"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/export"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/session"
)

func newExportCommand(stderr io.Writer) *cobra.Command {
	var (
		rootID          string
		outputDirectory string
		childTypes      string
		noChildren      bool
		dataTypes       string
		dateFrom        string
		dateTo          string
		metadataOnly    bool
		dataOnly        bool
		ignoreLimits    bool
		sessionValue    string
	)

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Write a device hierarchy and its external identities to disk",
		Long: "Write a device hierarchy and its external identities to disk.\n\n" +
			"The migration includes all properties and fragments of the concerned managed\n" +
			"objects and all external identities. Device protocols are not migrated.\n\n" +
			"Without --session the active go-c8y-cli session is used, so this works\n" +
			"straight after:\n\n" +
			"  eval \"$(c8y sessions set --sessionFilter my-tenant)\"\n\n" +
			"That route needs no C8Y_PASSPHRASE, because an activated session exports its\n" +
			"credentials already decrypted. With --session the session file is read\n" +
			"directly and the ambient C8Y_* variables are ignored.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, _ := cmd.Flags().GetString("sessionHome")

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

			return runExport(cmd.Context(), stderr, sessionValue, home, export.Options{
				RootID:          rootID,
				OutputDirectory: outputDirectory,
				ChildTypes:      resolvedChildTypes,
				DataTypes:       resolvedDataTypes,
				Windows:         windows,
				IgnoreLimits:    ignoreLimits,
				MetadataOnly:    metadataOnly,
				DataOnly:        dataOnly,
			})
		},
	}

	cmd.Flags().StringVar(&rootID, "rootId", "", "ID of the root device to export (required)")
	cmd.Flags().StringVar(&outputDirectory, "output-directory", "", "Output directory for the export (required)")
	cmd.Flags().StringVar(&childTypes, "childTypes", "asset,device",
		"Comma separated child relationship types to follow: asset, device, addition")
	cmd.Flags().BoolVar(&noChildren, "no-children", false, "Export only the root device")
	cmd.Flags().StringVar(&dataTypes, "dataTypes", "",
		"Comma separated device data to include: measurements, events, alarms, or all")
	cmd.Flags().StringVar(&dateFrom, "dateFrom", "",
		"Only export data at or after this time, absolute or relative, e.g. "+
			"2026-01-01T00:00:00Z or -30d. Measurements default to the last 14 days; "+
			"events and alarms default to the whole history")
	cmd.Flags().StringVar(&dateTo, "dateTo", "", "Only export data at or before this time")
	cmd.Flags().BoolVar(&metadataOnly, "metadataOnly", false,
		"Export managed objects and external identities only, skipping the data itself. "+
			"The limits are still checked against whatever --dataTypes selects")
	cmd.Flags().BoolVar(&dataOnly, "dataOnly", false,
		"Export the data only, skipping external identities")
	cmd.Flags().BoolVar(&ignoreLimits, "ignoreLimits", false, "Export even if a limit is exceeded")
	cmd.Flags().StringVar(&sessionValue, "session", "",
		"Session to read from: a path, a full file name including its extension, or a glob. "+
			"Defaults to the active session")

	_ = cmd.MarkFlagRequired("rootId")
	_ = cmd.MarkFlagRequired("output-directory")
	cmd.MarkFlagsMutuallyExclusive("childTypes", "no-children")
	cmd.MarkFlagsMutuallyExclusive("metadataOnly", "dataOnly")

	return cmd
}

func runExport(ctx context.Context, stderr io.Writer, sessionValue, home string, opts export.Options) error {
	// Nothing is printed about which session was chosen: commands/export prints
	// nothing either, and the two outputs are compared byte for byte. Use
	// preflight to see what a session resolves to.
	s, _, err := session.Current(sessionValue, home)
	if err != nil {
		return err
	}
	client, err := c8yclient.New(s, session.Passphrase())
	if err != nil {
		return err
	}
	return export.New(client, stderr, opts).Run(ctx)
}
