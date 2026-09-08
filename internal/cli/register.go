package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/c8yclient"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/importer"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/registration"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/session"
)

func newRegisterCommand(stderr io.Writer) *cobra.Command {
	var (
		inputDirectory  string
		runID           string
		noFlag          bool
		identityType    string
		credentialsFile string
		dryRun          bool
		sessionValue    string
	)

	cmd := &cobra.Command{
		Use:   "register",
		Short: "Provision device users for an export, before importing it",
		Long: "Provision device users for an export, before importing it.\n\n" +
			"This is the middle step of a migration that carries credentials, and it has\n" +
			"to run BEFORE the import. Bulk registration creates each managed object\n" +
			"owned by the device user it creates alongside it, and `import\n" +
			"--adoptRegistered` then fills that object in. Registering after an import\n" +
			"cannot give a device ownership of an object that already exists, so it only\n" +
			"rotates a password and leaves the object alone.\n\n" +
			"A device is registered under its c8y_Serial external identity, or under its\n" +
			"only identity when it has no c8y_Serial. A device with several identities and\n" +
			"no c8y_Serial is reported and skipped rather than guessed at; --identityType\n" +
			"chooses for the whole run. Groups and other non-device objects are skipped:\n" +
			"they have no device user.\n\n" +
			"The credentials are written to --credentialsFile and exist nowhere else.\n" +
			"Registering the same device again ROTATES its password, which invalidates any\n" +
			"file handed out earlier.\n\n" +
			"Pass the same --runId to the import, so it can tell the objects this\n" +
			"registration created from anything else the target already holds.\n\n" +
			"Without --session the active go-c8y-cli session is used.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, _ := cmd.Flags().GetString("sessionHome")
			return runRegister(cmd.Context(), stderr, sessionValue, home, registerOptions{
				inputDirectory:  inputDirectory,
				runID:           runID,
				noFlag:          noFlag,
				identityType:    identityType,
				credentialsFile: credentialsFile,
				dryRun:          dryRun,
			})
		},
	}

	cmd.Flags().StringVar(&inputDirectory, "input-directory", "",
		"Directory holding the export (required)")
	cmd.Flags().StringVar(&runID, "runId", "",
		"Stamp the run marker onto every registered object, so the import can "+
			"recognise them. Letters, digits and underscores only. One is generated "+
			"and reported if this is not given")
	cmd.Flags().BoolVar(&noFlag, "noFlag", false,
		"Do not stamp the run marker. The import then cannot recognise these "+
			"objects and will create its own, so this is only useful for registering "+
			"devices without importing anything")
	cmd.Flags().StringVar(&identityType, "identityType", registration.DefaultIdentityType,
		"External identity type to register each device under")
	cmd.Flags().StringVar(&credentialsFile, "credentialsFile", "",
		"File to write the credentials to (required). Never stdout: the file holds "+
			"working device passwords")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Report which devices would be registered and which would be skipped, "+
			"without registering anything or generating a password")
	cmd.Flags().StringVar(&sessionValue, "session", "",
		"Session to register in: a path, a full file name including its extension, "+
			"or a glob. Defaults to the active session")

	_ = cmd.MarkFlagRequired("input-directory")
	cmd.MarkFlagsMutuallyExclusive("runId", "noFlag")
	cmd.MarkFlagsMutuallyExclusive("credentialsFile", "dry-run")

	return cmd
}

type registerOptions struct {
	inputDirectory  string
	runID           string
	noFlag          bool
	identityType    string
	credentialsFile string
	dryRun          bool
}

func runRegister(ctx context.Context, stderr io.Writer, sessionValue, home string, opts registerOptions) error {
	// Checked before the session is resolved, because a run that cannot write
	// its credentials must not create the device users first: the passwords
	// would exist only in this process and be gone when it exits.
	if !opts.dryRun && opts.credentialsFile == "" {
		return fmt.Errorf(
			"--credentialsFile is required: registering creates a password per device, and\n" +
				"those passwords are not recorded anywhere else. Use --dry-run to see what\n" +
				"would be registered without creating anything")
	}
	if err := importer.ValidateRunID(opts.runID); err != nil {
		return err
	}

	s, _, err := session.Current(sessionValue, home)
	if err != nil {
		return err
	}
	client, err := c8yclient.New(s, session.Passphrase())
	if err != nil {
		return err
	}

	marker := ""
	if !opts.noFlag {
		runID := opts.runID
		if runID == "" {
			runID = importer.GenerateRunID(time.Now())
		}
		marker = importer.MarkerPrefix + runID
		fmt.Fprintf(stderr,
			"Run id %s, marking registered objects with %s\n"+
				"Pass --runId %s to the import so it recognises them\n",
			runID, marker, runID)
	}

	registrar := registration.New(client, stderr, registration.Options{
		InputDirectory: opts.inputDirectory,
		Marker:         marker,
		IdentityType:   opts.identityType,
		Tenant:         s.Tenant,
	})

	if opts.dryRun {
		return reportRegisterPlan(stderr, registrar)
	}

	// Opened before the registration, not after. A file that cannot be created -
	// a read-only directory, a bad path - has to fail while nothing has been
	// created in the tenant, because the passwords only exist in memory.
	file, err := os.OpenFile(opts.credentialsFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("cannot write the credentials file: %w", err)
	}
	defer func() { _ = file.Close() }()

	credentials, runErr := registrar.Run(ctx)
	// Written even on a partial failure: the devices that did register have
	// working credentials, and this response is the only copy.
	if len(credentials) > 0 {
		if err := registration.WriteCredentialsFile(file, credentials); err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("cannot write the credentials file: %w", err)
		}
		fmt.Fprintf(stderr, "Credentials for %d %s written to %s\n",
			len(credentials), plural(len(credentials), "device", "devices"), opts.credentialsFile)
		fmt.Fprintln(stderr,
			"This is the only copy: the passwords are not recorded anywhere else, and re-issuing them rotates them.")
	}
	return runErr
}

// reportRegisterPlan prints what a registration would do.
func reportRegisterPlan(stderr io.Writer, registrar *registration.Registrar) error {
	plan, err := registrar.Plan()
	if err != nil {
		return err
	}
	for _, record := range plan.Records {
		fmt.Fprintf(stderr, "Would register %s as %s:%s\n",
			displayName(record.Name), record.ExternalType, record.ExternalID)
	}
	for _, skipped := range plan.Skipped {
		fmt.Fprintf(stderr, "Would skip %s (%s): %s\n",
			displayName(skipped.Name), skipped.SourceID, skipped.Reason)
	}
	fmt.Fprintf(stderr, "%d %s would be registered, %d skipped. Nothing was created.\n",
		len(plan.Records), plural(len(plan.Records), "device", "devices"), len(plan.Skipped))
	return nil
}

func displayName(name string) string {
	if name == "" {
		return "<unnamed>"
	}
	return name
}

func plural(n int, singular, many string) string {
	if n == 1 {
		return singular
	}
	return many
}
