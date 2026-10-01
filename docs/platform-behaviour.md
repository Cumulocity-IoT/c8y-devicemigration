# Measured Cumulocity behaviour

What this tool depends on the platform doing, established by running it against
live tenants rather than by reading the API reference. Each entry says what is
true, when it was established, and what in this repository breaks if it stops
being true.

It exists because most of these cost a day each to find and several of them
**contradicted** what the design had assumed. The wrong assumptions are kept
alongside the corrections: they are the more useful record, and every one of them
had already shaped code before it was caught.

Two conventions. *Measured* means someone ran it and read the response. *Known*
means it comes from the platform documentation or from this repository's own
history and nobody has re-run it — those are marked, and they are the ones to
distrust first.

## Certificates and which tenant a device reaches

**Within one platform instance, a connection is routed by the certificate it
presents, not by the hostname it dials.** Measured 2026-09-09: a device dialling
the *source* host with a chain only the *target* trusted got `CONNACK (0)` on
MQTT 8883 and auto-registered **in the target**, owned by its own device user,
with nothing appearing in the source. The same holds over REST — two hostnames
returned the *same* device access token, naming the tenant that trusts the chain
in `ten`, `iss` and `aud`, and it was then accepted by both hosts.

> **This replaced the opposite belief**, held for two days and recorded here
> because it survived into a design and nearly into a shipped device agent: that
> the tenant is resolved from the hostname first, so trust decides whether a
> connection is *accepted* rather than where it *lands*. On that assumption a
> trust-anchor move authorizes the target but moves nobody, and every device needs
> an agent to rewrite its URL. The measurement removed that entire half of the
> feature.

What depends on it: [`internal/service/trustanchor.go`](../internal/service/trustanchor.go)'s
cutover response tells the operator the fleet moves by itself, and a test asserts
it does **not** ask for a device-side hostname change. If this reverses, that
message strands a fleet by omission.

Scope: both tenants were platform subdomains of one instance sharing a wildcard
server certificate. A tenant on a **custom domain** is untested and is where
hostname resolution could still decide. Across instances the hostname obviously
selects the instance.

**The platform requires a leaf certificate whose issuer it trusts.** A
self-signed certificate presented directly is refused, `401 No leaf certificate
found`. Measured 2026-08-28. So the movable unit is the **CA**, never one
device's certificate — which is what makes a trust-anchor move affect every device
chained to that CA whether or not a migration covered them.

**A trusted certificate's fingerprint can exist in only one tenant per instance,
and disabling it does not free it** — it is held by existence, not by status.
Measured 2026-09-08. So moving a CA within an instance is `DELETE` from the source
then create in the target, with a window in which the fleet can reach neither
tenant, and recovery is re-creating in the source rather than reversing a flag.
Across instances the constraint does not apply and the same certificate can be
trusted in both at once.

What depends on it: the cutover endpoint is synchronous, checks everything it can
before the source loses anything, and restores the source certificate if the
target refuses the create.

**No proof of possession is demanded.** `autoRegistrationEnabled: true` was
accepted and retained with `proofOfPossessionValid: false`. Measured 2026-09-08.
So a **public** CA certificate is all that has to cross a trust boundary; nobody
needs the CA's private key.

**Auto-registration binds to an existing migrated object** rather than creating a
duplicate — the external identity still resolved to the migrated copy — but it
leaves `owner` alone. Measured 2026-09-08. It binds on the first REST token
request over mTLS on 8443, with no MQTT involved, and the token authenticates as
`device_<leaf-CN>` (measured 2026-08-28), which is a cheap way to trigger and
observe auto-registration without an MQTT client.

**A device object carries nothing naming a certificate, an issuer or a
fingerprint.** Measured 2026-09-08. So "which devices does this CA serve?" is not
answerable, at all. The cutover's coverage warning says so in words instead of
approximating a number — see `CoverageWarning` in
[`internal/service/trustanchor.go`](../internal/service/trustanchor.go).

