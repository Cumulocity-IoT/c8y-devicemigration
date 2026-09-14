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

### Device credentials

`POST /migrations` accepts `provisionCredentials: true`, which gives every
migrated device a device user of its own so the fleet can reconnect to the
target. The devices are registered **before** the hierarchy is imported, because
bulk registration creates each managed object owned by the device user it creates
alongside it and the import then fills that object in; registering afterwards
only rotates a password and leaves the object alone.

The passwords are **not on the job record**. That record is an ordinary managed
object in the owner tenant, kept by default forever, rewritten every ten seconds
while a job runs, echoed in every job response and forced into the publicly
served OpenAPI document by the parity tests — any administrator of the owner
tenant can read it. So the record carries a count, `credentialsIssued`, and
nothing else.

The file itself is held in this container's memory and collected **once**:

```sh
c8y api GET /service/devicemigration/migrations/<id>/credentials > credentials.csv
```

It arrives as `text/csv` with `EXTERNAL_ID`, `TENANT`, `USERNAME`, `PASSWORD`,
and is dropped as it is served. A second call is **410 Gone**, which is
deliberate: it makes "were these collected?" an answerable question, and it
bounds how long a list of working device passwords sits in a long-lived process.
`credentialsAvailable` on the job says whether there is still something to
collect, which is what the UI's download action watches.

A 410 means one of three things and cannot tell them apart — already collected,
the container was replaced, or none were issued. The way back in every case is to
register the devices again, which **rotates** their passwords and invalidates any
file handed out earlier.

Two refusals worth knowing:

- **`provisionCredentials` cannot be combined with `remoteTargetId`.** A remote
  target is reached with a device access token, whose effective roles are the
  device ones — it has no permission to register anything. Refused when the
  migration is accepted rather than discovered half way through a run. Migrate
  without credentials and provision them from the target tenant, or subscribe the
  target.
- **Devices with no external identity are skipped**, along with groups and
  devices carrying several identities and no `c8y_Serial`. Each appears in the
  job's progress with its reason. They are migrated like any other object; they
  simply arrive without credentials.

`revert` removes the device users along with the objects, in the same platform
call, so an undone migration leaves nothing behind that still authenticates.

### Device self-service credentials

The alternative to distributing a file: the devices collect their own. Ask for it
per migration with `deviceSelfCredentials: true`, and each device then calls one
endpoint to learn **both** where to reconnect and who it now is:

```sh
# as the device, with the credentials it already has for its own tenant
curl -sS -X POST -u "$SRC_TENANT/device_LINE-A-SN:$PW" \
    "https://$SRC_HOST/service/devicemigration/devices/self/migration" | jq .
```

```json
{
  "migrate": true,
  "host": "t22222.eu-latest.cumulocity.com",
  "tenant": "t22222",
  "username": "device_LINE-A-SN",
  "password": "…",
  "runId": "20260908T101112Z_ab12cd"
}
```

**This is the only endpoint that accepts a caller from outside the owner tenant**,
and the exception is deliberately narrow: device users, one endpoint, **no
parameters**. The device's identity comes from the platform rather than the
request, so there is nothing in the call to tamper with and no device id to
substitute — which is the difference between this and an API key with a
self-declared id. The external id is read out of the authenticated username, and
nothing consults an object's `owner`, which any tenant administrator can rewrite.

A migration is still started by a person through `POST /migrations`. This endpoint
neither starts nor authorises one.

Three properties worth knowing before using it:

- **The password is handed over once.** It is generated when the device asks and
  stored nowhere, so a second call answers with the endpoint and
  `alreadyIssued: true` rather than a new password. A device retrying a failed
  move needs to know where to go on every attempt, but a device that cannot keep
  its credential must not be able to rotate it forever. To re-issue, an
  administrator removes the `c8y_DeviceMigrationCredentialsIssued` fragment from
  that device's managed object in the target tenant.
- **Only migrations that asked for it are collectable.** The flag is what bounds
  this endpoint's reach to migrations somebody deliberately opened, rather than to
  every migration this service has ever run.
- **`deviceSelfCredentials` and `provisionCredentials` are refused together.** The
  first hands you a file of every password; the second lets devices collect their
  own. A device collecting one afterwards would rotate the password already in
  your file.
- **`deviceSelfCredentials` and `remoteTargetId` are refused together**, for the
  same reason `provisionCredentials` always was: issuing a device its credential
  means registering a device user in the target, and a remote target is reached
  with a device access token that has no permission to do so. This service also
  holds no client for an unsubscribed tenant, so a device asking would be told
  there is nothing to collect. Migrate such a fleet with `certificateFleet`
  instead, or subscribe the target.

Asking for it also makes each migrated object **owned by the device user it will
authenticate as** — a user that does not exist yet, which the platform accepts and
lines up when the device's credential is finally created. Without that a device
arrives bound to its own twin and unable to write it.

"Nothing to do" is a `200` with `migrate: false`, not an error: a device asking on
a timer should not fill its log with failures.

Not built, by decision: **rate limiting** per caller, and an **audit trail** of
who collected when. The issuance fragment is not that trail — it records *that* a
credential was issued, because the once-only rule needs it, not who asked or how
often.

### Moving a trust anchor, for a certificate fleet

