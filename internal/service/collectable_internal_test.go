package service

import "testing"

// Who may collect a migration's credentials, as a rule rather than through a
// request: the three answers that are not "the caller who started it" are each
// a refusal, and the one that is easiest to get wrong is the empty record.
func TestCollectableBy(t *testing.T) {
	caller := &Caller{Tenant: "t-owner", Username: "admin"}

	cases := map[string]struct {
		job     *Job
		caller  *Caller
		allowed bool
	}{
		"the caller who started it": {
			job: &Job{RequestedBy: "t-owner/admin"}, caller: caller, allowed: true,
		},
		// Nobody can prove they are the caller a record does not name, and "the
		// record has forgotten" is not a reason to hand a fleet's passwords to
		// whoever asks next.
		"a record naming nobody": {
			job: &Job{}, caller: caller,
		},
		"somebody else, with the same role": {
			job: &Job{RequestedBy: "t-owner/colleague"}, caller: caller,
		},
		// The name is tenant-qualified, so the same username in another tenant is
		// another person. Such a caller is refused by Authorize long before this,
		// but the rule must not depend on that.
		"the same username in another tenant": {
			job: &Job{RequestedBy: "t-other/admin"}, caller: caller,
		},
		"no caller at all": {
			job: &Job{RequestedBy: "t-owner/admin"},
		},
	}

	for name, c := range cases {
		err := collectableBy(c.job, c.caller)
		if c.allowed && err != nil {
			t.Errorf("%s: refused with %v", name, err)
		}
		if !c.allowed && err == nil {
			t.Errorf("%s: allowed", name)
		}
	}
}

// The name is written down when a migration is accepted and compared against
// when its credentials are collected, which is a different request and possibly
// a different day. Two spellings of it would read as a permission problem.
func TestTheCallerNameIsSpelledOneWay(t *testing.T) {
	caller := &Caller{Tenant: "t-owner", Username: "admin"}
	if got, want := caller.Name(), "t-owner/admin"; got != want {
		t.Errorf("Name() is %q, want %q", got, want)
	}
}

// The username is taken from the platform's answer rather than from what the
// caller typed, so that a difference in capitalisation between starting a
// migration and collecting its credentials cannot lock somebody out of their own
// run.
func TestTheUsernameComesFromThePlatform(t *testing.T) {
	user := []byte(`{"userName":"Harald.Meyer","effectiveRoles":[]}`)
	if got := usernameOf(user, "harald.meyer"); got != "Harald.Meyer" {
		t.Errorf("usernameOf is %q, want the platform's own spelling", got)
	}
	// And a platform that says nothing leaves the caller as they presented
	// themselves, which is what every other message already quotes.
	if got := usernameOf([]byte(`{}`), "harald.meyer"); got != "harald.meyer" {
		t.Errorf("usernameOf is %q, want the presented name as a fallback", got)
	}
}
