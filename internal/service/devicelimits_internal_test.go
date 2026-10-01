package service

import (
	"testing"
	"time"
)

// The allowance a device gets, driven by a clock rather than by sleeping: a rate
// limiter tested with real time is slow and passes for the wrong reason on a
// loaded machine.
func TestTheAllowanceRefillsOverTime(t *testing.T) {
	now := time.Now()
	limiter := newRateLimiter(10*time.Second, 5)
	limiter.now = func() time.Time { return now }

	// The burst, and then nothing.
	for i := range 5 {
		if !limiter.allow("device_A") {
			t.Fatalf("call %d of the burst was refused", i+1)
		}
	}
	if limiter.allow("device_A") {
		t.Error("a caller was allowed past its burst")
	}

	// One interval buys one call, not the whole burst back.
	now = now.Add(10 * time.Second)
	if !limiter.allow("device_A") {
		t.Error("an interval later the caller was still refused")
	}
	if limiter.allow("device_A") {
		t.Error("one interval gave back more than one call")
	}

	// And an idle caller comes back to a full allowance rather than a growing one.
	now = now.Add(time.Hour)
	for i := range 5 {
		if !limiter.allow("device_A") {
			t.Fatalf("call %d after a long wait was refused", i+1)
		}
	}
	if limiter.allow("device_A") {
		t.Error("an idle caller accumulated more than its burst")
	}
}

// One caller's behaviour must not cost another its allowance: a fleet is many
// devices asking independently.
func TestAllowancesAreSeparatePerCaller(t *testing.T) {
	now := time.Now()
	limiter := newRateLimiter(10*time.Second, 2)
	limiter.now = func() time.Time { return now }

	for limiter.allow("device_A") {
	}
	if !limiter.allow("device_B") {
		t.Error("a second caller was refused because the first had been asking")
	}
}

// The map is keyed by caller, so without eviction it grows by one entry for every
// device that ever calls - and a fleet is not a small number.
func TestIdleCallersAreForgotten(t *testing.T) {
	now := time.Now()
	limiter := newRateLimiter(10*time.Second, 5)
	limiter.now = func() time.Time { return now }

	for _, key := range []string{"device_A", "device_B", "device_C"} {
		limiter.allow(key)
	}
	if len(limiter.buckets) != 3 {
		t.Fatalf("holding %d callers, want 3", len(limiter.buckets))
	}

	// Long enough that every allowance is full again and the sweep is due.
	now = now.Add(2 * deviceAskIdle)
	limiter.allow("device_A")

	if _, held := limiter.buckets["device_B"]; held {
		t.Error("a caller that has not asked for a long time is still held")
	}
	// The one that just asked is kept, because it has spent a token.
	if _, held := limiter.buckets["device_A"]; !held {
		t.Error("the caller that just asked was forgotten")
	}
}

// Everything that puts a migration in the index is terminal, which is what makes
// the index safe to hold at all. A job that fails any of these must not be
// offered to a device.
func TestWhatIsCollectable(t *testing.T) {
	good := func() *Job {
		return &Job{
			Kind: KindMigration, Status: StatusSucceeded, RunID: "r1",
			Request: &MigrationRequest{
				SourceTenant: "t-source", TargetTenant: "t-target",
				DeviceSelfCredentials: true,
			},
		}
	}
	if !collectable(good()) {
		t.Fatal("a succeeded self-service migration is not collectable")
	}

	cases := map[string]func(*Job){
		"still running":                   func(j *Job) { j.Status = StatusRunning },
		"failed":                          func(j *Job) { j.Status = StatusFailed },
		"an undo rather than a migration": func(j *Job) { j.Kind = KindRevert },
		// The flag is what bounds this endpoint to migrations somebody opened,
		// rather than to every migration this service has ever run.
		"not opened for self-service": func(j *Job) { j.Request.DeviceSelfCredentials = false },
		// Without a marker there is no telling this run's copy from anything else
		// the target knows under the same id.
		"carrying no run marker": func(j *Job) { j.RunID = "" },
		"naming no target":       func(j *Job) { j.Request.TargetTenant = "" },
		"naming no source":       func(j *Job) { j.Request.SourceTenant = "" },
		"carrying no request":    func(j *Job) { j.Request = nil },
	}
	for name, break_ := range cases {
		job := good()
		break_(job)
		if collectable(job) {
			t.Errorf("%s: offered to a device anyway", name)
		}
	}
}
