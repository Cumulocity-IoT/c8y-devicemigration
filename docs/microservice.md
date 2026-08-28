# The microservice

The same migration engine, running inside Cumulocity instead of on a laptop.

It is deployed on a **parent tenant** and subscribed to the tenants it migrates
between. The platform then hands it a service user for each subscribed tenant,
so nothing here needs a session file, a passphrase, or anybody's personal
credentials. An operator posts a migration and the work happens in the platform.

This is the same code as [the standalone binary](cli.md) — the migration itself
is `internal/migrate`, linked in, not a copy and not a subprocess. What the
microservice adds is everything the CLI got from a person: credentials, the
decision that two tenants really are two tenants, and somewhere to send progress.

> **Status: complete.** Bootstrap, subscriptions, authorization, `/health`,
> `GET /tenants`, `POST /preflight`, migrations and reverts as asynchronous jobs,
> closing the records of jobs whose container stopped existing, metrics, runtime
> log levels, and settings from tenant options. It has not yet run against a live
> tenant.

## Deploying it

```sh
make service-package       # -> build/devicemigration-<version>.zip
```

A release publishes the same zip on the
[releases page](https://github.com/Cumulocity-IoT/c8y-devicemigration/releases),
and the same image in the registry:

```sh
docker pull ghcr.io/cumulocity-iot/c8y-devicemigration/microservice:<version>
```

That zip is exactly what the platform wants: `cumulocity.json` and the image as
a tarball. Upload it under **Administration → Ecosystem → Microservices → Add
microservice**, or let go-c8y-cli do it:

```sh
eval "$(c8y sessions set --sessionFilter parent-tenant)"
make service-deploy
```

The version in the manifest is `0.2.0-SNAPSHOT` — a snapshot of the version being
developed — unless the commit is exactly a release tag, in which case it is that
release. `make version` prints it; `make service-package SERVICE_VERSION=1.2.0`
overrides it. Every snapshot upload replaces the same version, which is what you
want while iterating. The platform rejects anything that is not SemVer, so
`service-package` checks that before building the zip rather than letting the
upload fail. See [internals](internals.md#versions) for how it is derived.

That matters more than it sounds: the version in **Administration → Ecosystem →
Microservices** and in the service's own startup log is the only thing that
answers "is the code I just wrote actually running there".

CI builds the same zip on every push and attaches it to the run as an artifact,
so a deployable package exists for any commit without building locally.

### Then, in this order

1. **Subscribe the parent tenant to the application.** This is not optional: the
   parent tenant is where job records are written, and the service refuses to
   start without a service user there, saying so in its log.
2. **Subscribe the source and target tenants.**
3. **Grant `ROLE_DEVICE_MIGRATION_ADMIN`** to whoever should be allowed to
   migrate. The manifest declares the permission; the platform does not enforce
   it, so the service checks it on every call.

## Endpoints

All paths are relative to `/service/devicemigration/`.

The full contract is an OpenAPI 3.1 document,
[internal/service/openapi.yaml](../internal/service/openapi.yaml), which is
compiled into the binary and **served by the deployment itself** in two forms:

```sh
c8y api GET /service/devicemigration/openapi.yaml   # as written, for reading
c8y api GET /service/devicemigration/api-json       # as JSON, for tooling
```

so a running instance can be asked what it accepts rather than the answer living
only in a repository.

### In the API documentation application

The manifest sets `"openApiSpec": "api-json"`, which is how a microservice opts
in to the platform's **API documentation** application. Install that application
on the parent tenant and this service appears alongside the core APIs, with
try-it-out working against the live tenant — the document's `servers` entry is
`/service/devicemigration`, which is the path the platform exposes.

Two consequences worth knowing. The application fetches the spec with the
browser's own session, so `api-json` answers without this service's
owner-tenant check — the document is not secret, and a spec behind that check
would fail to load for everyone else. And try-it-out really does call the API:
`POST /migrations` from that page starts a migration.

Those calls arrive as a browser session, which is a credential form of its own —
see [the three ways credentials arrive](#the-three-ways-credentials-arrive). A
write from that page needs the `X-XSRF-TOKEN` header; if it comes back 401 naming
that header, the application is not sending it.

The path is only useful if it agrees with what is served, and nothing at
deployment time checks that, so a test compares the manifest's `openApiSpec`
against the routes and against whether that route needs credentials. It is kept true by tests rather than by discipline: every
route is compared against the document in both directions, every request and
response field is compared against the Go struct's json tags, and each endpoint's
documented `security` is checked against whether it really answers without
credentials. Adding an endpoint or a field without documenting it fails
`make check`.

| | |
| --- | --- |
| `GET /health` | `{"status":"UP"}`, for the platform's probes. No credentials. |
| `GET /prometheus` | metrics. No credentials. |
| `GET /openapi.yaml` | this service's own description. No credentials. |
| `GET /api-json` | the same document as JSON, for the API documentation app. No credentials. |
| `GET /tenants` | the owner tenant and every tenant this service can act on |
| `POST /preflight` | resolves both tenants, refuses a migration into the tenant it came from, and proves both service users against the platform. Takes no `rootId`: it checks the deployment, not a hierarchy |
| `POST /migrations` | starts a migration; answers **202** with a job id |
| `GET /migrations` | every job this service has run, newest first |
| `GET /migrations/{id}` | one job, including the tail of its output |
| `POST /migrations/{id}/cancel` | asks a running job to stop |
| `POST /reverts` | undoes what a migration created; answers **202** with a job id |
| `GET /reverts` | every revert, newest first |
| `GET /reverts/{id}` | one revert |
| `POST /reverts/{id}/cancel` | asks a running revert to stop |
| `GET /loggers`, `GET`/`POST /loggers/{name}` | the log level, changeable while the service runs |
| `GET /tenants/{tenant}/inventory` | find something to migrate, by name or only roots |
| `GET /tenants/{tenant}/inventory/{id}` | one object and its child counts |
| `GET /tenants/{tenant}/inventory/{id}/children` | what is directly under it |
| `GET /tenants/{tenant}/platform/inventory/managedObjects` | the platform's own listing, in that tenant |
| `GET /tenants/{tenant}/platform/inventory/managedObjects/{id}` | the platform's own read, in that tenant |
| `GET /tenants/{tenant}/platform/inventory/managedObjects/{id}/{relationship}` | one of its reference collections |
| `POST /estimates` | how much a migration would move, and whether the limits allow it |

```sh
c8y api GET /service/devicemigration/tenants

c8y api POST /service/devicemigration/preflight --data '{
    "sourceTenant": "t11111",
    "targetTenant": "t22222"
}'
```

Starting a migration:

```sh
c8y api POST /service/devicemigration/migrations --data '{
    "sourceTenant": "t11111",
    "targetTenant": "t22222",
    "rootId": "1399229",
    "dataTypes": "all",
    "dateFrom": "-30d"
}'

# then follow it
c8y api GET /service/devicemigration/migrations/20260825T162735Z_c8e249
```

The fields are the CLI's flags — `childTypes`, `noChildren`, `dataTypes`,
`dateFrom`, `dateTo`, `noFlag` — parsed by the same code, so `"all"` and
`"asset,device"` mean exactly what they mean there. An unknown field is refused
rather than ignored, so a typo is a 400 and not a surprise.

### Undoing a migration

```sh
# by the job that created it, which is where the run id comes from
c8y api POST /service/devicemigration/reverts --data '{
    "jobId": "20260825T162735Z_c8e249"
}'

# or by run id, if the job record is long gone
c8y api POST /service/devicemigration/reverts --data '{
    "targetTenant": "t22222",
    "runId": "20260825T162735Z_b5c5a2"
}'
```

A run id is all this needs, and that is the whole reason it works from a
container: every object a migration creates carries a marker fragment naming the
run, so the engine finds them by asking the target tenant rather than by reading
the mapping file — which lived in a job directory that was removed when the job
ended.

The consequence is that **a migration run with `noFlag` cannot be undone here**.
Nothing it created carries a marker, so only its mapping file could find it, and
that is gone. Asking is refused with that explanation rather than accepted into a
revert that would delete nothing and report success. Undo such a run with
[the command line tool](cli.md) and the mapping.

Reverts share the migrations' queue, so one never runs while a migration is part
way through creating what it would be deleting. A revert of a revert is refused:
a revert creates nothing.

### Why a migration is a job and not a request

The platform caps a request at fifteen minutes and a hierarchy with data
regularly takes longer, so `POST /migrations` returns as soon as the work is
accepted. One migration runs at a time: the engine keeps its state in files and
its exporters are single-use, and a container's disk is sized for one migration's
staging area. Ten more may wait; the eleventh gets a **429** rather than a
promise nobody will keep.

A job ends in one of:

| | |
| --- | --- |
| `SUCCEEDED` | the engine finished; its counts are reported as verified |
| `FAILED` | the engine stopped and said why; `runId` is still recorded, because objects may already exist in the target |
| `CANCELLED` | asked to stop. **Its counts are not verified** — see below |
| `INTERRUPTED` | the container stopped existing while the job was running or queued. **Counts are not verified** |

**A record cannot be left saying `RUNNING` for good.** A container that is
replaced mid-job takes the job with it — there is no persistent storage and no way
to resume — so the record would otherwise claim to be running for ever, with a
Cancel that answers *"running job … does not exist"*. Two things close it: startup
reconciles every record that says `RUNNING` or `QUEUED`, and the listings settle
any record that says `RUNNING` while this container knows it is not the job it is
running. Both mark it `INTERRUPTED`, with `countsVerified: false` and the run id,
which is what an undo needs.

`QUEUED` records are left alone at runtime on purpose: a job queued *here* has one
too, and from inside a request the two cannot be told apart. Startup can tell,
because nothing is queued then.

Cancelling a job that is not running is therefore not an error either. If its
record still says `RUNNING`, cancelling settles it; if the job is genuinely over,
the answer is **409** and says so.

**A job reports itself finished only once its record says so.** While a job is
running, `GET /migrations/{id}` answers from the running job rather than from the
record, because the record is only as fresh as the last progress write. The
outcome is the other way round: the record is written first, and only then does
the running job start reporting it. The other order was a real defect — a caller
that polled until a migration was `SUCCEEDED` and then asked to undo it could be
refused with *"has no run id"*, because the record it was refused from still said
`RUNNING`. Between the two writes a caller sees `RUNNING` for a few milliseconds
longer, which is merely out of date rather than wrong.

`countsVerified` is false for a cancelled job, and that is not a formality. The
engine has no cancellation checks anywhere: a cancelled import counts the records
it never sent as *failures*, and a cancelled revert reports objects as deleted
that are still there. So a job that did not finish reports its run id and says
its numbers cannot be trusted, rather than presenting them as fact. Check the
target tenant by run id before retrying.

### Remote targets

A target the instance is not subscribed to — a different parent, or a different
platform — is reached with a client certificate instead of a service user, which
is what lets a device authenticate to a tenant nobody handed it a password for.
The verified basis for this is in
[docs/concepts/remote-targets.md](concepts/remote-targets.md); the flow the
service exposes:

```sh
# 1. establish a connection to the unsubscribed target; get the CA to register
c8y api POST /service/devicemigration/remote-targets \
  --data '{"host":"t22222.eu-latest.cumulocity.com","tenant":"t22222"}'
# -> { id, caCertificate, ... }

# 2. register that caCertificate as trusted, with auto-registration enabled, in
#    the target tenant (Administration -> Device management -> Trusted certificates)

# 3. migrate, referencing the connection by id rather than a subscribed tenant
c8y api POST /service/devicemigration/migrations \
  --data '{"sourceTenant":"t11111","remoteTargetId":"<id>","rootId":"1399229"}'
```

`GET /remote-targets` lists the established connections (without their
certificates), and `DELETE /remote-targets/{id}` discards one. The rules that
make it safe:

- **The secret never rests.** The service mints a throwaway CA and a client leaf,
  hands out only the public CA, and keeps the leaf and its key **in memory keyed
  by the connection id**. The migration request carries only that id, so nothing
  secret reaches the job record, the job responses, or the OpenAPI document. A
  container restart forgets the connections, exactly as it abandons running jobs.
- **The target authenticates the certificate.** The platform issues a
  device access token to whoever presents, over mTLS, a leaf whose issuer it
  trusts — which is why the operator registers the CA. The token is the
  platform's own short-lived one, renewed by the client.
- **Reverting a remote-target migration is refused here** with a message saying
  why: undoing it needs the certificate that created the objects, which the
  service no longer holds, so it points at the command line or the target admin.

One thing this has **not** had is an end-to-end run against a live remote tenant;
the certificate, token and import primitives it rests on were each verified live
(see the concept), but the assembled service flow has only been exercised against
the test fixture.

### Naming things, not just identifying them

`GET /tenants` answers with each subscribed tenant's id, its company name and its
host, because `t11106993` identifies a tenant and describes nothing. The two names
come from two places, since the platform keeps them in two:

| | |
| --- | --- |
| the company name | tenant management, read once from the owner tenant. Needs `ROLE_TENANT_MANAGEMENT_READ`; without it there are no names |
| the host | each tenant's own `currentTenant`, which its service user can always read. Note that `name` there is the tenant's *id*, which is why it is not used as one |

Both are cached and both are best effort: a tenant nobody can describe is still
listed with its id, because the id is what a migration needs.

A job record carries `rootName` for the same reason — what the migrated object was
called when the migration was accepted. It is read once, at that moment, and
recorded: the object may not be there to ask later, and a list of ids describes
nothing either.

### Choosing what to migrate, and knowing how big it is

A caller has no session in the tenant being migrated — a session belongs to one
tenant, and the hierarchy is in another. So the service reads that tenant's
inventory on the caller's behalf:

```sh
c8y api GET '/service/devicemigration/tenants/t11111/inventory?text=berlin'
c8y api GET  /service/devicemigration/tenants/t11111/inventory/1399229
c8y api GET '/service/devicemigration/tenants/t11111/inventory/1399229/children?childTypes=asset,device'
```

Two things about the listing are worth knowing, because both were wrong once.

**Only devices, groups and assets come back.** The condition is part of the query
the platform runs — `has(c8y_IsDevice) or has(c8y_IsDeviceGroup) or
has(c8y_IsAsset)`, the same three the platform's own inventory search looks for —
so dashboards, smart rules, retention rules and this service's job records are
never offered as something to migrate. It has to be inside `query`: the platform
ignores `type` and `fragmentType` whenever a query is present.

**`onlyRoots=true` is resolved from the objects' parents.** There is no fragment
marking an object as somebody's child, so the service asks for the parents and
drops anything that has one — which is how the platform's own asset picker
decides it too. The consequence is that a page of roots is a page of the
inventory with the children taken out, and can hold fewer objects than
`pageSize`; `more` comes from the platform's page count, so it stays right when
that happens.

### The inventory proxy

The picker in [the UI](ui.md) is the platform's own asset selector, and that
component reads through Cumulocity's own client. It cannot be handed data. So
three more routes exist, which are the platform's inventory reads performed in a
named tenant and answered with exactly what the platform said:

```sh
c8y api GET '/service/devicemigration/tenants/t11111/platform/inventory/managedObjects?pageSize=10'
c8y api GET  /service/devicemigration/tenants/t11111/platform/inventory/managedObjects/1399229
c8y api GET  /service/devicemigration/tenants/t11111/platform/inventory/managedObjects/1399229/childAssets
```

A Cumulocity client given `/service/devicemigration/tenants/t11111/platform` as
its base URL behaves as though it were signed in to `t11111`. That is what makes
the asset selector work over a tenant the browser has no session in, without any
of its searching, paging or drill-down being reimplemented.

Four things about it are deliberate:

- **The whole query string is forwarded.** Every filter, page and ordering the
  platform understands works, because deciding here which ones are allowed would
  be deciding which of the platform's own components are allowed to work.
- **The answer is verbatim.** The envelope, the `statistics` and the ordering are
  what make the platform's paging and search work; reshaping any of it would
  break the component this exists for.
- **The links are rewritten.** `self`, `next` and `prev` come back from the
  platform naming the platform's *internal* address — a host no browser can
  reach and none should be told about. Each becomes the proxy path that produced
  it, so a client that follows a link stays in the tenant it was reading.
- **There is no wildcard.** Three routes exist and the router is the allowlist:
  the inventory listing, one managed object, and one of its reference
  collections. Anything else — `user/currentUser`, a measurement, a second path
  segment — is a 404, and an unknown collection name is a 400.

These are reads and nothing else. **They do grant something new**, though: an
owner-tenant administrator holding `ROLE_DEVICE_MIGRATION_ADMIN` can now read
any subscribed tenant's inventory through its service user. That is strictly less
than what the role already permits — migrating that tenant's hierarchy wholesale
— but it is a capability, and anyone deciding who gets the role should know it
comes with this.

An id that does not exist is a 404 here, which is the point: until these existed,
a mistyped root was discovered when the migration ran, minutes later.

`POST /estimates` answers how big a migration would be before anybody commits to
it:

```sh
c8y api POST /service/devicemigration/estimates --data '{
    "sourceTenant": "t11111",
    "rootId": "1399229",
    "dataTypes": "measurements"
}'
```

It reports the object and identity counts, the record totals per kind, the
**busiest single device** per kind — the limits are per device, so a hierarchy
well under the total can still be refused — the window it counted over, any
limits that would be breached, and the limits themselves so nothing has to
hardcode them. It creates nothing and takes seconds; the same request fields as a
migration, resolved the same way including the configured default window, so an
estimate describes the run that follows it rather than a similar one.

### What preflight is for

It answers whether a migration between two tenants is possible at all, without
reading or writing a single device — which is why it takes no `rootId`. Three
things are established: both tenants are subscribed to this application, they are
not the same tenant, and each side's service user actually works. Run it after
subscribing a tenant, and before a migration whose credentials have never been
used. It answers with what each side resolved to:

```json
{
  "host": "https://example.cumulocity.com",
  "source": {"tenant": "t11111", "reportedTenant": "t11111", "serviceUser": "service_devicemigration"},
  "target": {"tenant": "t22222", "reportedTenant": "t22222", "serviceUser": "service_devicemigration"}
}
```

The SDK also offers `/env` and `/logfile`. They are deliberately **not**
registered: they print the container's environment and log to any caller the
gateway authenticated, from any subscribed tenant, which is a wider audience than
everything else here. The container log is available through the platform's own
microservice log view.

## Watching it run

`GET /prometheus` exposes, besides the Go runtime's own metrics:

| | |
| --- | --- |
| `devicemigration_jobs_total{kind,status}` | jobs that finished, by kind and outcome |
| `devicemigration_job_duration_seconds{kind}` | how long they took. The buckets reach six hours, because a hierarchy with a month of measurements does |
| `devicemigration_queue_depth` | accepted and waiting |
| `devicemigration_job_running` | 1 while one runs, and one runs at a time |
| `devicemigration_subscribed_tenants` | how many tenants this service can act on |
| `devicemigration_records_pruned_total` | job records deleted for being older than the retention period |

The log level can be changed without redeploying, which otherwise means losing
whatever was running:

```sh
c8y microservices loggers set --name devicemigration --logger ROOT --level DEBUG
c8y microservices loggers delete --name devicemigration --logger ROOT   # back to INFO
```

These are the Spring Boot actuator endpoints the platform proxies, so its own
tooling works against this service. Go's structured logging has no logger
hierarchy to expose, so there is exactly one logger, named `ROOT` as the actuator
names its own; any other name is a 404 rather than a pretence that it was
configured.

## Settings

Declared in the manifest and stored as tenant options of the tenant the service
is deployed on, so they can be changed in **Administration → Settings** without
redeploying. They are read at startup and re-read hourly.

| Setting | Default | |
| --- | --- | --- |
| `records.retention.days` | `0` | How long a finished job's record is kept. **Zero keeps them forever**, which is the safe default: the record is the only account of what was done to a customer's tenant |
| `data.dateFrom` | `-14d` | The data window a migration gets when it does not name one |

```sh
c8y tenants options update --category devicemigration \
    --key records.retention.days --value 90
```

Pruning only ever removes records of jobs that **finished**, and only when a
retention period is configured. A value that cannot be read as a number is
ignored with a warning rather than treated as zero-something.

### The default window differs from the command line, deliberately

The engine's own defaults are asymmetric: without a `--dateFrom` it takes the
last fourteen days of measurements and **all** events and alarms. A single
`dateFrom` applies to all three kinds, so there is no value that reproduces that
asymmetry — and a setting has to declare a real default, or an operator finds it
blank in Administration with no idea what it does.

So the service seeds `-14d`, and a migration that does not name a window carries
**fourteen days of measurements, events and alarms alike**. That is narrower than
[the command line](cli.md) would take, never wider, and the estimate
[the UI](ui.md) shows names the window it counted over beside the counts. Set the option to something
longer if a migration needs more, or name `dateFrom` on the request.

Clearing the option entirely returns to the engine's own asymmetric defaults —
that is what an empty value means to the code, and it stays supported for anyone
who wants it.

## Who may call it

Only users of the tenant the microservice is deployed on, and only those holding
`ROLE_DEVICE_MIGRATION_ADMIN`.

The API gateway authenticates every request before it arrives, but it forwards
the credentials rather than a verdict and says nothing about roles — so the
service proves the credentials against `/user/currentUser` itself and then
applies both rules. A subtenant admin is deliberately refused even with the role:
migrating writes into a second tenant, and holding rights in the source says
nothing about the target.

### The three ways credentials arrive

| | |
| --- | --- |
| `Authorization: Basic` | `tenant/username`. Without the tenant prefix it is ambiguous, and is refused rather than assumed to mean the owner tenant |
| `Authorization: Bearer` | a token; the tenant comes from its `ten` claim |
| the `authorization` cookie | a **browser session**, which sends no Authorization header at all. Holds either a token or `Basic <base64>`, depending on how the tenant's UI authenticates |

The cookie form is what the API documentation application's try-it-out uses, and
the SDK's own `AuthFromRequest` does not read it — it looks at the Authorization
header and nothing else. Reading it is therefore this service's job, which is why
[internal/service/credentials.go](../internal/service/credentials.go) exists.

**A browser session must send `X-XSRF-TOKEN` for anything that is not a read**,
and it must match the session. A cookie travels with any request a browser can be
talked into making, so without that header a page visited while logged in could
start a migration — the platform's own UI sends it on every write for the same
reason. Reads need no such proof, so browsing the API stays possible either way.
If a write is refused, the message names the missing header.

A session using two-factor authentication also sends a `tfatoken` header, which
the platform requires on every call made with it — including the one that
verifies the caller — so it is forwarded.

One deliberate deviation: the session token's claims are decoded locally rather
than with the library's `authentication.ParseToken`, because that decodes with the
standard base64 alphabet while JWT mandates the URL-safe one. The two differ as
soon as a payload needs `-` or `_`, which a claim containing a character as
ordinary as `?` produces. A test mints exactly such a token, and it fails if the
library is ever fixed — at which point the local decoder can go.

## Limitations

- **The source tenant must be subscribed to this application.** Its credentials
  come from the subscription mechanism. The **target** may instead be a tenant
  this instance is not subscribed to — a different parent, or a different
  platform — reached with a certificate; see [Remote targets](#remote-targets).
- **One replica, one migration at a time.** The manifest pins `replicas: 1`. The
  engine keeps its state in files whose paths come from the caller, so two
  containers sharing a mapping file would corrupt it.
- **A container restart ends a migration.** Microservices have no persistent
  volumes, so the staging directory and the resume journal are ephemeral. The
  job record keeps the run id, which is enough to revert or to resume from the
  CLI, but the service does not resume by itself.

  On startup, every record still saying `RUNNING` or `QUEUED` is closed as
  `INTERRUPTED`, keeping its run id so the target tenant can be inspected and the
  run undone. That is only sound because the manifest pins `replicas: 1`: with
  two containers, each would be closing the other's live jobs.
- **Job records accumulate until a retention period is set.** The default keeps
  everything, which is right for an audit trail and wrong for a tenant that
  migrates daily. See [Settings](#settings).
- **Progress is kept, not streamed.** The last 200 lines of a job's output are on
  its record and in the status response; everything goes to the container log as
  well. There is no streaming endpoint.
- **`ignoreLimits` is not exposed.** The export limits are the only thing keeping
  a container's ephemeral disk finite, so migrations always run incrementally —
  one device's data staged at a time — and always within the limits.

## Development

```sh
go test ./internal/service/     # httptest platform, no Docker needed
make service-package            # needs a Docker daemon
```

The tests run against a fake platform in
[internal/service/platform_test.go](../internal/service/platform_test.go). It is
worth understanding before adding to them: a real Cumulocity is *one host serving
many tenants*, where the tenant is decided by the credentials rather than by the
URL. So the fixture is a single TLS server that reads the tenant out of the Basic
auth and reverse-proxies to that tenant's `testfixture.Server`, and answers the
two endpoints the fixtures do not model — the application's subscriptions and the
current user. Without that shape there would be no way to test a two-tenant
migration at all, because the service only ever knows one base URL.

`service.Options{InsecureSkipVerify: true}` exists for those tests, whose fixture
serves a self-signed certificate. Nothing reads it from the environment or from a
file, so a deployed microservice always verifies the platform's certificate — the
same arrangement as `session.Session.InsecureSkipVerify`.
