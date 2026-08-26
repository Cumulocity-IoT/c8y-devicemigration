package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/export"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/migrate"
)

// QueueDepth is how many migrations may be waiting. Small on purpose: a queue
// this service cannot work through in reasonable time is a queue whose entries
// will have gone stale, and refusing the eleventh request is more honest than
// accepting it and starting it in an hour.
const QueueDepth = 10

// JobKind is what a job does. Both kinds go through the same queue, so a revert
// can never run while a migration is half way through creating the objects it
// would be deleting.
type JobKind string

const (
	KindMigration JobKind = "MIGRATION"
	KindRevert    JobKind = "REVERT"
)

// JobStatus is where a job has got to.
type JobStatus string

const (
	StatusQueued  JobStatus = "QUEUED"
	StatusRunning JobStatus = "RUNNING"

	StatusSucceeded JobStatus = "SUCCEEDED"
	StatusFailed    JobStatus = "FAILED"
	StatusCancelled JobStatus = "CANCELLED"

	// StatusInterrupted is set on a record whose container died while it was
	// running. Nothing sets it during a run - it is what a later startup
	// concludes from finding a RUNNING record it does not own.
	StatusInterrupted JobStatus = "INTERRUPTED"
)

// MigrationRequest is a migration as asked for over HTTP.
//
// The fields mirror the CLI's flags, and the same parsers resolve them, so
// "asset,device" and "all" mean here exactly what they mean there. Two flags are
// deliberately absent: ignoreLimits, because the export limits are what keeps a
// container's ephemeral disk finite, and keepStaging, because there would be
// nowhere for it to be kept.
type MigrationRequest struct {
	SourceTenant string `json:"sourceTenant"`
	TargetTenant string `json:"targetTenant"`
	RootID       string `json:"rootId"`

	ChildTypes string `json:"childTypes,omitempty"`
	NoChildren bool   `json:"noChildren,omitempty"`

	DataTypes string `json:"dataTypes,omitempty"`
	DateFrom  string `json:"dateFrom,omitempty"`
	DateTo    string `json:"dateTo,omitempty"`

	NoFlag bool `json:"noFlag,omitempty"`
}

// RevertRequest undoes what a migration created.
//
// A run id is all the engine needs - it finds the objects by the marker fragment
// they carry, so nothing has to have survived on disk. JobID is the same thing
// spelled more conveniently: the run id and the target tenant are both on that
// job's record.
type RevertRequest struct {
	TargetTenant string `json:"targetTenant,omitempty"`
	RunID        string `json:"runId,omitempty"`
	JobID        string `json:"jobId,omitempty"`
}

// Job is one migration or one revert, and is also exactly what a job record
// holds.
type Job struct {
	ID     string    `json:"id"`
	Kind   JobKind   `json:"kind"`
	Status JobStatus `json:"status"`

	// Exactly one of these is set, according to Kind. Pointers so a record holds
	// the request it is actually about and not an empty copy of the other.
	Request *MigrationRequest `json:"request,omitempty"`
	Revert  *RevertRequest    `json:"revert,omitempty"`

	// RunID marks everything the run created in the target tenant, and is what
	// a revert needs. Learned from the engine's own output.
	RunID string `json:"runId,omitempty"`

	RequestedBy string `json:"requestedBy,omitempty"`

	QueuedAt   string `json:"queuedAt,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`

	// Error is the engine's failure, verbatim.
	Error string `json:"error,omitempty"`

	// Progress is the tail of what the engine printed.
	Progress []string `json:"progress,omitempty"`

	// CountsVerified is false when the run was cut short. The engine reports
	// what it believes it did, and a cancelled or interrupted run's beliefs are
	// not reliable - a cancelled import counts its unsent records as failures,
	// and a cancelled revert reports objects as deleted that are still there. So
	// a job that did not finish says so rather than presenting its numbers as
	// fact.
	CountsVerified bool `json:"countsVerified"`

	// RecordID is the managed object holding this job in the owner tenant. Not
	// stored in the record itself - it is the record.
	RecordID string `json:"-"`
}

// Finished is whether nothing more will happen to this job.
func (j *Job) Finished() bool {
	switch j.Status {
	case StatusSucceeded, StatusFailed, StatusCancelled, StatusInterrupted:
		return true
	default:
		return false
	}
}

