package service

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/c8yclient"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/session"
)

// A remote target is a tenant this instance is not subscribed to - a different
// parent, or a different system - reached with a client certificate instead of a
// service user. The secret that reaches it (the leaf certificate and its key)
// must never touch a job record, which is a managed object readable by owner
// admins and echoed in every job response. So a remote target lives here, in
// memory, keyed by an opaque id; the migration request carries only that id,
// which is not a secret. The store dying with the container is consistent with
// how queued jobs already behave: a restart abandons them.

// remoteTarget is one established certificate connection to a tenant.
type remoteTarget struct {
	ID      string
	Host    string
	Tenant  string
	Subject string

	// material is the secret: the leaf and key this presents, and the CA the
	// operator was given to register. Never serialised.
	material certMaterial

	CreatedAt time.Time
	ExpiresAt time.Time
}

// caPEM is what the operator copies to the target to register as trusted.
func (rt *remoteTarget) caPEM() string { return rt.material.CAPEM }

// client builds a platform client that authenticates to this target with the
// certificate. Built fresh each time it is asked for - a client is cheap next to
// a migration, and there is no long-lived pool to cache.
func (rt *remoteTarget) client(insecure bool) (*tenantClient, error) {
	sess := &session.Session{
		Host:               rt.Host,
		Tenant:             rt.Tenant,
		Certificate:        rt.material.LeafPEM,
		CertificateKey:     rt.material.KeyPEM,
		InsecureSkipVerify: insecure,
	}
	client, err := c8yclient.New(sess, "")
	if err != nil {
		return nil, fmt.Errorf("could not build a client for the remote target %s: %w", rt.Host, err)
	}
	return &tenantClient{session: sess, client: client}, nil
}

// remoteTargetStore holds the established remote targets.
type remoteTargetStore struct {
	mu      sync.Mutex
	targets map[string]*remoteTarget
}

func newRemoteTargetStore() *remoteTargetStore {
	return &remoteTargetStore{targets: map[string]*remoteTarget{}}
}

// create mints a certificate for a target and remembers it.
func (s *remoteTargetStore) create(host, tenant string, now time.Time) (*remoteTarget, error) {
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	subject := "devicemigration-" + id
	material, err := generateCertMaterial(subject, now)
	if err != nil {
		return nil, err
	}

	rt := &remoteTarget{
		ID:        id,
		Host:      host,
		Tenant:    tenant,
		Subject:   subject,
		material:  material,
		CreatedAt: now,
		ExpiresAt: now.Add(certValidity),
	}

	s.mu.Lock()
	s.targets[id] = rt
	s.mu.Unlock()
	return rt, nil
}

// get returns a target by id.
func (s *remoteTargetStore) get(id string) (*remoteTarget, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rt, ok := s.targets[id]
	return rt, ok
}

// expired reports whether the certificate has run out.
//
// Until a revert could use one of these, the expiry was a note on a listing and
// nothing enforced it: an expired target would have been handed to the platform
// and refused with a 401 that says nothing about why. Now that a migration can
// be undone for as long as its certificate lives, the deadline is the thing that
// decides whether that is still possible, so it is checked where the client is
// built and said in words.
func (rt *remoteTarget) expired(now time.Time) bool {
	return !now.Before(rt.ExpiresAt)
}

// list is every remote target, newest first, for a management view. The secret
// material is not exposed by the caller that renders these.
func (s *remoteTargetStore) list() []*remoteTarget {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*remoteTarget, 0, len(s.targets))
	for _, rt := range s.targets {
		out = append(out, rt)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// remove discards a target, so its certificate can be forgotten before it
// lapses. Returns whether there was one.
func (s *remoteTargetStore) remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.targets[id]
	delete(s.targets, id)
	return ok
}