**A TLS handshake cannot tell whether a tenant trusts a device's certificate.**
Measured 2026-09-09: run against a tenant that trusted the CA and one that did
not, `openssl s_client` to 8883 produced *identical* output — `Verify return code:
0 (ok)` both times, and no `CertificateRequest` with CA names came back. The
platform completes the TLS connection and decides authorization later, at the MQTT
CONNECT. To test the real property, ask for a device access token over HTTPS and
read the `ten` claim; a tenant that does not trust the chain answers 401.

## Ownership, and what `ROLE_DEVICE` can do

**`ROLE_DEVICE` carries writes only together with source-ownership.** A device
bound to an object it does not own is **read-only on its own twin**: `403` on
updating the object, `403` on posting a measurement. Measured 2026-09-08. This is
the single fact that shaped all three credential mechanisms — a migrated object is
owned by whoever imported it (`service_devicemigration`), so binding a device to
its twin is not enough to let it send data.

**A managed object's `owner` can be set to a `device_` user that does not exist
yet**, and the platform lines the two up when that user is later created: the
late-created user could `PUT` its own twin (`200`) and post a measurement
(`201`). Measured 2026-09-08. This is what makes the previous fact fixable *up
front* rather than by a pass over every device afterwards.

What depends on it: `--stampDeviceOwner` in
[`internal/importer/deviceowner.go`](../internal/importer/deviceowner.go) stamps
`owner: device_<externalId>` at import time, before any device connects. The rule
that picks the external id is written twice — there and in the registration phase
— with a test that the two agree, because an owner that disagrees with the user
later created is an object nobody will ever be.

**A user who is not the owner can `PUT` a device-owned object, and `owner`
survives the update untouched.** Measured 2026-09-07, as a tenant administrator;
the permission is `ROLE_INVENTORY_ADMIN`. So the import can update objects that
bulk registration created without taking them over.

**A managed object's `owner` is writable by anyone who administers the tenant**,
and a device user can reassign the owner of its own object. Measured 2026-09-08.
So `owner` is a field, not evidence: "the object this caller owns" is not an
authorization signal, and the device self-service endpoint deliberately runs its
chain from the authenticated username instead. It also means a revert can find an
object whose owner names a user that no longer exists, which the reverter's retry
covers.

## Bulk device registration

**`/devicecontrol/bulkNewDeviceRequests` reads tab-separated and
comma-separated files alike.** Retested 2026-09-09, four uploads differing only
in delimiter and column set: go-c8y's own tab-separated output, the same file
with tabs swapped for commas, this tool's six columns as tabs, and this tool's
file as written. **All four answered `201 numberOfCreated: 1`**, and all four
parsed every field — `ID`, `NAME`, `TYPE` and `IDTYPE` each landed in the right
place, with the object owned by `device_<externalId>`.

> **This overturns a finding recorded as settled on 2026-09-07**, which said the
> endpoint *rejects* a tab-separated file and that go-c8y's
> `model.BulkRegistrationRecordWriter` (which sets `csvWriter.Comma = '\t'`) is
> therefore unusable. It was inferred from one failed upload rather than isolated:
> that upload used an external id the tenant already knew, and the endpoint's
> answer for *that* is a `numberOfFailed: 1` carrying
> `"Error during password validation."` — a message this page separately records
> as untrustworthy. The delimiter was blamed for a failure the known id caused.
> The narrower claim that may still hold is about Cumulocity's **bulk import
> screen** in the UI rather than this API endpoint; that was never tested and is
> not what the code relied on.

What depended on it, and what was corrected once this was retested: the fake
platform in [`internal/testfixture/server.go`](../internal/testfixture/server.go)
*refused* any file containing a tab, so the test suite enforced a platform rule
that does not exist. It now sniffs the delimiter as the real endpoint does and
records it, and the tests pin the comma as **this tool's** choice instead.

[`internal/registration/file.go`](../internal/registration/file.go) still writes
the file itself, and the reasons survive the correction because none of them was
ever the delimiter: `model.BulkRegistrationRecord` has a fixed column list with
no way to add the dotted marker column, and its `SetBasicAuth` forces
`com_cumulocity_model_Agent.active`, which is the one column that has to be
**absent** rather than false. Commas remain the choice because the file is handed
to an operator who will open it in a spreadsheet — which is also why the generated
passwords exclude a comma.

