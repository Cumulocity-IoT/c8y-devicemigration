// Package service is the Cumulocity microservice around the migration engine.
//
// The engine itself is untouched: this package builds clients from the service
// users the platform hands out to a subscribed application, and calls
// internal/migrate and internal/reverter exactly as the CLI does. What lives
// here is everything the CLI got from a person instead - credentials, the
// decision that two tenants are different, a confirmation, and a terminal to
// print progress to.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/microservices"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/model"
	"github.com/reubenmiller/go-c8y/v2/pkg/microservice"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/c8yclient"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/session"
)

// SubscriptionRefreshInterval is how often the subscribed tenants are reloaded.
// The SDK refreshes on a cache miss, so this only matters for a tenant that
// unsubscribed - and for noticing a rotated service user password before a job
// runs into a 401.
const SubscriptionRefreshInterval = time.Minute

// ProgressInterval is how often a running job's record is refreshed with its
// latest output. Often enough to follow a migration from outside the container,
// rarely enough not to spend the tenant's API budget on bookkeeping.
const ProgressInterval = 10 * time.Second

// recordTimeout bounds the write that closes a finished job's record. It runs
// with a context detached from the job's own, so it needs a limit of its own.
const recordTimeout = 30 * time.Second

// Options is what a caller can vary. Everything else the platform decides
// through the environment it injects.
type Options struct {
	// InsecureSkipVerify accepts a platform certificate that does not verify.
	// Set in Go by the tests, whose fixture serves TLS with a self-signed
	// certificate. Nothing reads it from the environment or from a file, so a
	// deployed microservice always verifies - the same arrangement as
	// session.Session.InsecureSkipVerify.
	InsecureSkipVerify bool
}

// Service holds what the whole microservice needs: the platform connection, the
// tenants it may act on, and the job queue.
type Service struct {
	ms *microservice.Microservice

	// baseURL is the platform, from C8Y_BASEURL. Every tenant shares it - which
	// tenant a request lands in is decided by the credentials, not the host.
	baseURL string

	// ownerTenant is the tenant the microservice is deployed on. It is the only
	// tenant whose users may start a job, and where job records are written.
	ownerTenant string

	insecureSkipVerify bool

	queue chan *queuedJob

	// settings is what an operator configured through tenant options, kept in
	// memory so a request never waits on a platform call to find out how it
	// should behave.
	settings settingsStore

	mu      sync.Mutex
	clients map[string]*tenantClient
	running map[string]*queuedJob
}

// tenantClient is one subscribed tenant's service user and the client built
// from it. Clients are cached because api.NewClient clones a transport, so
// building one per request would leak connection pools.
type tenantClient struct {
	session *session.Session
	client  *api.Client
}

// New builds the service and proves the bootstrap credentials by loading the
// subscriptions. It makes no other platform call, so a failure here is a
// configuration problem rather than a migration problem.
func New(ctx context.Context, opts Options) (*Service, error) {
	tenant, username, _ := microservices.GetBootstrapUserFromEnvironment()
	if tenant == "" || username == "" {
		return nil, errors.New(
			"no bootstrap credentials in the environment: C8Y_BOOTSTRAP_TENANT and " +
				"C8Y_BOOTSTRAP_USER are set by the platform, so this binary only runs " +
				"as a deployed microservice")
	}

	baseURL := microservices.GetBootstrapBaseURLFromEnvironment()
	if baseURL == "" {
		return nil, errors.New("no C8Y_BASEURL in the environment")
	}

	// NewMicroservice rather than New, because it is the constructor that lets
	// the client options through - New builds its bootstrap client from the
	// environment alone. It skips InitConfiguration, so that is called here.
	ms := microservice.NewMicroservice(baseURL, func(clientOpts *api.ClientOptions) {
		clientOpts.InsecureSkipVerify = opts.InsecureSkipVerify
	})
	ms.Config.InitConfiguration()

	if err := ms.Bootstrap(ctx); err != nil {
		return nil, fmt.Errorf("could not load the application subscriptions: %w", err)
	}

	s := &Service{
		ms:                 ms,
		baseURL:            baseURL,
		ownerTenant:        tenant,
		insecureSkipVerify: opts.InsecureSkipVerify,
		queue:              make(chan *queuedJob, QueueDepth),
		clients:            make(map[string]*tenantClient),
		running:            make(map[string]*queuedJob),
	}

	// Read once at startup so the first job already behaves as configured. A
	// failure here is worth reporting but not fatal: the defaults are the
	// documented behaviour, and refusing to start over an unreadable option
	// would take the service down for a setting nobody may have used.
	if err := s.RefreshSettings(ctx); err != nil {
		slog.Warn("could not read the settings, continuing with the defaults", "error", err)
	}

	// Job records live in the owner tenant, so without a service user there the
	// service could run but never record anything. Better to refuse at startup,
	// where the message is read, than at the first migration.
	if _, err := s.tenant(s.ownerTenant); err != nil {
		return nil, fmt.Errorf(
			"the microservice is not subscribed on its own tenant %s, and that is where "+
				"job records are written: subscribe it there and restart (%w)",
			s.ownerTenant, err)
	}

	return s, nil
}

