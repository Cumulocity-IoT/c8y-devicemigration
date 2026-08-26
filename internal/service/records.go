package service

import (
	"context"
	"encoding/json"
	"fmt"
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

		var body struct {
			Job *Job `json:"c8y_DeviceMigration"`
		}
		if err := json.Unmarshal(object.Bytes(), &body); err != nil || body.Job == nil {
			// A managed object of this type that this service did not write, or
			// wrote in an older shape. Skipped rather than fatal: one unreadable
			// record must not hide all the others.
			continue
		}
		body.Job.RecordID = object.ID()
		jobs = append(jobs, body.Job)
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
func (s *Service) Record(ctx context.Context, jobID string) (*Job, error) {
	jobs, err := s.Records(ctx)
	if err != nil {
		return nil, err
	}
	for _, job := range jobs {
		if job.ID == jobID {
			return job, nil
		}
	}
	return nil, &NotFoundError{What: fmt.Sprintf("job %s", jobID)}
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