**A successful row creates the managed object, the device user and the external
identity in one call**, owned by `device_<externalId>`. No device has to connect
for the object to appear. Measured 2026-09-07.

**A row for an external id the target already knows creates nothing.** It returns
`bulkNewDeviceStatus: CREDENTIAL_UPDATED` in `credentialUpdatedList`, sets a new
password, and leaves the managed object alone — including its `owner`. Measured
2026-09-07.

Those two together are why credential provisioning **registers before it
imports**, and then updates with a `PUT`: after an import the identity already
exists, so registration could only ever rotate a password, never produce an object
the device owns. It is also the rotation mechanism, so re-registering replaces the
old password rather than leaving both working.

**The endpoint's error text cannot be trusted.** The same password that was
refused with `"Error during password validation."` for a known id succeeded for a
fresh id in the next call. Measured 2026-09-07. Do not let this endpoint's message
drive a diagnosis, in tests or in error handling.

**Custom columns map onto fragments through dotted headers**, so registration can
stamp a run marker itself with a `c8y_DeviceMigration_<runid>.phase` column.
Values arrive as **strings**: `{}` in a data row is the literal string `"{}"`, and
arrays are impossible. Measured 2026-09-08. The consequence is that a column must
be **omitted** rather than set false — an agent column reading `"false"` would
produce `{"active": "false"}`, not the absence of an agent fragment.

**A minimal column set is accepted**, and registration adds nothing to an object
that the source's copy would not have, so the following `PUT` does not have to
clear fragments. Measured 2026-09-08.

**A device user can be created for an external identity the tenant already
knows.** Measured 2026-09-08 — and this call answered `201` with an **empty
body** where it normally returns a summary, so issuance has to be confirmed by
reading the user back rather than by parsing the response.

