// Command c8y-devicemigration-service runs the migration engine as a
// multi-tenant Cumulocity microservice.
//
// It is deployed on a parent tenant and subscribed to the tenants it migrates
// between, so the platform hands it a service user for each one and no session
// file or passphrase is involved. The migration logic is the same code the
// c8y-devicemigration CLI runs.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/service"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/version"
)

// shutdownGrace is how long in-flight requests get once a signal arrives. Jobs
// are cancelled through their own context, so this only covers HTTP.
const shutdownGrace = 20 * time.Second

func main() {
	if err := run(); err != nil {
		// Everything the platform shows in the microservice log comes from
		// stdout/stderr, so a startup failure has to be readable there.
		fmt.Fprintf(os.Stderr, "c8y-devicemigration-service: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Before anything logs, so the level is one the /loggers endpoints can
	// change afterwards.
	service.SetupLogging()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	svc, err := service.New(ctx, service.Options{})
	if err != nil {
		return err
	}

	slog.Info("microservice ready",
		"version", version.Version,
		"ownerTenant", svc.OwnerTenant(),
		"subscribedTenants", svc.Tenants(ctx))

	// Before any new work is accepted: a record still saying RUNNING belongs to
	// a container that no longer exists, and leaving it that way hides the fact
	// that something was interrupted part way through.
	if err := svc.Reconcile(ctx); err != nil {
		slog.Warn("could not reconcile the job records", "error", err)
	}

	if pruned, err := svc.Prune(ctx); err != nil {
		slog.Warn("could not prune expired job records", "error", err)
	} else if pruned > 0 {
		slog.Info("pruned expired job records", "count", pruned)
	}

	go svc.WatchSubscriptions(ctx)
	go svc.WatchHousekeeping(ctx)
	// Its own loop: uncollected device credentials are held for an hour, and
	// sweeping them on the hourly housekeeping turn would keep them for two.
	go svc.WatchCredentialExpiry(ctx)

	// One worker, so one migration at a time. See Service.Work.
	go svc.Work(ctx)

	server := &http.Server{
		Addr:    ":" + svc.Port(),
		Handler: svc.Handler(),

		// The platform caps a request at 15 minutes, so nothing here should
		// outlive that. Long work belongs in a job, not in a request.
		ReadHeaderTimeout: 20 * time.Second,
	}

	errs := make(chan error, 1)
	go func() {
		slog.Info("listening", "address", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdown, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}
