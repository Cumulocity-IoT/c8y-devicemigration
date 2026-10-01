package service

import (
	"context"
	"sync"
	"time"
)

// What the one endpoint outside the owner tenant costs, and how that is bounded.
//
// A device asks on a timer - a thin-edge device every few minutes - and every
// ask used to list the owner tenant's whole job record collection and then, per
// candidate migration, resolve an external identity and read a managed object in
// the target tenant. All of it with this service's own service users, so the
// work is charged to the platform rather than to the caller, and a device with
// nothing to do paid the full search before being told so.
//
// Three things bound it, and none of them changes what the endpoint answers:
//
//   - the collectable migrations are derived once and held briefly, rather than
//     re-read per device;
//   - a caller whose tenant has nothing to collect is answered from that index
//     alone, touching no other tenant;
//   - and a caller that asks far more often than a device would is refused.

// collectableTTL is how long the index of collectable migrations is held.
//
// Short, because a device that polls while a migration finishes should not wait
// noticeably longer than it already does - and the index is also dropped the
// moment a job finishes or a record is deleted, so this is the bound on
// staleness from anything *else* changing, not the ordinary path.
const collectableTTL = 10 * time.Second

// collectableIndex is the migrations a device may collect a credential from,
// grouped by the tenant whose devices may collect them.
//
// Safe to hold at all because every property that puts a migration in here is
// terminal: it succeeded, it asked for device self-service, and it has a run
// marker and a target. None of that changes again. The operator-facing listings
// deliberately do not read this - a person watching a running job needs the
// record itself, not a derived view of the finished ones.
type collectableIndex struct {
	mu       sync.Mutex
	builtAt  time.Time
	byTenant map[string][]*Job
}

func newCollectableIndex() *collectableIndex {
	return &collectableIndex{}
}

// invalidate drops the index, so the next caller rebuilds it.
//
// Called where a job reaches a terminal state and where a record is removed. The
// TTL alone would do, eventually; this is what makes "the migration finished, so
// its devices can collect" immediate rather than eventually.
func (c *collectableIndex) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byTenant = nil
}

// collectableFor is the migrations a device of this tenant may collect from,
// newest first.
//
// The read of the job records happens under the lock on purpose. A fleet
// reconnecting at once is exactly when this is called by many callers at the
// same moment, and the useful behaviour then is that one of them rebuilds the
// index and the rest wait for it - not that each starts its own listing.
func (s *Service) collectableFor(ctx context.Context, tenant string) ([]*Job, error) {
	index := s.collectable
	index.mu.Lock()
	defer index.mu.Unlock()

	if index.byTenant != nil && time.Since(index.builtAt) < collectableTTL {
		return index.byTenant[tenant], nil
	}

	jobs, err := s.Records(ctx)
	if err != nil {
		return nil, err
	}

	// Records answers newest first, and appending in that order keeps it - so a
	// device migrated more than once is told about the most recent move.
	byTenant := map[string][]*Job{}
	for _, job := range jobs {
		if !collectable(job) {
			continue
		}
		source := job.Request.SourceTenant
		byTenant[source] = append(byTenant[source], job)
	}

	index.byTenant, index.builtAt = byTenant, time.Now()
	return byTenant[tenant], nil
}

// collectable reports whether a device could collect a credential from this job.
//
// Every condition here is about the job alone, which is what lets the answer be
// held: the caller's own identity is checked afterwards, against the target
// tenant, and is not part of this.
func collectable(job *Job) bool {
	switch {
	case job.Kind != KindMigration || job.Status != StatusSucceeded:
		return false
	case job.Request == nil || job.Request.SourceTenant == "":
		return false
	case !job.Request.DeviceSelfCredentials:
		// A migration whose operator did not ask for device self-service is not
		// collectable. This is what bounds the one endpoint outside the owner
		// tenant to migrations somebody deliberately opened, rather than to every
		// migration this service has ever run.
		return false
	case job.RunID == "" || job.Request.TargetTenant == "":
		// Without a run marker there is no way to tell this run's copy from
		// anything else the target knows, so such a run cannot answer for a
		// device.
		return false
	}
	return true
}

// How often one caller may ask.
//
// A device asks on a timer measured in minutes, so one call every few seconds is
// already far more than any of them needs, and a short burst covers a device
// retrying a move it failed. What this refuses is a caller asking in a loop -
// which costs this service a platform call per ask, under its own service users,
// for an answer that cannot have changed.
const (
	deviceAskInterval = 10 * time.Second
	deviceAskBurst    = 5

	// deviceAskIdle is how long a caller's allowance is remembered once it is
	// back to full. Without this the map grows by one entry per device that ever
	// calls, and a fleet is not a small number.
	deviceAskIdle = 10 * time.Minute
)

// rateLimiter is a token bucket per key.
type rateLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	burst    float64
	buckets  map[string]*bucket
	sweptAt  time.Time

	// now is time.Now, replaced in tests. A rate limiter tested by sleeping is a
	// slow test that passes for the wrong reason on a loaded machine.
	now func() time.Time
}

type bucket struct {
	tokens float64
	seenAt time.Time
}

func newRateLimiter(interval time.Duration, burst int) *rateLimiter {
	return &rateLimiter{
		interval: interval,
		burst:    float64(burst),
		buckets:  map[string]*bucket{},
		now:      time.Now,
	}
}

// allow takes one token for a key, and reports whether there was one.
//
// Keyed by the *verified* caller rather than by what the request presented, so
// one caller cannot spend another's allowance by claiming their name.
func (r *rateLimiter) allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	r.sweep(now)

	held, known := r.buckets[key]
	if !known {
		// A caller's first ask is always allowed, and starts them one token down
		// from full rather than at full: a bucket created full would make the
		// first burst free for every name somebody cares to use.
		r.buckets[key] = &bucket{tokens: r.burst - 1, seenAt: now}
		return true
	}

	held.tokens += now.Sub(held.seenAt).Seconds() / r.interval.Seconds()
	if held.tokens > r.burst {
		held.tokens = r.burst
	}
	held.seenAt = now

	if held.tokens < 1 {
		return false
	}
	held.tokens--
	return true
}

// sweep forgets callers whose allowance is back to full and who have not asked
// for a while. Called under the lock, from allow.
func (r *rateLimiter) sweep(now time.Time) {
	if now.Sub(r.sweptAt) < deviceAskIdle {
		return
	}
	r.sweptAt = now

	for key, held := range r.buckets {
		idle := now.Sub(held.seenAt)
		if idle >= deviceAskIdle && held.tokens+idle.Seconds()/r.interval.Seconds() >= r.burst {
			delete(r.buckets, key)
		}
	}
}
