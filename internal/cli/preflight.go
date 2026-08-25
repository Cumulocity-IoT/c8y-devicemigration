package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/c8yclient"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/session"
)

// newPreflightCommand exposes the checks migrate runs before it creates
// anything. The bash extension has no such command - this is the one deliberate
// addition to the surface, because otherwise there is no way to find out whether
// session decryption and credentials work without starting a real migration.
func newPreflightCommand(stderr io.Writer) *cobra.Command {
	var sourceSession, targetSession string

	cmd := &cobra.Command{
		Use:   "preflight",
		Short: "Check that two sessions resolve, authenticate and are different tenants",
		Long: "Resolve both sessions, report the tenants they name, refuse two sessions\n" +
			"that point at the same tenant, and prove the credentials work with an\n" +
			"authenticated call.\n\n" +
			"The identity comes from reading the session files: host and tenant are\n" +
			"plaintext even when the credentials are encrypted, so that part needs no\n" +
			"passphrase, no network and no active session.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, _ := cmd.Flags().GetString("sessionHome")
			return runPreflight(cmd.Context(), stderr, sourceSession, targetSession, home)
		},
	}

	cmd.Flags().StringVar(&sourceSession, "sourceSession", "", "Session to read from (required)")
	cmd.Flags().StringVar(&targetSession, "targetSession", "", "Session to write to (required)")
	_ = cmd.MarkFlagRequired("sourceSession")
	_ = cmd.MarkFlagRequired("targetSession")

	return cmd
}

// Preflight is the result of a successful check, so callers can report what they
// are about to do without resolving anything twice.
type Preflight struct {
	Source, Target             *session.Session
	SourceTenant, TargetTenant string
}

func runPreflight(ctx context.Context, stderr io.Writer, sourceValue, targetValue, home string) error {
	result, err := preflight(ctx, stderr, sourceValue, targetValue, home)
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "Both sessions authenticate. Reported tenants: %s and %s\n",
		result.SourceTenant, result.TargetTenant)
	return nil
}

// preflight mirrors commands/migrate:283-353.
func preflight(ctx context.Context, stderr io.Writer, sourceValue, targetValue, home string) (*Preflight, error) {
	source, err := session.Load(sourceValue, home, "source")
	if err != nil {
		return nil, err
	}
	target, err := session.Load(targetValue, home, "target")
	if err != nil {
		return nil, err
	}

	if source.Host == "" && source.Tenant == "" {
		return nil, fmt.Errorf("The source session file names no host or tenant: %s", source.Path)
	}
	if target.Host == "" && target.Tenant == "" {
		return nil, fmt.Errorf("The target session file names no host or tenant: %s", target.Path)
	}

	sourceHost := session.NormalisedHost(source.Host)
	targetHost := session.NormalisedHost(target.Host)

	if source.SameTenantAs(target) {
		return nil, fmt.Errorf(
			"Source and target are the same tenant (%s at %s).\n"+
				"That would duplicate the hierarchy into the tenant it came from.\n"+
				"  source: %s\n"+
				"  target: %s",
			source.Tenant, sourceHost, source.Name(), target.Name())
	}

	// What was matched, not what was typed: a glob that resolved to something
	// unexpected is the failure this exists to make visible.
	fmt.Fprintf(stderr, "Migrating from %s (%s)\n", sourceHost, source.Tenant)
	fmt.Fprintf(stderr, "               to %s (%s)\n", targetHost, target.Tenant)
	fmt.Fprintf(stderr, "  source session: %s\n", source.Name())
	fmt.Fprintf(stderr, "  target session: %s\n", target.Name())

	passphrase := session.Passphrase()

	sourceTenant, err := probe(ctx, source, passphrase)
	if err != nil {
		return nil, fmt.Errorf("The source session does not authenticate. An expired token or an\n"+
			"encrypted session file with no C8Y_PASSPHRASE both look like this.\n  %w", err)
	}
	targetTenant, err := probe(ctx, target, passphrase)
	if err != nil {
		return nil, fmt.Errorf("The target session does not authenticate. An expired token or an\n"+
			"encrypted session file with no C8Y_PASSPHRASE both look like this.\n  %w", err)
	}

	return &Preflight{
		Source: source, Target: target,
		SourceTenant: sourceTenant, TargetTenant: targetTenant,
	}, nil
}

func probe(ctx context.Context, s *session.Session, passphrase string) (string, error) {
	client, err := c8yclient.New(s, passphrase)
	if err != nil {
		return "", err
	}
	return c8yclient.Probe(ctx, client)
}
