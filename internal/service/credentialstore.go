package service

import (
	"bytes"
	"errors"
	"sync"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/registration"
)

// ErrNoCredentials means there are no credentials to hand over for a job.
//
// The same answer for "this job issued none" and "they have already been
// collected", and deliberately so: the second is not a fact worth confirming to
// a caller who did not get them, and telling the two apart would only invite a
// retry that cannot succeed.
var ErrNoCredentials = errors.New("no credentials are available")

// credentialStore holds the credentials a migration issued, until they are
// collected.
//
// Memory only, and this is the whole reason it exists. The credentials cannot go
// on the job record: that record is an ordinary managed object in the owner
// tenant, kept by default forever, rewritten every ten seconds while a job runs,
// echoed in every job response and forced into the publicly served OpenAPI
// document by the parity tests. Any tenant administrator can read it. So the
// passwords live here, keyed by job, and a restart loses them - which is
// consistent with what already happens to a running job, and recoverable by
// re-registering, which rotates.
//
// Handed over exactly once. A second read is Gone rather than the same body
// again: it makes "was this collected?" answerable, and it bounds how long a list
// of working device passwords sits in a long-lived process.
type credentialStore struct {
	mu    sync.Mutex
	files map[string][]byte
}

func newCredentialStore() *credentialStore {
	return &credentialStore{files: map[string][]byte{}}
}

// put renders the credentials and holds them for one collection.
//
// Rendered here rather than at collection time so the slice can be dropped
// immediately: what is retained is the bytes that will be served, and nothing
// keeps a structured copy alive alongside them.
func (c *credentialStore) put(jobID string, credentials []registration.Credential) error {
	if len(credentials) == 0 {
		return nil
	}
	var body bytes.Buffer
	if err := registration.WriteCredentialsFile(&body, credentials); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.files[jobID] = body.Bytes()
	return nil
}

// take returns the credentials for a job and forgets them.
func (c *credentialStore) take(jobID string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	body, held := c.files[jobID]
	if !held {
		return nil, ErrNoCredentials
	}
	delete(c.files, jobID)
	return body, nil
}

// held reports whether a job's credentials are still waiting.
//
// Read by the job record's CredentialsAvailable, so a user interface can offer a
// download only while there is something to download. Says nothing about the
// contents.
func (c *credentialStore) held(jobID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, waiting := c.files[jobID]
	return waiting
}

// forget drops a job's credentials without serving them.
//
// Used when a job's record is being removed, so retention cannot leave
// collectable passwords for a migration nobody can look up any more.
func (c *credentialStore) forget(jobID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.files, jobID)
}