For a certificate fleet the credential is the chain and it never changes: the
device keeps its key and its certificate, and migrating it means making the
**target** tenant trust that chain instead.

**Migrate such a fleet with `certificateFleet: true`.** It issues nothing — there
is nothing to issue — but it is not cosmetic: it makes each migrated object
**owned by the device user its certificate will authenticate as**. Without it a
certificate device arrives, auto-registers onto its migrated twin, and is silently
read-only on it, because `ROLE_DEVICE` carries writes only together with
ownership. Nothing about that failure is visible until somebody notices a fleet
that looks migrated and sends no data. It cannot be combined with either
credential option, and unlike them it **can** be combined with `remoteTargetId`:
it creates no user, only an owner on objects the remote target's own token
already owns.

Then two endpoints move the anchor:

```sh
# what can be moved out of a tenant, and what cannot
c8y api GET /service/devicemigration/tenants/t11111/trusted-certificates | jq .

# move one
c8y api POST /service/devicemigration/certificate-cutovers \
    --data '{"sourceTenant":"t11111","targetTenant":"t22222",
             "fingerprint":"f75bd686243e6916c94408e1fa65e34367136077"}'
```

**There is an outage in the cutover and no flag to flip back.** A fingerprint can
exist in only one tenant per platform instance, and disabling it in the source
does *not* free it — it is held by existence, not by status. So the operation is
delete-then-create, and between the two calls every device chained to that
certificate can reach neither tenant. If the target then refuses, the service puts
the certificate back in the source and says so; if *that* fails too, it says that
instead, which is the one case somebody has to fix by hand from the PEM the
certificate's owner holds.

That is why this is **synchronous rather than a job**: a failure has to reach the
person who asked while they are still there to act on it. Everything checkable is
checked before the source loses anything — the certificate exists and is movable,
its PEM is readable, and the target does not already have it. A refusal at any of
those points leaves the source untouched.

Two certificates are never offered, and both would otherwise be discovered *after*
the delete:

| | |
| --- | --- |
| the tenant's **own** certificate authority | cannot be moved at all. A fleet enrolled against it needs re-enrolling in the target, which is a different mechanism |
| an **expired** certificate | would produce a cutover that cannot work, after an outage that cannot be avoided |

`autoRegistrationEnabled` in the response is **read back from the target** rather
than assumed. A target that accepted the certificate while dropping that flag
would refuse every arriving device, which is a silent failure of the only thing
the operation is for.

**One thing it cannot do**, and one it does after all. It cannot tell you how many
devices a certificate serves: the platform records nothing linking a device to the
certificate that authenticates it, so the `warning` says so rather than
approximating a number.

But it **does** move the devices, which took a correction to establish. This
endpoint was written believing the tenant is resolved from the hostname a device
dialled, which would have made the cutover an authorization change and left the
fleet to be pointed elsewhere by an agent. Within one instance that is not how it
works: a connection is routed by the certificate it presents, so a device still
configured with the source host arrives in the target. Connected devices move when
they reconnect, offline devices when they return, and no device-side change is
involved at all.

**This is the same-instance case, and it is the only one this endpoint can
serve.** A fingerprint can be trusted in only one tenant *per platform instance*,
which is what forces delete-then-create and the outage. Across instances the same
certificate can be trusted in both at once, so there is no cutover to perform and
no outage to suffer — stage the CA in the target, point the devices, and remove it
again to back out. This endpoint cannot do that anyway: it resolves its target
through the subscription mechanism, and a tenant on another instance cannot be
subscribed here. See [Remote targets](#remote-targets).

Reading a truststore needs `ROLE_TENANT_MANAGEMENT_READ`, which this service
already had. Writing one needs `ROLE_TENANT_MANAGEMENT_ADMIN`, which the manifest
gained for this — so an upgrade asks each subscribed tenant's administrator to
approve it.

**The cutover leaves no record.** It creates no job, being two calls rather than a
run, so nothing afterwards reports that it happened. The listing above is the
answer instead: a certificate that has already moved is simply no longer in the
source tenant. That is also why [the UI](ui.md) offers the move for as long as the
migration stands rather than hiding it once used — it has nothing to hide it on.

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
What such a token can and cannot do was measured rather than assumed —
[docs/platform-behaviour.md](platform-behaviour.md#device-access-tokens-for-a-target-reached-by-certificate).
The flow the service exposes:

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
- **Reverting a remote-target migration works while its certificate lives.**
  Undoing needs the *same* certificate that created the objects — a device token
  deletes only what it owns — so the revert carries the migration's
  `remoteTargetId` and presents that certificate again. Which makes the
  certificate's own deadline the limit: six hours, and not across a restart,
  because the key was never written down. After that the service refuses and says
  when it expired, pointing at the command line against that tenant or at the
  target tenant's own admin. `GET /remote-targets` is what tells a client how long
  is left, and the UI uses it to stop offering the action at the same moment the
  service stops accepting it.
- **The expiry is enforced where the client is built**, so a migration and an undo
  refuse identically and neither discovers it as a 401 from the platform halfway
  through.

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
- **Issued credentials do not survive a restart.** They are held in memory on
  purpose — see [Device credentials](#device-credentials) — so a container
  replaced between the migration and the download takes them with it. The devices
  are registered and working; nobody has their passwords. Recover by registering
  again, which rotates them.
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
