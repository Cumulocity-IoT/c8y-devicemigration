# Migrating to a remote or unsubscribed target tenant

> **Status: Option 4 is being built.** The certificate-exchange direction was
> [verified on a live tenant](#verified-on-a-live-tenant) and the first increment
> is implemented: the service establishes a remote target
> (`POST /remote-targets`), keeps the key in memory, and migrates against it, and
> the UI has a panel for it. Not yet done: an end-to-end run against a live remote
> tenant, and a proper answer for reverting a remote-target migration (today it is
> refused with guidance). The other options below remain the record of why this
> one was chosen. As this settles, its behaviour graduates into
> [the microservice docs](../microservice.md#remote-targets) and this file is
> retired.

## The problem

The microservice migrates a device hierarchy from a source tenant to a target
tenant. Today both must be **subscribed to this microservice instance** — the
service gets its credentials from the platform's subscription mechanism, and a
target it has no service user for is refused with `tenantNotSubscribed`. That
rules out three cases somebody legitimately wants:

- **T1** — a tenant on the same platform that simply is not subscribed.
- **T2** — a tenant under a *different parent tenant*. Subscription cannot reach
  across parents at all.
- **T3** — a tenant on a *different Cumulocity system* entirely.

The goal is to support these conveniently but securely, **without users handing
their personal credentials to the microservice**, and ideally without leaving the
migration UI. One constant shapes every option: Cumulocity accepts no
unauthenticated request — even a call to a microservice passes the platform's API
gateway, which authenticates the caller first.

## What is already true (verified in the code)

- **The engine is already cross-platform.** A migration runs on two independent
  clients, each built for its own host and credentials
  ([internal/c8yclient/client.go](../../internal/c8yclient/client.go)); the
  same-tenant refusal compares host *and* tenant on purpose
  ([internal/session/session.go:210](../../internal/session/session.go#L210)); the
  engine performs no same-platform check. The command line already migrates
  across platforms with two session files — only the *service's* way of getting
  target credentials is tied to subscriptions.
- **The service-side change is one function.** `tenant(name)`
  ([internal/service/bootstrap.go:390](../../internal/service/bootstrap.go#L390))
  resolves a subscription service user; a second way to produce the same client
  object, for a target that is not subscribed, is the whole of it.
- **A credential must never touch the job record.** The entire migration request
  is embedded in the job, which is stored as a managed object in the owner tenant,
  kept by default forever, rewritten every ten seconds, echoed in every job
  response, and forced into the **publicly served** OpenAPI document by the parity
  tests. Any target secret therefore has to live in memory, keyed by the job, with
  only the non-secret host and tenant on the record. A credential dying with the
  container is consistent with what already happens — a restart abandons running
  jobs and marks them interrupted.
- **The library already speaks certificates.** Its client accepts a client
  certificate and, given one, obtains and renews device access tokens over mTLS on
  port 8443 by itself — so the option chosen below needs almost no new token
  plumbing.

## The options

Each is described on its own, then scored together.

### Option 1 — manual credentials

The dialogue grows an *Advanced* panel: target host, tenant, username, password,
and a TOTP field for tenants that enforce it. The service exchanges them for a
token and holds everything in memory for the job only, recording none of it. It
is the one option that works with no prerequisite anywhere — but it is also the
one that puts a person's own credential into the service, which is exactly what
the goal asks to avoid. It ships, but **demoted**: behind the expander, with a
recommendation to create a dedicated user first, because the command line can
already do the same thing and refusing it in the UI would only push people toward
handling those secrets worse. SSO-only users have no password and cannot use it.

### Option 2 — instance pairing

Two deployments of the service establish standing trust once, after which the
source lists the remote tenants like local ones and mints a short-lived grant per
migration. It has the best experience for estates that migrate to each other
*repeatedly* — and by far the largest build: a remote-facing API, key lifecycle
and revocation on both sides, version-skew between instances, and persistent
encrypted secrets the service has no permission to store today. **Deferred**: the
migrations in view are one-off, which is the case this does not pay for.

### Option 3 — a grant string from the target's UI plugin

A target-tenant administrator opens the migration plugin *in the target tenant*,
clicks "create migration grant", and the plugin — using that admin's own session —
creates a purpose-named technical user holding exactly the import roles and shows
one copyable string carrying its host, tenant, username and password. The source
operator pastes it; the service uses it in memory only. It needs no service on the
target, only the plugin. The pasted string is a bearer-equivalent secret, but it
is nobody's *personal* credential: purpose-named, least-role, revoked by deleting
the user, and every import is attributed to that named user. Its cleanup is
manual — the grant user deliberately cannot delete itself — so the plugin carries
a list of grants with their age and a revoke button. A strong option, and the
designated fallback where a target admin would rather mint a technical user than
register a certificate.

### Option 4 — certificate exchange (chosen)

Cumulocity issues a JWT to the holder of a trusted client certificate: a device
presents its certificate over mTLS on port 8443
(`POST /devicecontrol/deviceAccessToken`), receives an access token, and uses that
token for ordinary REST calls. That turns a **public** certificate into the thing
to copy, and keeps every secret on the source side:

1. In the source migration dialogue, the service generates an ephemeral CA and a
   leaf certificate signed by it, named for this migration. It shows the operator
   the **public CA certificate** — that is the string to carry over. Both private
   keys stay in the service. (The CA/leaf split is required: the platform accepts
   a *leaf* presented over mTLS whose issuer it trusts, not a self-signed
   certificate presented directly — see the verification section.)
2. On the target, an administrator registers that **CA** as trusted with
   auto-registration enabled — in Cumulocity's own Administration screen, so **no
   plugin is required on the target**, or in one click if the plugin is installed.
3. The source service builds the target client from the leaf and its key; the library
   obtains and renews the mTLS access tokens on its own, and the migration runs.
   When it finishes, the source drops the key, after which the trusted certificate
   in the target is inert whether or not anyone deletes it.

What makes this the pick: the material that crosses the gap is public and useless
to an interceptor; the private key is generated, used and destroyed entirely
inside the service; revocation is native (delete the trusted certificate) and can
also be done unilaterally from the source by discarding the key; and the library
already does the hard part. The device role set was the open question — whether a
device token may write the full import surface — and it was
[verified](#verified-on-a-live-tenant) to be sufficient, on objects the token
owns, which are exactly the ones an import creates. Auto-registration leaves one
throwaway "device" object standing for the importer, deleted afterwards; and a
revert must re-supply the same certificate, because a device token deletes only
what it owns (both points in the verification section).

### Option 5 — subscribe for the duration (T1 only)

For a same-parent unsubscribed target, the sanctioned path already exists:
subscribe the tenant, migrate, unsubscribe. No credential machinery at all — but
it covers only T1, and Option 4 covers T1 too, so it is **deferred** rather than
built as a second flow.

### The command line — the baseline

Cross-platform migration works today from the command line with two session files
and no service at all. It is the yardstick every option above must beat, and it
beats them all except on convenience — which is the whole point of doing this in
the UI.

### Rejected

- **A pasted bearer token** on its own: it cannot be renewed and expires in the
  middle of an hour-long migration. Option 1's token exchange keeps the useful
  part.
- **Certificates as a device-only idea.** An earlier draft of this concept
  rejected Option 4 on the belief that certificate authentication in Cumulocity is
  only for devices over MQTT. That was wrong: the 8443 device-access-token flow
  above issues a JWT usable on the normal REST API, which is what makes Option 4
  viable. The correction is kept here deliberately, as a reminder of how easily an
  assumption about platform behaviour becomes a wrong "fact".

## Criteria and scores

Judged on: **credential exposure** (whose secret, where it rests, blast radius);
**coverage** of T1/T2/T3; **operator convenience**; **target-side prerequisites**;
**implementation cost and new attack surface** (5 = cheap and small); **audit and
lifecycle**; and **long-job robustness** (survives hours, restarts, token expiry).

| Option | Exposure | Coverage | Convenience | Prereqs | Cost | Audit | Robustness | Σ |
|---|---|---|---|---|---|---|---|---|
| **4** certificate exchange | 5 | 5 | 3 | 4 | 3 | 4 | 5 | **29** |
| **3** grant string (plugin) | 3 | 5 | 4 | 4 | 4 | 4 | 5 | **29** |
| 5 subscribe for the duration | 5 | 2 | 4 | 3 | 4 | 4 | 5 | 27 |
| Command line (baseline) | 3 | 5 | 1 | 5 | 5 | 2 | 4 | 25 |
| 1 manual credentials | 1 | 4 | 3 | 5 | 5 | 2 | 4 | 24 |
| 2 instance pairing | 4 | 4 | 4 | 2 | 1 | 4 | 4 | 23 |
| — pasted token | 2 | 3 | 2 | 5 | 5 | 3 | 1 | 21 |

Options 4 and 3 tie; Option 4 leads on credential exposure (the copied material is
public, not a live credential), and 3 leads on convenience (no certificate step).
Option 4's coverage and robustness were confirmed by the verification below, so it
is the pick; Option 3 stays the fallback where a target admin would rather mint a
technical user than register a certificate.

## The plan

1. **Build Option 4** as the primary path (verification passed), with **Option
   3's grants page as the model** for a certificate-lifecycle page, and **Option
   1 alongside** as the demoted universal fallback.
2. **Defer** Options 2 and 5; keep the command line documented as the escape
   hatch.
3. Across all of them: the target secret lives in memory keyed by the job, only
   host and tenant reach the record, and **reverting a remote-target migration
   re-supplies the credential** — for Option 4 that is the same certificate and
   key (see the verification section on why a fresh one will not do).

## Verified on a live tenant

Run against a training tenant on `eu-latest` (2026-08-28), and Option 4 works
**as designed — no added role-grant step**. What the checks established:

- **Certificate shape.** A self-signed certificate presented directly is refused
  (`401 No leaf certificate found`): the platform requires a **leaf** certificate
  whose **issuer is trusted**. So the copyable material is a **CA certificate**
  registered on the target with auto-registration; the source holds a leaf signed
  by it and presents the leaf to `:8443`. One PEM still crosses the gap (the CA),
  and it is public.
- **Auto-registration** binds on the first REST token request over mTLS, no MQTT
  involved. The token authenticates as `device_<leaf-CN>`.
- **The make-or-break — a device access token can run a full import.** Every
  operation returned success:

  | Operation | Result |
  | --- | --- |
  | create managed object | `201` |
  | update own object (PUT — marker / resume) | `200` |
  | create external identity | `201` |
  | create child object, assign child device | `201`, `201` |
  | create measurement / event / event binary / alarm | `201` each |
  | delete own object (what a revert does) | `204` |

  The device token's effective roles are `INVENTORY_CREATE` + `INVENTORY_READ`,
  `IDENTITY_ADMIN` + `IDENTITY_READ`, `MEASUREMENT/EVENT/ALARM_READ`,
  `DEVICE`, `DEVICE_CONTROL_READ`, `AUDIT_READ`, `USER_MANAGEMENT_OWN_*`. The
  data writes succeed on only `_READ` roles because `ROLE_DEVICE` plus
  **source-ownership** carries them — and the importer creates every object with
  this same identity, so it owns all of them.

**Two consequences to carry into the build:**

1. **No manifest change on the source side.** The target is reached with a device
   token obtained from a certificate, not with a service user, so the service's
   `requiredRoles` are untouched. The one platform permission involved is the
   *target admin's* right to register a trusted certificate — their action, on
   their tenant.
2. **Revert needs the same key, or an admin.** A device token can delete only
   what it *owns*. The migrated objects are owned by the migration's device user
   (`device_<CN>`), so a revert must run under the **same** certificate/key — a
   *fresh* certificate is a different user that owns nothing and would be refused.
   Since the service drops the key when the job ends, the implementation must
   either (a) have the operator keep and re-supply the same CA/leaf/key to revert,
   or (b) revert through the target admin's own credentials or the CLI. The
   concept's "revert re-prompts for the credential" sharpens to "revert re-supplies
   the same certificate" for this option; the design round picks between (a) and
   (b).