// queuedJob is one job's live state.
//
// The Job inside it is written by the worker and by the progress reporter, and
// read by every request that asks after it, so it is guarded and only ever
// handed out as a copy. Sharing the Job itself is what made a freshly queued
// job report itself as already running: the response was being serialised while
// the worker was starting on the very same struct.
type queuedJob struct {
	mu     sync.Mutex
	job    Job
	cancel context.CancelFunc
}

// with applies a change to the job and returns the result.
func (q *queuedJob) with(change func(*Job)) Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	change(&q.job)
	return q.job.clone()
}

// snapshot is the job as it is now.
func (q *queuedJob) snapshot() Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.job.clone()
}

// stop cancels the job's context, if it has started.
func (q *queuedJob) stop() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.cancel != nil {
		q.cancel()
	}
}

// clone copies a job deeply enough that the copy cannot be changed underneath
// its reader - which for this struct means the progress lines.
func (j Job) clone() Job {
	copied := j
	copied.Progress = append([]string(nil), j.Progress...)
	return copied
}

// QueueFullError means the service is already holding as much work as it will.
type QueueFullError struct{}

func (e *QueueFullError) Error() string {
	return fmt.Sprintf("%d migrations are already queued, so this one was not accepted", QueueDepth)
}

// Enqueue validates a migration, records it, and queues it.
//
// Validation happens before the record is written, so a rejected request leaves
// nothing behind. Everything that can be checked without touching a tenant is
// checked here rather than in the worker, because the caller is still on the
// phone: a bad root id or an unsubscribed tenant should be a 400, not a job that
// fails a minute later.
func (s *Service) Enqueue(ctx context.Context, request MigrationRequest, caller *Caller) (*Job, error) {
	if _, err := s.resolveOptions(request, ""); err != nil {
		return nil, err
	}
	if _, err := s.Preflight(ctx, PreflightRequest{
		SourceTenant: request.SourceTenant,
		TargetTenant: request.TargetTenant,
	}); err != nil {
		return nil, err
	}

	job := Job{
		ID:       migrate.GenerateRunID(time.Now()),
		Kind:     KindMigration,
		Status:   StatusQueued,
		Request:  &request,
		QueuedAt: recordTime(time.Now()),
	}
	if caller != nil {
		job.RequestedBy = caller.Tenant + "/" + caller.Username
	}

	recordID, err := s.CreateRecord(ctx, &job)
	if err != nil {
		return nil, err
	}
	job.RecordID = recordID

	// Taken before the job is queued, so the response describes the job as it
	// was accepted. A snapshot from afterwards would be a race with the worker.
	accepted := job.clone()

	select {
	case s.queue <- &queuedJob{job: job}:
		return &accepted, nil
	default:
		// The record exists, so say what became of it rather than leaving a
		// QUEUED record for a job that will never run.
		job.Status = StatusFailed
		job.Error = (&QueueFullError{}).Error()
		job.FinishedAt = recordTime(time.Now())
		if err := s.UpdateRecord(ctx, &job); err != nil {
			slog.Warn("could not close the record of a rejected job", "job", job.ID, "error", err)
		}
		return nil, &QueueFullError{}
	}
}

// Work runs queued migrations until the context is done.
//
// One at a time, deliberately. The engine keeps its state in files whose paths
// the caller chooses, and its exporters and counters are single-use; two
// migrations at once would need two of everything and would still share the
// container's ephemeral disk. Sequential also means the container's memory and
// disk profile is the profile of one migration, which is what the manifest's
// resources are sized for.
func (s *Service) Work(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case queued := <-s.queue:
			s.run(ctx, queued)
		}
	}
}

// Cancel stops a running or queued job.
func (s *Service) Cancel(jobID string) error {
	s.mu.Lock()
	running, ok := s.running[jobID]
	s.mu.Unlock()

	if !ok {
		return &NotFoundError{What: fmt.Sprintf("running job %s", jobID)}
	}
	// Outside the service lock: stopping takes the job's own lock, and holding
	// both at once is how lock orders get learned the hard way.
	running.stop()
	return nil
}

// Running is the job being worked on, if any.
func (s *Service) Running() *Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, running := range s.running {
		snapshot := running.snapshot()
		return &snapshot
	}
	return nil
}