**A tenant's password strength policy requires a symbol**: a 15-character
alphanumeric password was refused on a live tenant, 2026-09-08. Generated
passwords are 15 characters over letters, digits and 27 punctuation characters;
`,` `"` `'` `\` and a backtick are excluded — the first two because the files
these travel through are comma-separated and the endpoint has not been shown to
honour RFC 4180 quoting, the rest because an operator is told to paste them inside
single quotes. See [`internal/registration/password.go`](../internal/registration/password.go).

**The registration file has no escaping. The endpoint splits on every comma and
trims quotes rather than unescaping them.** Measured 2026-10-01, with a file
written by `registration.WriteRegistrationFile` itself so the encoding was the
tool's own:

| written | arrived as |
| --- | --- |
| `"RFC4180-B,RUN1"` as the external id | split at the comma: `RFC4180-B` in `ID`, `RUN1` in `AUTH_TYPE`, answered `has unknown auth type: 'RUN1'` |
| `"Pump, north"` as the name | split at the comma, and **created anyway**: name `"Pump`, type `north"`, external id type `c8y_Test` — the `TYPE` value — and the marker column carrying `c8y_Serial` |
| `"Pump ""North"""` as the name, RFC 4180's encoding of `Pump "North"` | `Pump ""North` — one quote trimmed off the front, three off the back, the doubled pair left as it was |

Three things follow, and the second is the one that matters.

**A shift that starts after `CREDENTIALS` is accepted silently.** The row with a
comma in its external id failed only because the text after the comma landed in
`AUTH_TYPE`, which the platform validates; nothing after `CREDENTIALS` is
validated at all. The device in the second row was registered under
`c8y_Test:RFC4180-A-RUN1` instead of `c8y_Serial:…`, so the import's adoption —
which looks the identity up as the export carried it — would not resolve it, and
would create a **second** object. One object owned by the device user with no
data, one with the data owned by the service user, and a device that arrives
unable to write its own twin.

**The marker survives a shift.** It receives the wrong *value* (`c8y_Serial`
rather than `registered`), but the fragment is still created, and both the
import's adoption check and the revert's query test for its **presence**. An
earlier reading of this page's own warning said a shift could suppress the marker;
it cannot.

What depends on it:
[`internal/registration/file.go`](../internal/registration/file.go) refuses a
comma, a quote or a line break in **any** field rather than relying on quoting,
and [`internal/registration/registration.go`](../internal/registration/registration.go)
skips such a device while planning so one device's name cannot cost a fleet its
credentials. Altering the value instead was considered and rejected: a name is
overwritten by the import moments later and could have been stripped, but a tool
that silently changes what it was asked to migrate is worse to own than one that
says it cannot. The estimate and the credential pre-flight apply the same rule,
so the count in the dialogue cannot promise a credential the run then skips.

> **This replaced an open question, not a wrong finding.** The page previously
> recorded that quoting was *untested* and named the exact failure it would cause
> if unsupported. It was tested, it is unsupported, and the failure is the one
> that was predicted — with the addition that the dangerous half is silent.

## Device users calling a microservice

**The platform's API gateway forwards a device user's credentials to a
microservice.** `/health` answered `200` with device credentials, and an
authenticated endpoint answered `403` **with this service's own message** — so the
request reached our authorizer having been proven against the platform. Measured
2026-09-08.

An administrator of the same subtenant gets the *identical* `403`, so a device
user is not treated specially by the platform. Any exception for devices has to be
written for device users in particular, not for subtenant users in general — which
is what `AuthorizeDevice` in
[`internal/service/deviceself.go`](../internal/service/deviceself.go) does, and why
`device_` being a naming convention rather than a protected namespace is recorded
there as a soft edge rather than a boundary.

**A device user can resolve the device it owns with no extra roles:**
`?owner=device_<id>` returned exactly one object, its own; `/user/currentUser`
answered with its own name; and it could read its own external identity. Measured
2026-09-08.

## Deleting

**`DELETE` on a managed object takes `withDeviceUser`**, which removes the
associated device user along with the device. Known, from the [platform
documentation](https://cumulocity.com/api/core/#operation/deleteManagedObjectResource);
go-c8y exposes it as `managedobjects.DeleteOptions.WithDeviceUser`. A complete
revert therefore costs no user-management role and no second request.

**But it must not be set unconditionally.** A device with no device user returns
`409` and **survives** the delete — found by a regression that shipped green
because the fake platform accepted any query parameter. The reverter reads
`owner` from the plan and only sets the flag where there is a user to remove; the
fake now returns the `409`.

## Device access tokens, for a target reached by certificate

Measured 2026-08-28, and the basis for [remote
targets](microservice.md#remote-targets): a device access token obtained over mTLS
can run a **full import**.

| Operation | Result |
| --- | --- |
| create managed object | `201` |
| update own object (`PUT` — marker, resume) | `200` |
| create external identity | `201` |
| create child object, assign child device | `201`, `201` |
| create measurement / event / event binary / alarm | `201` each |
| delete own object (what a revert does) | `204` |

The token's effective roles are `INVENTORY_CREATE` + `INVENTORY_READ`,
`IDENTITY_ADMIN` + `IDENTITY_READ`, `MEASUREMENT/EVENT/ALARM_READ`, `DEVICE`,
`DEVICE_CONTROL_READ`, `AUDIT_READ`, `USER_MANAGEMENT_OWN_*`. The data writes
succeed on `_READ` roles alone because `ROLE_DEVICE` plus source-ownership carries
them, and the importer creates every object with this identity — so it owns all of
them.

Two consequences carried into the build:

- **No manifest change for the source side.** The target is reached with a token
  derived from a certificate, not with a service user.
- **A revert needs the same key.** A device token deletes only what it *owns*, and
  the migrated objects are owned by that migration's device user. A *fresh*
  certificate is a different user that owns nothing and is refused, which is why
  the remote target's certificate — held in memory, valid six hours — is what
  bounds how long such a migration can be undone from the service.

Bulk registration is **not** available to such a token, which is why provisioning
credentials is refused together with a remote target.

## Where the reasoning lived

This page keeps the findings. The options that were weighed to arrive at them —
three ways to give a migrated device its credentials, five ways to reach an
unsubscribed target, and the procedures that settled each assumption with its
answer beside it — were concept documents under `docs/concepts/`, removed once all
of it was built. They are in the git history if the *rejected* alternatives ever
matter again; what was built is documented in
[microservice.md](microservice.md#the-three-ways-credentials-arrive) and
[cli.md](cli.md#device-credentials).
