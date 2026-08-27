package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/inventory/managedobjects"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/pagination"
)

// RecordType is the managed object type every job record carries. Job records
// live in the owner tenant because a microservice has no storage of its own: the
// container can be replaced at any moment, and the record is the only thing that
// then still says what a run created.
const RecordType = "c8y_DeviceMigrationJob"

// RecordFragment holds the job itself. Keeping it in one fragment means a record
// can be read back with one query and no field-by-field mapping.
const RecordFragment = "c8y_DeviceMigration"

// managedObjectDeleteOptions is how a job record is deleted: on its own. A
// record has no children, and cascade on something that unexpectedly does would
// be a very bad surprise.
var managedObjectDeleteOptions = managedobjects.DeleteOptions{}

// recordPageSize is deliberately large: job records are small and a listing that
// silently returned only the first page would be worse than a slow one.
const recordPageSize = 100

// record is the managed object body of a job record.
type record struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Job  *Job   `json:"c8y_DeviceMigration"`
}

// CreateRecord writes a job record to the owner tenant and returns its id.
func (s *Service) CreateRecord(ctx context.Context, job *Job) (string, error) {
	owner, err := s.tenant(s.ownerTenant)
	if err != nil {
		return "", err
	}

	body, err := json.Marshal(record{
		Name: fmt.Sprintf("Migration %s", job.ID),
		Type: RecordType,
		Job:  job,
	})
	if err != nil {
		return "", err
	}

	result := owner.client.ManagedObjects.Create(ctx, body)
	if result.Err != nil {
		return "", fmt.Errorf("could not record job %s in %s: %w", job.ID, s.ownerTenant, result.Err)
	}
	return result.Data.ID(), nil
}

// UpdateRecord writes the job's current state over its record.
//
// The whole fragment is replaced rather than patched, so the record always
// matches the job in memory. A failure here is reported but never fails the
// migration: losing the bookkeeping is much less bad than abandoning a run that
// is part way through creating objects in a customer's tenant.
func (s *Service) UpdateRecord(ctx context.Context, job *Job) error {
	if job.RecordID == "" {
		return nil
	}

	owner, err := s.tenant(s.ownerTenant)
	if err != nil {
		return err
	}

	body, err := json.Marshal(map[string]any{RecordFragment: job})
	if err != nil {
		return err
	}

	result := owner.client.ManagedObjects.Update(ctx, job.RecordID, body)
	if result.Err != nil {
		return fmt.Errorf("could not update the record of job %s: %w", job.ID, result.Err)
	}
	return nil
}

// Records lists the job records in the owner tenant, newest first.
func (s *Service) Records(ctx context.Context) ([]*Job, error) {
	owner, err := s.tenant(s.ownerTenant)
	if err != nil {
		return nil, err
	}

	// The condition goes inside the query, never in ListOptions.Type: the
	// platform ignores type and fragmentType whenever a query is present, and
	// ListAll always adds one for keyset paging. Asking by type here would
	// quietly return every managed object in the tenant.
	iterator := owner.client.ManagedObjects.ListAll(ctx, managedobjects.ListOptions{
		Query:             fmt.Sprintf("$filter=has(%s)", RecordFragment),
		PaginationOptions: pagination.PaginationOptions{PageSize: recordPageSize},
	})

	var jobs []*Job
	for object, err := range iterator.Items() {
		if err != nil {
			return nil, fmt.Errorf("could not read the job records: %w", err)
		}

		job, ok := decodeRecord(object.Bytes())
		if !ok {
			// A managed object carrying this fragment that this service did not
			// write, or wrote in an older shape. Skipped rather than fatal: one
			// unreadable record must not hide all the others.
			continue
		}
		job.RecordID = object.ID()
		jobs = append(jobs, job)
	}
	if err := iterator.Err(); err != nil {
		return nil, fmt.Errorf("could not read the job records: %w", err)
	}

	// Newest first, which is what someone looking for "what just happened"
	// wants. Ids are time-ordered, so this needs no timestamp parsing.
	for i, j := 0, len(jobs)-1; i < j; i, j = i+1, j-1 {
		jobs[i], jobs[j] = jobs[j], jobs[i]
	}
	return jobs, nil
}

// Record reads one job record by job id.
//
// Asked for by id in the query rather than by reading every record and filtering
// here: polling a job's status is the most frequent call this service gets, and
// on a tenant with a few hundred jobs the filtering version reads all of them
// every time. The nested path is how the inventory query language addresses a
// property inside a fragment, the same shape as the documented
// c8y_Hardware.serialNumber.
func (s *Service) Record(ctx context.Context, jobID string) (*Job, error) {
	owner, err := s.tenant(s.ownerTenant)
	if err != nil {
		return nil, err
	}

	iterator := owner.client.ManagedObjects.ListAll(ctx, managedobjects.ListOptions{
		Query: fmt.Sprintf("$filter=(has(%s)) and (%s.id eq '%s')",
			RecordFragment, RecordFragment, jobID),
		PaginationOptions: pagination.PaginationOptions{PageSize: recordPageSize},
	})

	for object, err := range iterator.Items() {
		if err != nil {
			return nil, fmt.Errorf("could not read the record of job %s: %w", jobID, err)
		}
		job, ok := decodeRecord(object.Bytes())
		if !ok || job.ID != jobID {
			// The query decides what matches, but a platform that ignored the
			// filter would otherwise hand back somebody else's job.
			continue
		}
		job.RecordID = object.ID()
		return job, nil
	}
	if err := iterator.Err(); err != nil {
		return nil, fmt.Errorf("could not read the record of job %s: %w", jobID, err)
	}

	return nil, &NotFoundError{What: fmt.Sprintf("job %s", jobID)}
}