func (s *Service) run(parent context.Context, queued *queuedJob) {
	id := queued.snapshot().ID

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	queued.mu.Lock()
	queued.cancel = cancel
	queued.mu.Unlock()

	s.mu.Lock()
	s.running = map[string]*queuedJob{id: queued}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.running, id)
		s.mu.Unlock()
	}()

	started := queued.with(func(job *Job) {
		job.Status = StatusRunning
		job.StartedAt = recordTime(time.Now())
	})
	s.record(ctx, &started)

	var err error
	switch queued.snapshot().Kind {
	case KindRevert:
		err = s.revert(ctx, queued)
	default:
		err = s.migrate(ctx, queued)
	}

	finished := queued.with(func(job *Job) {
		job.FinishedAt = recordTime(time.Now())
		switch {
		case err == nil:
			job.Status = StatusSucceeded
			job.CountsVerified = true
		case errors.Is(err, context.Canceled), errors.Is(ctx.Err(), context.Canceled):
			job.Status = StatusCancelled
			job.Error = "cancelled part way through, so what reached the target tenant is not known: " +
				"check it with the run id before retrying"
		default:
			job.Status = StatusFailed
			job.Error = err.Error()
			// A failure the engine reported itself: it stopped where it says it
			// stopped, and its output up to that point is accurate.
			job.CountsVerified = true
		}
	})

	// Written with a context of its own: the job's context may be exactly what
	// was cancelled, and a cancelled job whose record still says RUNNING is the
	// one outcome nobody can act on.
	closing, closingCancel := context.WithTimeout(context.WithoutCancel(parent), recordTimeout)
	defer closingCancel()
	s.record(closing, &finished)

	slog.Info("job finished", "job", finished.ID, "status", finished.Status, "runId", finished.RunID)
}

// migrate is the part that reuses the engine unchanged.
func (s *Service) migrate(ctx context.Context, queued *queuedJob) error {
	job := queued.snapshot()

	if job.Request == nil {
		return errors.New("a migration job with no migration request")
	}

	source, err := s.tenant(job.Request.SourceTenant)
	if err != nil {
		return err
	}
	target, err := s.tenant(job.Request.TargetTenant)
	if err != nil {
		return err
	}

	directory, err := s.jobDirectory(job.ID)
	if err != nil {
		return err
	}
	// What was in the temporary directory before the run, so the export the
	// engine stages can be removed afterwards without touching anything else.
	before := stagingEntries()
	defer func() {
		if err := os.RemoveAll(directory); err != nil {
			slog.Warn("could not remove the job directory", "job", job.ID, "error", err)
		}
		sweepStaging(before, job.ID)
	}()

	options, err := s.resolveOptions(*job.Request, filepath.Join(directory, "id_mapping.json"))
	if err != nil {
		return err
	}

	output := newProgress(job.ID)
	defer output.Flush()

	// Progress is also written to the record as it arrives, so a long migration
	// can be followed without reading the container log.
	stopReporting := s.reportProgress(ctx, queued, output)
	defer stopReporting()

	// nil stdin: Force is set, so the confirmation is never reached. The engine
	// treats a nil reader as a refusal rather than blocking, which is the safe
	// way round if Force were ever lost.
	err = migrate.New(source.client, target.client, source.session, target.session,
		output, nil, options).Run(ctx)

	output.Flush()
	queued.with(func(job *Job) {
		job.RunID = output.RunID()
		job.Progress = output.Lines()
	})
	return err
}