// randomID is an unguessable handle. Unguessable matters: the id is what a
// migration references, so a weak one would let a caller aim a migration at
// somebody else's established target.
func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("could not generate a remote-target id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// targetClient resolves the target a migration or preflight names, which is
// either a subscribed tenant or an established remote target. The one place that
// knows the difference, so every path that reaches a target goes through it.
func (s *Service) targetClient(remoteTargetID, tenant string) (*tenantClient, error) {
	if remoteTargetID != "" {
		rt, ok := s.remoteTargets.get(remoteTargetID)
		if !ok {
			return nil, &BadRequestError{Reason: fmt.Sprintf(
				"remote target %s is not known to this instance - it may have expired or "+
					"the service may have restarted; establish it again", remoteTargetID)}
		}
		if rt.expired(time.Now()) {
			return nil, &BadRequestError{Reason: fmt.Sprintf(
				"the certificate for remote target %s expired at %s, so this service can no "+
					"longer reach %s. Establish a new remote target to migrate again; an "+
					"existing migration can no longer be undone from here",
				remoteTargetID, recordTime(rt.ExpiresAt), rt.Tenant)}
		}
		return rt.client(s.insecureSkipVerify)
	}
	return s.tenant(tenant)
}

// --- HTTP surface ----------------------------------------------------------

// RemoteTargetRequest establishes a certificate connection to a target this
// instance is not subscribed to.
type RemoteTargetRequest struct {
	// Host is where the target lives, e.g. t12345.eu-latest.cumulocity.com. The
	// scheme is optional; https is assumed.
	Host string `json:"host"`

	// Tenant is the target tenant id. Used to reject a migration into the same
	// tenant, and shown on the job.
	Tenant string `json:"tenant"`
}

// RemoteTargetView is a remote target without its secret, for listing.
type RemoteTargetView struct {
	ID        string `json:"id"`
	Host      string `json:"host"`
	Tenant    string `json:"tenant"`
	Subject   string `json:"subject"`
	CreatedAt string `json:"createdAt"`
	ExpiresAt string `json:"expiresAt"`
}

// RemoteTargetEstablished is the answer to creating one: the view plus the CA to
// register on the target. The CA is public; the leaf and key it was signed with
// are never returned.
type RemoteTargetEstablished struct {
	RemoteTargetView
	CACertificate string `json:"caCertificate"`
	Instructions  string `json:"instructions"`
}

func (rt *remoteTarget) view() RemoteTargetView {
	return RemoteTargetView{
		ID:        rt.ID,
		Host:      rt.Host,
		Tenant:    rt.Tenant,
		Subject:   rt.Subject,
		CreatedAt: recordTime(rt.CreatedAt),
		ExpiresAt: recordTime(rt.ExpiresAt),
	}
}

func (s *Service) handleCreateRemoteTarget(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Authorize(r.Context(), r); err != nil {
		writeError(w, r, err)
		return
	}

	var request RemoteTargetRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, r, err)
		return
	}
	if request.Host == "" || request.Tenant == "" {
		writeError(w, r, &BadRequestError{Reason: "host and tenant are both required"})
		return
	}

	rt, err := s.remoteTargets.create(request.Host, request.Tenant, time.Now())
	if err != nil {
		writeError(w, r, err)
		return
	}

	writeJSON(w, r, http.StatusCreated, RemoteTargetEstablished{
		RemoteTargetView: rt.view(),
		CACertificate:    rt.caPEM(),
		Instructions: "Register this CA certificate as trusted, with " +
			"auto-registration enabled, in the target tenant (Administration → " +
			"Device management → Trusted certificates). Then preflight and migrate.",
	})
}

func (s *Service) handleListRemoteTargets(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Authorize(r.Context(), r); err != nil {
		writeError(w, r, err)
		return
	}

	views := []RemoteTargetView{}
	for _, rt := range s.remoteTargets.list() {
		views = append(views, rt.view())
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"remoteTargets": views})
}

func (s *Service) handleDeleteRemoteTarget(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Authorize(r.Context(), r); err != nil {
		writeError(w, r, err)
		return
	}

	if !s.remoteTargets.remove(r.PathValue("id")) {
		writeError(w, r, &NotFoundError{What: fmt.Sprintf("remote target %s", r.PathValue("id"))})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