// decodeRecord reads a job out of a managed object.
func decodeRecord(body []byte) (*Job, bool) {
	var document struct {
		Job *Job `json:"c8y_DeviceMigration"`
	}
	if err := json.Unmarshal(body, &document); err != nil || document.Job == nil {
		return nil, false
	}
	return document.Job, true
}

// NotFoundError is a job id nobody has heard of.
type NotFoundError struct {
	What string
}

func (e *NotFoundError) Error() string { return e.What + " does not exist" }

// recordTime formats a timestamp the way Cumulocity does, so a record reads the
// same as everything else in the tenant.
func recordTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// OfKind is the records of one kind, newest first.
//
// A record written before jobs had kinds is a migration: it is the only thing
// that existed, and treating it as one keeps an older tenant's history readable
// rather than hiding it.
func (s *Service) OfKind(ctx context.Context, kind JobKind) ([]*Job, error) {
	jobs, err := s.Records(ctx)
	if err != nil {
		return nil, err
	}

	var wanted []*Job
	for _, job := range jobs {
		actual := job.Kind
		if actual == "" {
			actual = KindMigration
		}
		if actual == kind {
			wanted = append(wanted, job)
		}
	}
	return wanted, nil
}

// SettleStale closes the records of jobs that claim to be running when nothing
// is.
//
// At most one job runs at a time and this container knows which one, and the
// manifest pins replicas to 1 - so a record saying RUNNING that is not that job
// belongs to a container that no longer exists. Reconcile does this once at
// startup; this is the same reasoning applied while running, for the container
// that was replaced while somebody had the list open in front of them.
//
// Without it the row sits there saying RUNNING with a Cancel button that answers
// "running job ... does not exist", which is true and useless. The listing is
// what calls this, so the row settles itself within one refresh.
//
// QUEUED records are deliberately left alone: a job queued in this container has
// one of those too, and from here the two cannot be told apart. Startup can tell
// - nothing is queued then - which is why Reconcile closes those and this does
// not.
func (s *Service) SettleStale(ctx context.Context, jobs []*Job) {
	live := ""
	if running := s.Running(); running != nil {
		live = running.ID
	}

	for _, job := range jobs {
		if job.Status != StatusRunning || job.ID == live {
			continue
		}

		job.Status = StatusInterrupted
		job.CountsVerified = false
		job.FinishedAt = recordTime(time.Now())
		job.Error = "the microservice was replaced while this job was running, so what it " +
			"reached is not known: check the target tenant by run id"

		if err := s.UpdateRecord(ctx, job); err != nil {
			// Best effort: a listing must still answer. The record keeps saying
			// RUNNING and the next refresh tries again.
			slog.Warn("could not close a stale running job's record", "job", job.ID, "error", err)
			continue
		}
		slog.Info("closed a job left running by a replaced container",
			"job", job.ID, "runId", job.RunID)
	}
}

// Reconcile closes the records of jobs that were running when this container
// stopped existing.
//
// A microservice has no persistent storage and no way to resume: whatever the
// engine was part way through is over, and the container that comes back knows
// nothing about it. What it can do is stop the record claiming to be RUNNING
// forever, and say what the run id was so somebody can look at the target tenant
// and undo it. Sound because the manifest pins replicas to 1 - with two
// containers this would close the other one's live jobs.
func (s *Service) Reconcile(ctx context.Context) error {
	jobs, err := s.Records(ctx)
	if err != nil {
		return err
	}

	for _, job := range jobs {
		if job.Status != StatusRunning && job.Status != StatusQueued {
			continue
		}

		unfinished := job.Status
		job.Status = StatusInterrupted
		job.CountsVerified = false
		job.FinishedAt = recordTime(time.Now())
		if unfinished == StatusQueued {
			job.Error = "the microservice restarted before this job started, so nothing was done"
		} else {
			job.Error = "the microservice restarted while this job was running, so what it " +
				"reached is not known: check the target tenant by run id"
		}

		if err := s.UpdateRecord(ctx, job); err != nil {
			// Reported rather than fatal: one unwritable record must not stop
			// the service from starting.
			slog.Warn("could not close an interrupted job's record", "job", job.ID, "error", err)
			continue
		}
		slog.Info("closed an interrupted job", "job", job.ID, "was", unfinished, "runId", job.RunID)
	}
	return nil
}