// resolveOptions turns a request into engine options, using the CLI's own
// parsers so the two accept exactly the same values.
func (s *Service) resolveOptions(request MigrationRequest, mappingFile string) (migrate.Options, error) {
	if request.RootID == "" {
		return migrate.Options{}, &BadRequestError{Reason: "rootId is required"}
	}
	if err := export.ValidateRootID(request.RootID); err != nil {
		return migrate.Options{}, &BadRequestError{Reason: err.Error()}
	}

	if request.ChildTypes != "" && request.NoChildren {
		return migrate.Options{}, &BadRequestError{
			Reason: "childTypes and noChildren cannot both be given"}
	}

	// An omitted childTypes means the default, exactly as the CLI's flag default
	// does. Derived from the engine's own default rather than repeated, so the
	// two cannot drift.
	wanted := request.ChildTypes
	if wanted == "" && !request.NoChildren {
		wanted = defaultChildTypes()
	}

	childTypes, err := export.ResolveChildTypes(wanted, request.NoChildren)
	if err != nil {
		return migrate.Options{}, &BadRequestError{Reason: err.Error()}
	}
	dataTypes, err := export.ResolveDataTypes(request.DataTypes)
	if err != nil {
		return migrate.Options{}, &BadRequestError{Reason: err.Error()}
	}
	windows, err := export.ResolveWindows(request.DateFrom, request.DateTo, time.Now())
	if err != nil {
		return migrate.Options{}, &BadRequestError{Reason: err.Error()}
	}

	return migrate.Options{
		RootID:      request.RootID,
		MappingFile: mappingFile,
		ChildTypes:  childTypes,
		DataTypes:   dataTypes,
		Windows:     windows,
		NoFlag:      request.NoFlag,

		// Always incremental: it is what bounds the staging directory to one
		// device's data, and a container's disk is not something to gamble with.
		Incremental: true,

		// Nobody is at a terminal to answer, and the caller already confirmed by
		// making the request.
		Force: true,
	}, nil
}

// defaultChildTypes is what --childTypes defaults to on the command line.
func defaultChildTypes() string {
	names := make([]string, 0, len(export.DefaultChildTypes))
	for _, childType := range export.DefaultChildTypes {
		names = append(names, string(childType))
	}
	return strings.Join(names, ",")
}

// stagingPrefix is how migrate names the directory it stages an export in.
//
// Knowing this is unfortunate but necessary: migrate creates it with
// os.MkdirTemp("", ...), keeps it deliberately when a run fails, and offers no
// way to say where it should live or what it was called. A container that ran
// for months would otherwise accumulate one abandoned export per failure. The
// sweep below is prefix-matched, so if this ever changed the leftovers would
// come back - which is what TestAFailedJobAlsoLeavesNothingOnDisk exists to
// notice, since it looks for exported data rather than for this name.
const stagingPrefix = "c8y-devicemigration-staging."

// jobDirectory makes the directory a job works in.
//
// Only the mapping and its journal go here - the engine stages its export in a
// directory of its own choosing under the process temporary directory, which is
// what sweepStaging deals with. Setting TMPDIR instead would be neater and is
// what the first version did, but it changes the temporary directory of the
// whole process from a worker goroutine, which is not a thing a library should
// do to its host.
func (s *Service) jobDirectory(jobID string) (string, error) {
	directory := filepath.Join(os.TempDir(), "job-"+jobID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("could not make a working directory for job %s: %w", jobID, err)
	}
	return directory, nil
}

// stagingEntries names what is in the temporary directory now, so what a job
// leaves behind can be told from what was already there.
func stagingEntries() map[string]bool {
	present := map[string]bool{}
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return present
	}
	for _, entry := range entries {
		present[entry.Name()] = true
	}
	return present
}

// sweepStaging removes the staging directories that appeared while a job ran.
//
// Only new ones, and only ones with the engine's prefix, so nothing that was
// already there can be caught by it. One job runs at a time, so anything new is
// this job's.
func sweepStaging(before map[string]bool, jobID string) {
	for name := range stagingEntries() {
		if before[name] || !strings.HasPrefix(name, stagingPrefix) {
			continue
		}
		path := filepath.Join(os.TempDir(), name)
		if err := os.RemoveAll(path); err != nil {
			slog.Warn("could not remove a staging directory", "job", jobID, "path", path, "error", err)
		}
	}
}

// reportProgress copies the running job's output into its record periodically,
// and returns a function that stops doing so.
func (s *Service) reportProgress(ctx context.Context, queued *queuedJob, output *progress) func() {
	ticker := time.NewTicker(ProgressInterval)
	done := make(chan struct{})
	var once sync.Once

	go func() {
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				latest := queued.with(func(job *Job) {
					job.RunID = output.RunID()
					job.Progress = output.Lines()
				})
				s.record(ctx, &latest)
			}
		}
	}()

	return func() {
		once.Do(func() {
			ticker.Stop()
			close(done)
		})
	}
}

// record updates a job's record, reporting rather than propagating a failure:
// bookkeeping must not be able to abort a migration.
func (s *Service) record(ctx context.Context, job *Job) {
	if err := s.UpdateRecord(ctx, job); err != nil {
		slog.Warn("could not update a job record", "job", job.ID, "error", err)
	}
}
