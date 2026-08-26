package service

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/tenants/tenantoptions"
)

// What an operator can change without redeploying, declared in the manifest
// under "settings" and stored as tenant options in SettingsCategory. The manifest
// declaration is what makes them appear in the Administration UI; reading them
// from the owner tenant is what makes them take effect.

// SettingsCategory is the tenant option category the manifest declares.
const SettingsCategory = "devicemigration"

// Setting keys, matching the manifest.
const (
	// SettingRetentionDays is how long a finished job's record is kept. Zero
	// keeps them forever, which is the safe default for a record of what was
	// done to a customer's data.
	SettingRetentionDays = "records.retention.days"

	// SettingDefaultDateFrom is the data window used when a migration does not
	// say. Empty leaves the engine's own defaults alone - measurements for the
	// last fourteen days, everything for events and alarms.
	SettingDefaultDateFrom = "data.dateFrom"
)

// Settings is the resolved configuration.
type Settings struct {
	RetentionDays   int
	DefaultDateFrom string
}

// settingsStore holds the last values read, so a request does not wait on a
// platform call to find out how it should behave.
type settingsStore struct {
	mu       sync.RWMutex
	settings Settings
}

func (s *settingsStore) get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

func (s *settingsStore) set(settings Settings) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = settings
}

// Settings is the configuration currently in force.
func (s *Service) Settings() Settings { return s.settings.get() }

// RefreshSettings reads the tenant options of the owner tenant.
//
// A missing option is not an error and not a warning: nothing is configured
// until somebody configures it, and the defaults are the documented behaviour. A
// value that cannot be read is worth a warning, because somebody meant something
// by it.
func (s *Service) RefreshSettings(ctx context.Context) error {
	owner, err := s.tenant(s.ownerTenant)
	if err != nil {
		return err
	}

	result := owner.client.Tenants.Options.ListByCategory(ctx,
		tenantoptions.ListByCategoryOptions{Category: SettingsCategory})
	if result.Err != nil {
		return fmt.Errorf("could not read the %s tenant options: %w",
			SettingsCategory, result.Err)
	}

	settings := Settings{}
	if raw, ok := result.Data[SettingRetentionDays]; ok && raw != "" {
		days, err := strconv.Atoi(raw)
		switch {
		case err != nil:
			slog.Warn("ignoring an unreadable setting",
				"key", SettingRetentionDays, "value", raw, "error", err)
		case days < 0:
			slog.Warn("ignoring a negative retention period",
				"key", SettingRetentionDays, "value", raw)
		default:
			settings.RetentionDays = days
		}
	}
	if raw, ok := result.Data[SettingDefaultDateFrom]; ok {
		settings.DefaultDateFrom = raw
	}

	s.settings.set(settings)
	return nil
}

// Prune deletes the records of jobs that finished longer ago than the retention
// period, and reports how many it removed.
//
// Only finished jobs, and only when a retention period is configured: a record
// is the only account of what was done to a customer's tenant, so deleting one
// has to be something somebody asked for.
func (s *Service) Prune(ctx context.Context) (int, error) {
	retention := s.Settings().RetentionDays
	if retention == 0 {
		return 0, nil
	}

	owner, err := s.tenant(s.ownerTenant)
	if err != nil {
		return 0, err
	}

	jobs, err := s.Records(ctx)
	if err != nil {
		return 0, err
	}

	cutoff := time.Now().AddDate(0, 0, -retention)
	pruned := 0
	for _, job := range jobs {
		if !job.Finished() || job.FinishedAt == "" || job.RecordID == "" {
			continue
		}

		finished, err := time.Parse("2006-01-02T15:04:05.000Z", job.FinishedAt)
		if err != nil {
			// An unparseable timestamp is not grounds for deletion.
			slog.Warn("keeping a record whose end time cannot be read",
				"job", job.ID, "finishedAt", job.FinishedAt)
			continue
		}
		if finished.After(cutoff) {
			continue
		}

		result := owner.client.ManagedObjects.Delete(ctx, job.RecordID, managedObjectDeleteOptions)
		if result.Err != nil {
			slog.Warn("could not delete an expired job record",
				"job", job.ID, "error", result.Err)
			continue
		}
		pruned++
		recordsPruned.Inc()
	}

	if pruned > 0 {
		slog.Info("pruned expired job records", "count", pruned, "retentionDays", retention)
	}
	return pruned, nil
}

// HousekeepingInterval is how often the settings are re-read and expired records
// are removed. Hourly: a retention period is measured in days, and an operator
// changing a setting can wait an hour or restart the container.
const HousekeepingInterval = time.Hour

// WatchHousekeeping re-reads the settings and prunes expired records until the
// context is done.
func (s *Service) WatchHousekeeping(ctx context.Context) {
	ticker := time.NewTicker(HousekeepingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.RefreshSettings(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("could not refresh the settings", "error", err)
			}
			if _, err := s.Prune(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("could not prune the job records", "error", err)
			}
		}
	}
}
