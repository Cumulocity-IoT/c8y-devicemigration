package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

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
//
// Held for a bounded time as well as for a single read. Without that, a fleet's
// passwords sit in a long-lived process for as long as the container happens to
// live, which is where a heap dump or a core file would find them - and an
// operator who has not collected within the hour has almost certainly moved on.
// It takes nothing away that was being relied on: the credentials are already
// lost whenever the container is replaced, so the only change is that the loss
// becomes predictable rather than a matter of when a deployment happens.
//
// **This design assumes a single replica**, which the manifest declares
// (`"replicas": 1`, `"scale": "NONE"`) and a test pins. With more than one, each
// container would hold its own store: whether a download is offered would depend
// on which replica answered, and a collection could be refused by one while
// another still held the file.
type credentialStore struct {
	mu    sync.Mutex
	files map[string]heldCredentials
}

// heldCredentials is one job's file and when it stops being offered.
type heldCredentials struct {
	body      []byte
	expiresAt time.Time
}

// credentialTTL is how long an uncollected file is kept.
//
// An hour, which is long enough for an operator to be called away between
// starting a migration and collecting from it, and short enough that a fleet's
// passwords are not resident for the life of a container. Expiry is the same
// answer as a restart: re-register, which rotates.
const credentialTTL = time.Hour

// credentialSweepInterval is how often expired files are dropped.
//
// Separate from the expiry itself because take and held already refuse an expired
// file the moment they are asked: this is only about not keeping the bytes. Short
// relative to the TTL, so "expired" and "gone from memory" are close together.
const credentialSweepInterval = 5 * time.Minute

func newCredentialStore() *credentialStore {
	return &credentialStore{files: map[string]heldCredentials{}}
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
	c.files[jobID] = heldCredentials{
		body:      body.Bytes(),
		expiresAt: time.Now().Add(credentialTTL),
	}
	return nil
}

// take returns the credentials for a job and forgets them.
//
// An expired file is the same answer as one that was never there, deliberately:
// the caller's way forward is identical - re-register, which rotates - and
// distinguishing them would only invite a retry that cannot succeed.
func (c *credentialStore) take(jobID string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	held, waiting := c.files[jobID]
	if !waiting {
		return nil, ErrNoCredentials
	}
	delete(c.files, jobID)
	if time.Now().After(held.expiresAt) {
		return nil, ErrNoCredentials
	}
	return held.body, nil
}

// held reports whether a job's credentials are still waiting.
//
// Read by the job record's CredentialsAvailable, so a user interface can offer a
// download only while there is something to download. Says nothing about the
// contents.
func (c *credentialStore) held(jobID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	held, waiting := c.files[jobID]
	return waiting && !time.Now().After(held.expiresAt)
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

// sweep drops every file whose time is up, and says how many.
func (c *credentialStore) sweep(now time.Time) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	dropped := 0
	for jobID, held := range c.files {
		if now.After(held.expiresAt) {
			delete(c.files, jobID)
			dropped++
		}
	}
	return dropped
}

// WatchCredentialExpiry drops uncollected credentials once their time is up.
//
// Its own loop rather than a turn of the hourly housekeeping, because the point
// of the expiry is that the bytes are not resident - an hour's TTL swept hourly
// would keep them for up to two.
func (s *Service) WatchCredentialExpiry(ctx context.Context) {
	ticker := time.NewTicker(credentialSweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if dropped := s.credentials.sweep(now); dropped > 0 {
				// Worth a line: it means somebody started a migration with
				// credentials and never collected them, so those devices are
				// registered with passwords nobody has.
				slog.InfoContext(ctx, "dropped uncollected device credentials",
					"migrations", dropped, "ttl", credentialTTL)
			}
		}
	}
}
