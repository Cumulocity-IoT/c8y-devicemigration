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
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/pagination"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/tenants"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/tenants/currenttenant"
	"github.com/reubenmiller/go-c8y/v2/pkg/microservice"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/c8yclient"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/session"
)

// tenantPageSize is how many tenants are read per page from tenant management.
// One page covers any realistic parent tenant; the iterator pages beyond it.
const tenantPageSize = 200

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

	// ProgressInterval overrides how often a running job's record is refreshed
	// with what the engine has printed. Zero means ProgressInterval, which is
	// what a deployment gets: nothing reads this from the environment or from a
	// file either. The tests shorten it so that a progress write is in flight
	// when a job finishes, which is the only way to prove that one cannot land
	// after the record that finishes it.
	ProgressInterval time.Duration
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

	// progressInterval is how often a running job's record is refreshed. See
	// Options.ProgressInterval.
	progressInterval time.Duration

	queue chan *queuedJob

	// stagingRoot is this process's own temporary directory. Everything a job
	// writes lives under it, which is what lets it be swept without touching
	// anything else on the machine.
	stagingRoot string

	// settings is what an operator configured through tenant options, kept in
	// memory so a request never waits on a platform call to find out how it
	// should behave.
	settings settingsStore

	mu      sync.Mutex
	clients map[string]*tenantClient
	running map[string]*queuedJob

	// remoteTargets are the certificate connections to tenants this instance is
	// not subscribed to. In memory only; see remotetarget.go.
	remoteTargets *remoteTargetStore

	// tenantNames is how each tenant has been described, once asked, and
	// managementNames is what tenant management said about all of them - with
	// managementAsked recording that it was asked, since a refusal is an answer.
	// See TenantDescriptions.
	tenantNames     map[string]TenantDescription
	managementNames map[string]TenantDescription
	managementAsked bool
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
	quietenHTTPWarning(ms.Client)

	if err := ms.Bootstrap(ctx); err != nil {
		return nil, fmt.Errorf("could not load the application subscriptions: %w", err)
	}

	progressInterval := opts.ProgressInterval
	if progressInterval <= 0 {
		progressInterval = ProgressInterval
	}

	s := &Service{
		ms:                 ms,
		baseURL:            baseURL,
		ownerTenant:        tenant,
		insecureSkipVerify: opts.InsecureSkipVerify,
		progressInterval:   progressInterval,
		queue:              make(chan *queuedJob, QueueDepth),
		clients:            make(map[string]*tenantClient),
		running:            make(map[string]*queuedJob),
		tenantNames:        make(map[string]TenantDescription),
		remoteTargets:      newRemoteTargetStore(),
	}

	if err := s.prepareStaging(); err != nil {
		return nil, err
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

// TenantDescriptions is every subscribed tenant, with what it is called.
//
// An id identifies a tenant and describes nothing, so this adds the two things a
// person recognises: the company name and the host. They come from two different
// places, because the platform keeps them in two:
//
//   - the company name is in tenant management, readable only from the owner
//     tenant and only with ROLE_TENANT_MANAGEMENT_READ. One call describes every
//     subtenant at once.
//   - the host is in each tenant's own /tenant/currentTenant, which its own
//     service user can always read. `name` there is the tenant's id, not a name,
//     which is why it is not used for one.
//
// Both are best effort and cached. A tenant nobody can describe is still listed
// with its id: the id is what the API needs, and a missing name is a cosmetic
// loss rather than a reason to hide a tenant somebody has to migrate.
func (s *Service) TenantDescriptions(ctx context.Context) []TenantDescription {
	names := s.tenantManagementNames(ctx)

	described := make([]TenantDescription, 0)
	for _, id := range s.Tenants(ctx) {
		described = append(described, s.describeTenant(ctx, id, names[id]))
	}
	return described
}

func (s *Service) describeTenant(
	ctx context.Context, id string, fromManagement TenantDescription) TenantDescription {

	s.mu.Lock()
	cached, known := s.tenantNames[id]
	s.mu.Unlock()
	if known {
		return cached
	}

	described := TenantDescription{ID: id, Name: fromManagement.Name, Domain: fromManagement.Domain}
	if described.Domain == "" {
		described.Domain = s.tenantHost(ctx, id)
	}

	// Cached either way: a tenant that could not be described once will not be
	// describable the next time either, and this is read every time somebody
	// opens the list.
	s.mu.Lock()
	s.tenantNames[id] = described
	s.mu.Unlock()
	return described
}

// tenantHost asks a tenant where it lives, with its own service user.
func (s *Service) tenantHost(ctx context.Context, id string) string {
	tenant, err := s.tenant(id)
	if err != nil {
		return ""
	}

	result := tenant.client.Tenants.Current.Get(ctx, currenttenant.GetOptions{})
	if result.Err != nil {
		slog.WarnContext(ctx, "could not read a tenant's host",
			"tenant", id, "error", result.Err)
		return ""
	}
	return result.Data.DomainName()
}

// tenantManagementNames is every subtenant's company name, in one call.
//
// Read once and remembered, including a refusal: a tenant that has not granted
// ROLE_TENANT_MANAGEMENT_READ will not grant it on the next request either, and
// asking again per refresh would be a 403 per refresh in the log.
func (s *Service) tenantManagementNames(ctx context.Context) map[string]TenantDescription {
	s.mu.Lock()
	cached, asked := s.managementNames, s.managementAsked
	s.mu.Unlock()
	if asked {
		return cached
	}

	names := map[string]TenantDescription{}
	if owner, err := s.tenant(s.ownerTenant); err == nil {
		iterator := owner.client.Tenants.ListAll(ctx, tenants.ListOptions{
			PaginationOptions: pagination.PaginationOptions{PageSize: tenantPageSize},
		})
		for tenant, err := range iterator.Items() {
			if err != nil {
				// Almost always a 403: the application may read the inventory of
				// the tenants it is subscribed to without being allowed to read
				// the list of tenants. The hosts below still work.
				slog.WarnContext(ctx, "could not read the tenant names from tenant management",
					"error", err)
				break
			}
			id := tenant.ID()
			names[id] = TenantDescription{
				ID:     id,
				Name:   tenant.Get("company").String(),
				Domain: tenant.Domain(),
			}
		}
	}

	s.mu.Lock()
	s.managementNames, s.managementAsked = names, true
	s.mu.Unlock()
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
	quietenHTTPWarning(client)

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

// ClientFor is the platform client this service uses for a tenant. Exported for
// tests that need to ask something about the client itself rather than about a
// call made with it.
func (s *Service) ClientFor(tenant string) (*api.Client, error) {
	entry, err := s.tenant(tenant)
	if err != nil {
		return nil, err
	}
	return entry.client, nil
}

// quietenHTTPWarning stops the HTTP client warning about every request it makes.
//
// The platform gives a microservice an internal address - C8Y_BASEURL inside a
// container is http - and the HTTP library warns whenever credentials travel
// over anything but HTTPS. It is right in general and noise here: the
// alternative it recommends is not available, the traffic never leaves the
// platform, and the warning arrives once per request, which buries everything
// the service actually has to say.
//
// Deliberately not a silenced logger: this is the library's own switch for
// exactly this warning, so anything else it wants to report still gets through.
func quietenHTTPWarning(client *api.Client) {
	client.HTTPClient.SetDisableWarn(true)
}

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
