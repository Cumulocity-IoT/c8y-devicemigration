package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/migrate"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/reverter"
)

// Undoing a migration is the one operation here that destroys something, and it
// is also the one that needs nothing but a run id: every object a run created
// carries a marker fragment naming it, so the engine finds them by asking the
// target tenant rather than by reading a file. That is what makes it possible
// from a container that has no memory of the run at all.

// EnqueueRevert validates a revert, records it, and queues it.
func (s *Service) EnqueueRevert(ctx context.Context, request RevertRequest, caller *Caller) (*Job, error) {
	resolved, err := s.resolveRevert(ctx, request)
	if err != nil {
		return nil, err
	}

	// Proves the credentials before anything is recorded, the same way a
	// migration does - and through the same seam, so a remote target is proved
	// by its certificate and an expired one is refused here rather than halfway
	// through deleting things.
	if _, err := s.targetClient(resolved.RemoteTargetID, resolved.TargetTenant); err != nil {
		return nil, err
	}

	// An id of its own, like a migration's: two attempts at undoing the same run
	// are two jobs with two outcomes, and giving them one id would mean one
	// record telling half of each story.
	job := Job{
		ID:       migrate.GenerateRunID(time.Now()),
		Kind:     KindRevert,
		Status:   StatusQueued,
		Revert:   &resolved,
		RunID:    resolved.RunID,
		QueuedAt: recordTime(time.Now()),
	}
	if caller != nil {
		job.RequestedBy = caller.Name()
	}

	recordID, err := s.CreateRecord(ctx, &job)
	if err != nil {
		return nil, err
	}
	job.RecordID = recordID

	accepted := job.clone()

	select {
	case s.queue <- &queuedJob{job: job}:
		queueDepth.Set(float64(len(s.queue)))
		return &accepted, nil
	default:
		job.Status = StatusFailed
		job.Error = (&QueueFullError{}).Error()
		job.FinishedAt = recordTime(time.Now())
		if err := s.UpdateRecord(ctx, &job); err != nil {
			slog.Warn("could not close the record of a rejected revert", "job", job.ID, "error", err)
		}
		return nil, &QueueFullError{}
	}
}

// resolveRevert fills in what the caller left out.
//
// A job id is the convenient form: the run id and the tenant it was migrated
// into are both on that job's record, so an operator who has just watched a
// migration go wrong does not have to copy anything out of it.
func (s *Service) resolveRevert(ctx context.Context, request RevertRequest) (RevertRequest, error) {
	if request.JobID != "" {
		job, err := s.Record(ctx, request.JobID)
		if err != nil {
			return request, err
		}
		if job.Kind == KindRevert {
			return request, &BadRequestError{Reason: fmt.Sprintf(
				"%s is itself a revert, so there is nothing to undo about it", request.JobID)}
		}
		if job.RunID == "" {
			return request, &BadRequestError{Reason: fmt.Sprintf(
				"%s has no run id, so it stopped before one was announced and created "+
					"nothing to undo", request.JobID)}
		}
		// An unmarked run is findable only through its mapping file, and that
		// lived in a job directory which was removed when the job ended. Undoing
		// it means the command line tool and the mapping - so say that, rather
		// than accepting a revert that would delete nothing and report success.
		if job.Request != nil && job.Request.NoFlag {
			return request, &BadRequestError{Reason: fmt.Sprintf(
				"%s was migrated with noFlag, so nothing it created carries a marker and "+
					"a run id cannot find it. Undo it with the command line tool and that "+
					"run's mapping file", request.JobID)}
		}

		// A remote-target migration is undone with the certificate that made it,
		// for as long as that certificate lives. The same one, necessarily: a
		// device token deletes only what it owns, and what this run created is
		// owned by the identity that created it, so a fresh certificate would be
		// a different device user owning nothing.
		//
		// Whether the certificate is still alive is decided further down, where
		// the client is built - one place, one answer, and the same one the
		// listing shows.
		if job.Request != nil {
			request.RemoteTargetID = job.Request.RemoteTargetID
		}
		if request.RunID != "" && request.RunID != job.RunID {
			return request, &BadRequestError{Reason: fmt.Sprintf(
				"jobId %s ran as %s, but runId says %s",
				request.JobID, job.RunID, request.RunID)}
		}

		request.RunID = job.RunID
		if request.TargetTenant == "" && job.Request != nil {
			request.TargetTenant = job.Request.TargetTenant
		}
	}

	if request.RunID == "" {
		return request, &BadRequestError{Reason: "runId or jobId is required"}
	}
	if request.TargetTenant == "" {
		return request, &BadRequestError{
			Reason: "targetTenant is required unless jobId says which tenant to undo in"}
	}
	if err := reverter.ValidateRunID(request.RunID); err != nil {
		return request, &BadRequestError{Reason: err.Error()}
	}
	return request, nil
}

// revert is the part that reuses the engine unchanged.
func (s *Service) revert(ctx context.Context, queued *queuedJob) error {
	job := queued.snapshot()
	if job.Revert == nil {
		return errors.New("a revert job with no revert request")
	}

	target, err := s.targetClient(job.Revert.RemoteTargetID, job.Revert.TargetTenant)
	if err != nil {
		return err
	}

	output := newProgress(job.ID)
	defer output.Flush()

	stopReporting := s.reportProgress(ctx, queued, output)
	defer stopReporting()

	// Force, because nobody is at a terminal to confirm a deletion; nil stdin
	// for the same reason. By run id rather than by mapping file: the mapping
	// lived in a job directory that was removed when that job ended.
	err = reverter.New(target.client, output, nil, reverter.Options{
		RunID: job.Revert.RunID,
		Force: true,
	}).Run(ctx)

	output.Flush()
	queued.with(func(job *Job) {
		job.Progress = output.Lines()
	})

	// An incomplete revert is a failure worth naming: some objects are gone and
	// some are not, and the difference is in the output.
	if errors.Is(err, reverter.ErrIncomplete) {
		return fmt.Errorf("%w - the objects that survived are named above", err)
	}
	return err
}
