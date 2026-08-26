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

> **Status: increment 3.** Bootstrap, subscriptions, authorization, `/health`,
> `GET /tenants`, `POST /preflight`, migrations and reverts as asynchronous jobs,
> and closing the records of jobs whose container stopped existing. What remains
> is operational polish: metrics, log levels and settings from tenant options.

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

The version in the manifest comes from the newest `v<major>.<minor>.<patch>` tag,
or `0.1.0-SNAPSHOT` when there is none. Override it with
`make service-package SERVICE_VERSION=1.2.0`. The platform rejects anything that
is not SemVer, so `service-package` checks that before building the zip rather
than letting the upload fail.

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

`countsVerified` is false for a cancelled job, and that is not a formality. The
engine has no cancellation checks anywhere: a cancelled import counts the records
it never sent as *failures*, and a cancelled revert reports objects as deleted
that are still there. So a job that did not finish reports its run id and says
its numbers cannot be trusted, rather than presenting them as fact. Check the
target tenant by run id before retrying.

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

- **Both tenants must be on the same platform and subscribed to this
  application.** Credentials come from the subscription mechanism, so migrating
  between two platforms stays a job for [the CLI](cli.md).
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