// OwnerTenant is the tenant the microservice is deployed on.
func (s *Service) OwnerTenant() string { return s.ownerTenant }

// Tenants lists the tenants the microservice is currently subscribed to.
func (s *Service) Tenants(ctx context.Context) []string {
	users := s.serviceUsers(ctx)
	names := make([]string, 0, len(users))
	for _, user := range users {
		names = append(names, user.Tenant)
	}
	subscribedTenants.Set(float64(len(names)))
	return names
}

// serviceUsers copies the current service users out of the SDK.
//
// Through ForEachTenant rather than by reading ms.ServiceUsers, which is a
// public field guarded by the SDK's own mutex: a refresh can land while a
// request is being served, and reading the slice directly is a data race.
func (s *Service) serviceUsers(ctx context.Context) []model.ServiceUser {
	var users []model.ServiceUser
	// The callback never fails, so the only error this can return is a
	// cancelled context - in which case a partial list is what the caller
	// wanted anyway.
	_ = s.ms.ForEachTenant(ctx, func(_ context.Context, user model.ServiceUser) error {
		users = append(users, user)
		return nil
	})
	return users
}

// RefreshSubscriptions reloads the subscribed tenants and drops cached clients
// for tenants that are no longer subscribed or whose password changed.
func (s *Service) RefreshSubscriptions(ctx context.Context) error {
	if err := s.ms.RefreshServiceUsers(ctx); err != nil {
		return err
	}

	users := s.serviceUsers(ctx)
	live := make(map[string]string, len(users))
	for _, user := range users {
		live[user.Tenant] = user.Password
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for tenant, cached := range s.clients {
		if password, ok := live[tenant]; !ok || password != cached.session.Password {
			delete(s.clients, tenant)
		}
	}
	return nil
}

// WatchSubscriptions refreshes the subscriptions until the context is done. The
// SDK runs no timers of its own, so this is the caller's job.
func (s *Service) WatchSubscriptions(ctx context.Context) {
	ticker := time.NewTicker(SubscriptionRefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.RefreshSubscriptions(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("could not refresh the application subscriptions", "error", err)
			}
		}
	}
}

// UnsubscribedTenantError names a tenant this microservice cannot act on. It is
// reported rather than wrapped so the API can turn it into a 400 that says which
// tenant needs subscribing.
type UnsubscribedTenantError struct {
	Tenant string
}

func (e *UnsubscribedTenantError) Error() string {
	return fmt.Sprintf(
		"tenant %s has no service user for this microservice, so it must be subscribed to it first",
		e.Tenant)
}

// tenant returns the cached client for a subscribed tenant, building it on first
// use. The session it carries is in memory only: nothing is read from disk, and
// the password never leaves this process.
func (s *Service) tenant(name string) (*tenantClient, error) {
	if name == "" {
		return nil, errors.New("no tenant given")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if cached, ok := s.clients[name]; ok {
		return cached, nil
	}

	// GetServiceUser reloads the subscriptions once on a miss, which is what
	// makes a tenant subscribed after startup usable without a restart.
	user, ok := s.ms.GetServiceUser(name)
	if !ok || user.Tenant == "" {
		return nil, &UnsubscribedTenantError{Tenant: name}
	}

	// Deliberately built the same way the CLI builds it, so a migration run here
	// is configured identically to one run from a laptop.
	sess := &session.Session{
		Host:               s.baseURL,
		Tenant:             user.Tenant,
		Username:           user.Username,
		Password:           user.Password,
		InsecureSkipVerify: s.insecureSkipVerify,
	}
	client, err := c8yclient.New(sess, "")
	if err != nil {
		return nil, fmt.Errorf("could not build a client for tenant %s: %w", name, err)
	}
	// The one thing added to a client the engine also uses, and it only acts
	// when a request's context asks it to.
	forwardTFAToken(client)

	entry := &tenantClient{session: sess, client: client}
	s.clients[name] = entry
	return entry, nil
}

// Port is where the HTTP server listens. The platform requires 80 and injects
// SERVER_PORT; the SDK's viper configuration picks that up.
func (s *Service) Port() string {
	if port := s.ms.Config.GetString("server.port"); port != "" {
		return port
	}
	if port := os.Getenv("SERVER_PORT"); port != "" {
		return port
	}
	return "80"
}

// Microservice exposes the SDK object, for the health endpoints and the
// request-auth middleware.
func (s *Service) Microservice() *microservice.Microservice { return s.ms }

// probe proves a tenant's service user by asking the platform which tenant it
// belongs to. Same call the CLI's pre-flight makes, for the same reason: a
// credential problem should surface before a migration starts, not part way
// through one.
func probe(ctx context.Context, t *tenantClient) (string, error) {
	reported, err := c8yclient.Probe(ctx, t.client)
	if err != nil {
		return "", fmt.Errorf("the service user for tenant %s cannot reach the platform: %w",
			t.session.Tenant, err)
	}
	return reported, nil
}
