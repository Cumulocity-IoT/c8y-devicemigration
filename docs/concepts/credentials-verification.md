# Verifying the credential concepts on a live tenant

Every assumption in [credentials.md](credentials.md) that has not been seen
working, turned into a procedure somebody can run.

**Option A's are done, and it is built.** Between them A1, A7, A8 and A9 reshaped
the option rather than confirming it: bulk registration will not give a device
ownership of an object that already exists, so credentials are issued *before* the
import; the import updates those objects with ownership intact; registration
stamps the run marker itself, so both phases are recoverable; and the manifest
needed exactly one role. What the A-tests found and what it cost is in
[credentials.md § Settled](credentials.md#settled). They are kept here rather than
deleted, because a procedure with its answer beside it is how the next reader
learns that this endpoint's error messages cannot be trusted.

**What is left belongs to Options C and B.** Two can still sink the option they
belong to — **C3** and **B1** — and **C6** decides whether the manifest has to
change again, which every subscribed tenant's administrator must approve on
upgrade. Those three first; the rest sharpen a design rather than deciding it.

Results go back into [credentials.md](credentials.md) as a `## Verified on a live
tenant` section, in the style
[remote-targets.md](remote-targets.md#verified-on-a-live-tenant) already uses:
what was run, on what date, what came back, and the consequences to carry into the
build. **Record the failures and the surprises too** — the value of that section in
`remote-targets.md` is that it keeps a wrong assumption on the record instead of
quietly deleting it.

## Before you start

**Tenants.** Two on the same instance — `SRC` and `TGT` — with an administrator on
each, plus the parent/management tenant if you want to test through the deployed
microservice rather than by hand. Everything below works against training tenants;
none of it needs production.

**Never test the cutover against a CA a real device uses.** [C2](#c2--can-the-same-certificate-exist-in-both-tenants)
disables a trusted certificate, and every device chained to it loses its
connection. All the certificate tests use a throwaway CA created in step S2 that
nothing else trusts, so enabling and disabling it affects nothing.

### S1 — Record the two tenants

```sh
c8y api GET /tenant/currentTenant --session "$SRC_SESSION" | jq '{name, domainName}'
c8y api GET /tenant/currentTenant --session "$TGT_SESSION" | jq '{name, domainName}'
```

Keep the tenant ids to hand — the trusted-certificate paths need them
(`/tenant/tenants/{tenantId}/trusted-certificates`), and unlike most of the API
they will not accept a placeholder.

### S2 — A throwaway CA and one leaf per test device

```sh
openssl req -x509 -newkey rsa:2048 -nodes -days 30 \
    -keyout ca.key -out ca.pem \
    -subj "/CN=migtest-ca" \
    -addext "basicConstraints=critical,CA:TRUE" \
    -addext "keyUsage=critical,keyCertSign,cRLSign"

openssl req -new -newkey rsa:2048 -nodes \
    -keyout dev.key -out dev.csr -subj "/CN=MIGTEST-001"
openssl x509 -req -in dev.csr -CA ca.pem -CAkey ca.key \
    -days 30 -set_serial 1001 -out dev.pem
```

The leaf's **CN must equal the external id** the migrated device carries — that is
the binding [C3](#c3--does-auto-registration-bind-to-the-migrated-object) exists to
test. Keep `ca.key`: [C4](#c4--does-the-target-demand-proof-of-possession) needs it,
and the point of that test is that a real migration would *not* have it.

`certInPemFormat` wants the base64 body without the PEM armour or newlines:

```sh
CA_BODY=$(grep -v -- '-----' ca.pem | tr -d '\n')
```

If a create is rejected, retry with the full armoured PEM before concluding
anything about the request — which form the endpoint wants has changed between
versions, and it is worth recording which one worked.

### S3 — A migrated hierarchy to test against

The credential tests need a device that exists in both tenants with the same
external identity. Migrate a small hierarchy with the tool, and record per device:
the source managed-object id, the target managed-object id, and every external
identity with its type.

```sh
c8y api GET "/identity/externalIds/c8y_Serial/MIGTEST-001" --session "$TGT_SESSION" \
    | jq '{externalId, type, mo: .managedObject.id}'
```

**If the tool's own first live run is not something you want tangled up in these
results**, build the target side by hand instead — a managed object with the run's
marker fragment and an external identity of type `c8y_Serial` — and the credential
tests become independent of it:

```sh
c8y api POST /inventory/managedObjects --session "$TGT_SESSION" \
    --data '{"name":"MIGTEST-001","type":"c8y_MigrationTest","c8y_IsDevice":{},"c8y_DeviceMigration_testrun":{}}'
c8y api POST "/identity/globalIds/<newId>/externalIds" --session "$TGT_SESSION" \
    --data '{"type":"c8y_Serial","externalId":"MIGTEST-001"}'
```

Use `MIGTEST-001` for the bind tests and a second, **unregistered** id such as
`MIGTEST-999` as the control in [A1](#a1--does-a-bulk-row-with-an-existing-external-id-bind-or-duplicate).

### S4 — Record the tooling

`c8y version`, `openssl version`, `curl --version`, and whether the instance is
`eu-latest` or another stage. Two of the questions below (the PEM form, and
proof-of-possession) are version-sensitive, so a result without a version is a
result that has to be re-run later.

## Settled already

| Fact | How | Where |
| --- | --- | --- |
| The tenant is resolved from the **hostname** the device connected to; the certificate chain is checked afterwards, against that tenant. | Confirmed on a live instance, 2026-09-07 | [credentials.md § Settled](credentials.md#settled) |
| Auto-registration binds on the first mTLS request to `POST /devicecontrol/deviceAccessToken` on port 8443 — no MQTT needed — and the resulting token authenticates as `device_<leaf-CN>`. | Verified 2026-08-28 | [remote-targets.md](remote-targets.md#verified-on-a-live-tenant) |
| A self-signed certificate presented directly is refused (`401 No leaf certificate found`); the platform wants a leaf whose **issuer** is trusted. | Verified 2026-08-28 | [remote-targets.md](remote-targets.md#verified-on-a-live-tenant) |

The first of these is why Option C acquired a device-side half. If you want its
evidence in the record rather than just the conclusion, the cheapest capture is one
`mosquitto_pub` with `dev.pem` against the **source** hostname while the CA is
enabled only in the **target**, and the refusal it produces.

## The tests

| | Question | Blocks | Rough cost |
| --- | --- | --- | --- |
| [A1](#a1--does-a-bulk-row-with-an-existing-external-id-bind-or-duplicate) | Does a bulk row with an existing external id bind, duplicate, or fail? | Option A entirely | **answered** — it updates the credential and leaves the object alone, which is why registration now runs first |
| [A7](#a7--can-the-service-user-put-an-object-owned-by-a-device-user) | Can a non-owner `PUT` a device-owned object without taking it over? | Option A's new step 3 | **answered** — yes, `owner` survives; the permission half folds into A2 |
| [A2](#a2--what-permissions-does-bulk-registration-need) | What permissions do registration, the step-3 `PUT` and **deleting a device user** need? | manifest change | **answered** — `ROLE_DEVICE_CONTROL_ADMIN` for registration, `ROLE_INVENTORY_ADMIN` for the `PUT`, and nothing at all for the user, which `withDeviceUser` removes with the object |
| [A8](#a8--can-bulk-registration-stamp-the-run-marker) | Can registration stamp the run marker via a fragment column? | whether `--resume` and `revert` work between the two phases | **answered** — yes, via a dotted header; values are strings and arrays are impossible |
| [A9](#a9--what-does-registration-add-that-the-source-object-does-not-have) | What does registration add that the source object lacks? | whether step 3's `PUT` must clear fragments | **answered** — nothing that needs clearing, and a minimal column set is accepted |
| [A3](#a3--does-the-tenant-accept-an-administrator-chosen-password) | Does the tenant accept an administrator-chosen password? | usability of the file | **answered** — yes, but its strength policy needs a **symbol**: an alphanumeric one was refused |
| [A4](#a4--what-happens-when-the-same-device-is-registered-twice) | Does rotation really replace the old password? | the "I lost the file" answer | **answered** — the new one works and the old one stops |
| [A5](#a5--does-the-device-user-work-end-to-end) | Do operations and child devices survive the adoption? | nothing — an acceptance check for the first live run | **not run**, and not a gate: see below |
| [A6](#a6--can-a-managed-objects-owner-be-reassigned) | Can a managed object's owner be reassigned? | nothing — A7 passed | **not needed**, kept as a dormant fallback |
| [C2](#c2--can-the-same-certificate-exist-in-both-tenants) | Can the same certificate exist in both tenants, one disabled? | cutover shape | 30 min |
| [C3](#c3--does-auto-registration-bind-to-the-migrated-object) | Does auto-registration bind to the migrated object? | Option C entirely | 45 min |
| [C4](#c4--does-the-target-demand-proof-of-possession) | Does the target demand proof of possession? | Option C's premise | 30 min |
| [C5](#c5--which-trusted-certificates-can-be-moved-at-all) | Which certificates are movable? | scope of the first cut | 15 min |
| [C6](#c6--what-permissions-do-trusted-certificates-need) | What permissions do trusted certificates need? | manifest change | 30 min |
| [C7](#c7--can-a-device-be-attributed-to-its-ca) | Can a device be attributed to its CA? | the coverage warning | 30 min |
| [C8](#c8--does-the-device-side-agent-complete-the-move) | Does the device-side agent complete the move? | Option C end to end | 2 h, needs a device |
| [B1](#b1--can-a-device-user-call-a-microservice-at-all) | Can a device user reach a microservice at all? | Option B entirely | 30 min |
| [B2](#b2--can-the-service-resolve-the-device-a-caller-owns) | Does "the device a caller owns" resolve to one object? | Option B's authorization | 30 min |
| [B3](#b3--what-does-a-gateway-owning-children-look-like) | What does a gateway owning children look like? | Option B's edge case | 30 min |

**Option A's questions are all closed.** A5 is the one procedure never run, and it
is not a gate: it asks whether an adopted device's operations and child devices
behave, which is an acceptance check against a real fleet rather than a design
question — everything it could discover is a bug to fix, not a decision to take.
It belongs to the first live migration, alongside the residual "does the endpoint
honour quoted commas" risk noted in
[credentials.md](credentials.md#to-confirm-on-a-live-tenant-before-building).

A5 and C2–C7 need only S1–S3. C8 needs a real thin-edge device. B1–B3 need the
microservice deployed and the source tenant subscribed.

## Option A — bulk credential provisioning

### A1 — Does a bulk row with an existing external id bind or duplicate?

**Why it matters.** Option A is written as *"creates the device users in one bulk
registration, binding each new user to the migrated object through its external
identity"*. Every migrated device already has that identity, put there by the
import. If bulk registration instead creates a second managed object, or refuses
the row, the option has to be rebuilt around creating users directly.

**Run the control first**, with an external id the target has never seen, so you
know what an unimpeded bulk registration produces and can tell "bound to the
existing object" from "created a fresh one".

**1. Pre-state.** For both `MIGTEST-001` (exists) and `MIGTEST-999` (control):

```sh
c8y api GET "/identity/externalIds/c8y_Serial/MIGTEST-001" --session "$TGT_SESSION"
c8y api GET "/inventory/managedObjects?text=MIGTEST&pageSize=100" --session "$TGT_SESSION" \
    | jq '[.managedObjects[] | {id, name, owner, type}]'
c8y api GET "/user/$TGT_TENANT/userByName/device_MIGTEST-001" --session "$TGT_SESSION"
```

Record the managed-object ids, their `owner`, and that the device users do not yet
exist (expect `404`).

**2. The file.** **Comma**-separated, with the column names the library uses. The
delimiter is not a free choice and not the one the library writes — see the
warning below:

```sh
printf 'ID,AUTH_TYPE,CREDENTIALS,NAME,TYPE,IDTYPE,ICCID,TENANT,PATH,com_cumulocity_model_Agent.active\n' > devices.csv
printf 'MIGTEST-001,BASIC,%s,MIGTEST-001,c8y_MigrationTest,c8y_Serial,,,,true\n' "$PW1" >> devices.csv
printf 'MIGTEST-999,BASIC,%s,MIGTEST-999,c8y_MigrationTest,c8y_Serial,,,,true\n' "$PW2" >> devices.csv
```

> **The endpoint wants commas; `model.BulkRegistrationRecordWriter` writes tabs.**
> Observed 2026-09-07: a tab-separated file is rejected on import, the same content
> comma-separated is accepted. The vendored library sets `csvWriter.Comma = '\t'`,
> so **Option A cannot use that writer as it stands** — it produces a file this API
> refuses. Worth raising upstream, and worth a test of our own on whatever we do
> use. An earlier version of this document asserted the opposite, on the strength
> of reading the library instead of trying it.

Ten fields per row, every row. Check before uploading — a stray separator shifts
every column right of it, and `IDTYPE` landing in `ICCID` produces a registration
that succeeds and tests nothing:

```sh
awk -F',' '{print NR": "NF" fields"}' devices.csv
```

Passwords 8–32 characters, and **no commas** — a comma in a value needs CSV
quoting, which is one more thing to get wrong in a file whose parse errors are
reported as `"Error during password validation."`. `GeneratePassword` is already
constrained to symbols the bulk API accepts.

**3. Upload.**

```sh
curl -sS -X POST "https://$TGT_HOST/devicecontrol/bulkNewDeviceRequests" \
    -H "Authorization: Basic $TGT_BASIC" \
    -F "file=@devices.csv;type=text/csv" | jq .
```

The library sends a second `object` JSON part alongside `file`; if the endpoint
rejects the request outright, add `-F 'object={"name":"devices.csv","type":"text/csv"};type=application/json'`
before concluding anything.

**4. Read the response.** Record `numberOfAll`, `numberOfCreated`,
`numberOfSuccessful`, `numberOfFailed`, and both lists — `credentialUpdatedList`
and `failedCreationList`, including each entry's `failureReason` and `line`. The
distinction between "created" and "credential updated" is the answer to
[A4](#a4--what-happens-when-the-same-device-is-registered-twice) as well.

**5. Post-registration state.**

> **Observed 2026-09-07**, on a target tenant holding a real migrated copy of
> `MIGTEST-001`. The control row succeeded; the existing-id row did not:
>
> ```json
> { "numberOfAll": 2, "numberOfCreated": 1, "numberOfSuccessful": 1,
>   "numberOfFailed": 1, "credentialUpdatedList": [],
>   "failedCreationList": [ { "deviceId": "MIGTEST-001",
>       "bulkNewDeviceStatus": "FAILED",
>       "failureReason": "Error during password validation." } ] }
> ```
>
> Three things follow.
>
> **A successful row creates the managed object immediately.** The control arrived
> as `owner: device_MIGTEST-999` with no device having connected at all, so bulk
> registration creates the object, the device user and the external identity in one
> call. There is no deferred "created on first connect" step to wait for.
>
> **A migrated object is owned by the importing service user** —
> `owner: service_devicemigration`. That is the ownership gap step 8 exists to
> measure, and it is now a fact rather than a worry.
>
> **The existing-id row failed — but the reason is not established.** The message
> names password validation, while the *same password shape* succeeded on the
> control row in the same request, which is what makes it suspect. Step 6 exists to
> tell a misleading error from a real one, and nothing about Option A should be
> concluded before it is run.

Record what the tenant holds now:

```sh
c8y api GET "/devicecontrol/newDeviceRequests?pageSize=100" --session "$TGT_SESSION" | jq '{id, status}'
c8y api GET "/user/$TGT_TENANT/userByName/device_MIGTEST-001" --session "$TGT_SESSION" | jq '{userName, enabled}'
c8y api GET "/user/$TGT_TENANT/userByName/device_MIGTEST-999" --session "$TGT_SESSION" | jq '{userName, enabled}'
c8y api GET "/identity/externalIds/c8y_Serial/MIGTEST-001" --session "$TGT_SESSION" | jq '{mo: .managedObject.id}'
```

Two of these matter for step 6. If `device_MIGTEST-001` **already exists** — from an
earlier attempt against this tenant — then the failing row took a credential
*update* path rather than a creation path, and "password validation" may be a
truthful complaint about updating a password to one already in use. If it does not
exist, the message is about something else. Same for a lingering
`newDeviceRequests` entry in a non-terminal state.

**6. Is the failure about the password or about the external id?**

The error message cannot be taken at face value, so establish the cause by
elimination. Four cells, two already filled:

| | existing id (`MIGTEST-001`) | fresh id (`MIGTEST-998`) |
| --- | --- | --- |
| the password that failed | **FAILED** (observed) | **6a** — run this |
| a different password | **6b** — run this | succeeded (the control, effectively) |

**First, a free data point.** If you still have the rejected tab-separated file,
note *how* it was rejected. Parsed as CSV, a tab-separated line has no commas and
therefore exactly one field, which makes the whole line the external id and leaves
`CREDENTIALS` empty — and a row with no password would plausibly be reported as
`"Error during password validation."` If that is the message the TSV produced too,
then it is this endpoint's generic complaint about a row it could not use, and the
`MIGTEST-001` failure almost certainly has nothing to do with passwords either.
Suggestive rather than conclusive, but it costs nothing and it points 6a's result
in advance.

**6a is the decisive one**: register a brand-new external id using the *exact*
password that failed. If it succeeds, the password is exonerated and the collision
with the existing identity is the cause — the message is simply wrong about why.

```sh
printf 'ID,AUTH_TYPE,CREDENTIALS,NAME,TYPE,IDTYPE,ICCID,TENANT,PATH,com_cumulocity_model_Agent.active\n' > probe.csv
printf 'MIGTEST-998,BASIC,abcdEFGH123!,MIGTEST-998,c8y_MigrationTest,c8y_Serial,,,,true\n' >> probe.csv
awk -F',' '{print NR": "NF" fields"}' probe.csv    # expect 10 and 10

curl -sS -X POST "https://$TGT_HOST/devicecontrol/bulkNewDeviceRequests" \
    -H "Authorization: Basic $TGT_BASIC" -F "file=@probe.csv;type=text/csv" | jq .
```

**6b** completes the square: re-register `MIGTEST-001` with a password that shares
nothing with the first — different length, different symbol. Note the single
quotes; `$` in the password would otherwise be eaten by the shell.

```sh
printf 'ID,AUTH_TYPE,CREDENTIALS,NAME,TYPE,IDTYPE,ICCID,TENANT,PATH,com_cumulocity_model_Agent.active\n' > retry.csv
printf 'MIGTEST-001,BASIC,%s,MIGTEST-001,c8y_MigrationTest,c8y_Serial,,,,true\n' 'Qw7#kLm9Zx2$' >> retry.csv
awk -F',' '{print NR": "NF" fields"}' retry.csv    # expect 10 and 10

curl -sS -X POST "https://$TGT_HOST/devicecontrol/bulkNewDeviceRequests" \
    -H "Authorization: Basic $TGT_BASIC" -F "file=@retry.csv;type=text/csv" | jq .
```

> **Answered 2026-09-07. Both succeeded**, and between them they explain the
> original failure.
>
> **6a created `MIGTEST-998`** using the exact password that had been refused, so
> the password was never the problem — `"Error during password validation."` was
> returned for a cause that has nothing to do with passwords. Treat this endpoint's
> error text as unreliable, in tests and in the service's own error handling.
>
> **6b returned `CREDENTIAL_UPDATED`** for `MIGTEST-001`:
> `numberOfCreated: 0, numberOfSuccessful: 1`, the row in `credentialUpdatedList`.
> So a row for an id the target already knows takes an **update** path: new
> password, no new object — and, confirmed by the inventory afterwards, **no change
> to the existing object at all**, including its `owner: service_devicemigration`.
>
> The three objects afterwards are the whole result in one view: `MIGTEST-998` and
> `MIGTEST-999` owned by their device users because registration created them, and
> `MIGTEST-001` still owned by the importing service user because registration
> declined to touch it.
>
> **Consequence.** Bulk registration cannot give a device ownership of an object
> that already exists — so credentials must be issued **before** the import, not
> after, and the import must `PUT` onto the registered objects. That is the order
> [credentials.md](credentials.md#option-a--built)
> now describes, and it makes [A7](#a7--can-the-service-user-put-an-object-owned-by-a-device-user)
> the test that matters and [A6](#a6--can-a-managed-objects-owner-be-reassigned) a
> fallback.
>
> One loose end: the *first* attempt at `MIGTEST-001` failed where the second
> succeeded. The likeliest reading is that the first was a creation attempt that hit
> the existing identity, reported it badly, and left a device user behind for the
> second to update — but that is a story, not a finding. [A4](#a4--what-happens-when-the-same-device-is-registered-twice)
> closes it.

**7. If a row for an existing id ever succeeds: does it adopt or duplicate?**

Only reachable if 6b succeeded. The question is whether the platform reused the
migrated object or made a second one:

```sh
c8y api GET "/inventory/managedObjects?text=MIGTEST&pageSize=100" --session "$TGT_SESSION" \
    | jq '{id, name, type, owner}'
c8y api GET "/identity/externalIds/c8y_Serial/MIGTEST-001" --session "$TGT_SESSION" | jq '{mo: .managedObject.id}'
```

| What you see | Meaning | Consequence |
| --- | --- | --- |
| Still one object, identity still resolving to the migrated id, `owner` now `device_MIGTEST-001` | It adopts, and transfers ownership | Option A as written, and A6 is unnecessary |
| Still one object, identity unchanged, `owner` stays `service_devicemigration` | It adopts without transferring ownership | Worse than duplicating, because it looks like success. Step 8 measures what it costs |
| A second managed object, identity moved to it | It duplicates | The migrated copy is orphaned. Option A rebuilds around direct user creation |

**8. Can the device write its own twin?** Ask however the user was created, and
whether or not step 7 was reachable — this is about ownership, not about
registration. [remote-targets.md](remote-targets.md#verified-on-a-live-tenant)
established that a device identity's writes succeed on `_READ` roles only because
`ROLE_DEVICE` plus **source-ownership** carries them, and a migrated object is
owned by `service_devicemigration`.

```sh
curl -sS -w '\n%{http_code}\n' -X PUT \
    -u "$TGT_TENANT/device_MIGTEST-001:$PW1" \
    -H 'Content-Type: application/json' -d '{"c8y_TestFragment":{"v":1}}' \
    "https://$TGT_HOST/inventory/managedObjects/<targetMoId>"
```

A `403` means the fleet arrives bound to the right objects and unable to update
them — a failure mode that passes every earlier check and only shows up in the
field. If that is the result, provisioning has to hand each migrated object to its
device user, which makes [A6](#a6--can-a-managed-objects-owner-be-reassigned) part
of Option A rather than a curiosity.

### A7 — Can the service user PUT an object owned by a device user?

> **Answered 2026-09-07: yes, and `owner` survives.** A `PUT` from a user who is
> not the owner updated `MIGTEST-998`'s `name` and added a fragment, and the object
> came back still owned by `device_MIGTEST-998`. Register-first's step 3 works.
>
> Two notes for the record. It was run as a tenant administrator, so the
> **permission** half is still open — whether the manifest's roles alone suffice
> folds into [A2](#a2--what-permissions-does-bulk-registration-need), which creates
> the right user for it anyway. And the device's own write was not re-tested
> afterwards; ownership is unchanged, which is the thing `ROLE_DEVICE` plus
> source-ownership depends on, so there is no mechanism by which it would have
> lapsed.

**Why it mattered.** This is the whole of Option A's step 3. Register-first leaves
every object owned by `device_<externalId>`, and the import then has to update an
object it does not own, as `service_devicemigration`. If that is refused, the order
inversion collapses and the option goes back to importing first — with the
ownership problem [A6](#a6--can-a-managed-objects-owner-be-reassigned) describes.

`MIGTEST-998` and `MIGTEST-999` are already in the right state to test against:
registered, device-owned, and carrying nothing else.

```sh
# as the service user (or the manifest-roles user from A2), update a device-owned object
c8y api PUT "/inventory/managedObjects/2410119277" --session migtest-svc \
    --data '{"c8y_TestFragment":{"v":1},"name":"MIGTEST-998-renamed"}'

# then confirm the owner survived it
c8y api GET "/inventory/managedObjects/2410119277" --session "$TGT_SESSION" \
    | jq '{id, name, owner, c8y_TestFragment}'
```

Three things to record: the status code; whether `owner` is still
`device_MIGTEST-998` afterwards; and whether the device can still write its own
twin once the service has updated it (repeat [A1 step 8](#a1--does-a-bulk-row-with-an-existing-external-id-bind-or-duplicate)
as the device user).

| Outcome | Consequence |
| --- | --- |
| `200`, `owner` unchanged, device can still write | Register-first works. `ROLE_INVENTORY_ADMIN` is already in the manifest, so step 3 costs no new permission |
| `200` but `owner` silently changes to the service user | The strip list is not enough — the `PUT` is taking ownership. Registration and import would have to be reconciled, or ownership re-set after each update |
| `403` | Register-first collapses. Fall back to import-first plus [A6](#a6--can-a-managed-objects-owner-be-reassigned), and note that the option then needs a larger role after all |

Worth running the same `PUT` as the **manifest-roles-only** user from
[A2](#a2--what-permissions-does-bulk-registration-need), not just as a tenant
administrator — an administrator will succeed whether or not the service would.

### A8 — Can bulk registration stamp the run marker?

> **Answered 2026-09-08: yes.** The registration file maps custom columns onto
> fragments, so registration can stamp the run marker itself and the marker gap
> closes without a second call per device. Three limitations come with it, and all
> three shape how the marker is written:
>
> - **Values become strings.** A header `com_cumulocity_model_Agent.active` with
>   `true` in the data row produces `"com_cumulocity_model_Agent": {"active": "true"}`
>   — the *string* `"true"`, not a boolean.
> - **Braces are literal.** `{}` in a data row arrives as the two-character string
>   `"{}"`. An empty-object fragment cannot be written this way; a fragment becomes
>   an object only through a dotted header.
> - **No arrays.**
>
> **So the marker is written as a dotted header**, not as a bare column with `{}` in
> the row. A header `c8y_DeviceMigration_<runid>.phase` with `registered` in the data
> row produces `"c8y_DeviceMigration_<runid>": {"phase": "registered"}` — a real
> object, findable by `$filter=has(c8y_DeviceMigration_<runid>)`
> ([internal/importer/resume.go:27](../../internal/importer/resume.go#L27)), which is
> value-agnostic and does not care that the import writes `{}` in the same place
> later.
>
> **And the `phase` value turns out to be the answer to a second problem.** The
> mapping records a target id as soon as one exists, which under register-first is
> at registration — so it can no longer double as "the import finished this object",
> and `id_mapping.json` cannot grow a field to say so, because its format is
> compared byte for byte against the shell implementation's
> ([internal/importer/mapping.go:11](../../internal/importer/mapping.go#L11)). The
> marker's own value can carry it instead: `registered` from step 2, something else
> from step 3, and an interrupted run is then legible from the tenant alone.

**Why it mattered.** Register-first creates the objects one phase before the import
can mark them, so a run that dies in between leaves objects that neither
`--resume` nor `revert` can find — both locate a run's work through
`$filter=has(<fragment>)`. Closing that gap with a second `PUT` per device is
possible but wasteful; closing it inside the registration call costs nothing.

Two things suggest it may work. The registration file already carries
**`com_cumulocity_model_Agent.active`** — a *fragment path* used as a column name,
so the endpoint already maps columns onto fragments. And the marker does not care
what it contains: `MarkerQuery` is `$filter=has(...)`
([internal/importer/resume.go:27](../../internal/importer/resume.go#L27)), and a
run id is letters, digits and underscores only, chosen precisely because it becomes
a fragment name ([internal/importer/runid.go](../../internal/importer/runid.go)).

```sh
printf 'ID,AUTH_TYPE,CREDENTIALS,NAME,TYPE,IDTYPE,ICCID,TENANT,PATH,com_cumulocity_model_Agent.active,c8y_DeviceMigration_TESTRUN_a1b2c3\n' > marker.csv
printf 'MIGTEST-997,BASIC,abcdEFGH123!,MIGTEST-997,c8y_MigrationTest,c8y_Serial,,,,true,true\n' >> marker.csv
awk -F',' '{print NR": "NF" fields"}' marker.csv    # expect 11 and 11

curl -sS -X POST "https://$TGT_HOST/devicecontrol/bulkNewDeviceRequests" \
    -H "Authorization: Basic $TGT_BASIC" -F "file=@marker.csv;type=text/csv" | jq .
```

Then the two questions — did the fragment land, and is it findable the way a resume
would look for it:

```sh
c8y api GET "/inventory/managedObjects?text=MIGTEST-997" --session "$TGT_SESSION" | jq 'keys'
c8y api GET '/inventory/managedObjects?$filter=has(c8y_DeviceMigration_TESTRUN_a1b2c3)' \
    --session "$TGT_SESSION" | jq '{id, name}'
```

Single quotes on that second URL: `$filter` would otherwise be eaten by the shell.

| Outcome | Consequence |
| --- | --- |
| Column accepted, fragment present, `has()` query finds it | The marker gap closes for free. Registration stamps the run, and `--resume` and `revert` see the objects from the moment they exist. Note the fragment's **value type** — the import writes `{}` and the CSV can only write a scalar, so record which one lands and let step 3's `PUT` converge them |
| Column accepted but silently dropped | Registration needs a follow-up `PUT` per device to stamp the marker, or the gap needs a documented recovery procedure |
| Whole file rejected | Same consequence, and it also tells you the column set is fixed — which rules out anything else clever with the registration file, `PATH`-based grouping included |

### A9 — What does registration add that the source object does not have?

**Why it matters.** Cumulocity's inventory `PUT` **merges**; it does not replace.
So every fragment registration puts on an object survives step 3, and the migrated
twin ends up as the *union* of what registration created and what the source had.
Today's importer `POST`s a clean object and the result is exactly the source's
content; register-first changes that, and the difference has to be deliberate
rather than discovered by a customer comparing two tenants.

The one that would actually be wrong is the agent fragment. `SetBasicAuth` in the
library forces `IsAgent = true`, and the CSV's last column is
`com_cumulocity_model_Agent.active` — so a device that was never an agent in the
source can arrive in the target marked as one, and the `PUT` will not remove it.

[A8](#a8--can-bulk-registration-stamp-the-run-marker) sharpens this: registration
writes **strings**, so `active` arrives as `"true"` rather than `true`. For a device
whose source copy has the fragment that difference is temporary — step 3's `PUT`
overwrites it with the source's boolean. For a device whose source copy does not
have it, there is nothing to overwrite, and `"false"` is not the same thing as
absent. **So the recommendation is to omit the column entirely** and let step 3
supply the truth from the source object. What needs confirming is only that
registration accepts a file without it.

```sh
# what does a freshly registered object look like, before any import touches it?
c8y api GET "/inventory/managedObjects/2410119277" --session "$TGT_SESSION" | jq 'keys'

# and what did the source's copy have?
jq 'keys' exported_device_<sourceId>.json
```

Then confirm a file with no agent column at all is accepted — this is the shape
Option A would actually send, and it doubles as the smallest possible check that
the marker column from A8 works:

```sh
printf 'ID,AUTH_TYPE,CREDENTIALS,NAME,TYPE,IDTYPE,c8y_DeviceMigration_TESTRUN_a1b2c3.phase\n' > noagent.csv
printf 'MIGTEST-996,BASIC,abcdEFGH123!,MIGTEST-996,c8y_MigrationTest,c8y_Serial,registered\n' >> noagent.csv
curl -sS -X POST "https://$TGT_HOST/devicecontrol/bulkNewDeviceRequests" \
    -H "Authorization: Basic $TGT_BASIC" -F "file=@noagent.csv;type=text/csv" | jq .

c8y api GET "/inventory/managedObjects?text=MIGTEST-996" --session "$TGT_SESSION" \
    | jq '{keys: keys, marker: .c8y_DeviceMigration_TESTRUN_a1b2c3}'
```

Note it also drops `ICCID`, `TENANT`, `PATH` and the agent column together — worth
knowing whether the endpoint requires the full column set or only the ones it is
given, because that decides whether the writer emits a fixed header or a minimal
one.

Record the full key list for both, and the difference. Then step 3 has three
choices per surplus fragment, and the right one differs by fragment:

| Surplus fragment | Likely handling |
| --- | --- |
| `c8y_IsDevice` | Keep. The object is a device, and the source's copy of a device has it anyway |
| `com_cumulocity_model_Agent` on a non-agent | Clear it in step 3 — `jsonraw.Delete` already exists for exactly this shape of problem — or set the column from whether the source object has the fragment, which is cheaper and needs no delete |
| anything else registration adds | Decide per fragment and write the list down; a surprise fragment in a customer's target tenant is a support call |

### A6 — Can a managed object's owner be reassigned?

**Only needed if [A7](#a7--can-the-service-user-put-an-object-owned-by-a-device-user)
fails.** It did not, so this is a dormant fallback — kept in case the permission
half of A7 comes back refused in [A2](#a2--what-permissions-does-bulk-registration-need)
and Option A has to go back to importing before registering.

**Why it would matter.** Import-first leaves the migrated objects owned by
`service_devicemigration` and the devices owning nothing, and registration will not
change that — [A1 step 6b](#a1--does-a-bulk-row-with-an-existing-external-id-bind-or-duplicate)
showed the update path leaves the object untouched. So provisioning would have to
reassign `owner` itself.

`owner` is one of the server-managed properties the import already strips
([README](../../README.md#what-gets-migrated)), so whether it is writable at all is
an open question rather than a detail.

```sh
c8y api PUT "/inventory/managedObjects/<targetMoId>" --session "$TGT_SESSION" \
    --data '{"owner":"device_MIGTEST-001"}'
c8y api GET "/inventory/managedObjects/<targetMoId>" --session "$TGT_SESSION" | jq '{id, owner}'
```

Then repeat step 8's `PUT` as the device user and see whether the `403` becomes a
`200`.

| Outcome | Consequence |
| --- | --- |
| `owner` accepted and the device can then write | Provisioning gains one call per device. Note which role it needed — `ROLE_INVENTORY_ADMIN` is already in the manifest, so this may cost nothing |
| `owner` silently ignored | Look for a dedicated endpoint or a device-permission group as the alternative; if there is none, say so in the concept |
| `owner` rejected | Option A hands out credentials that can read but not write. That has to be stated in the concept and in the UI, not discovered by a customer |

### A2 — What permissions does bulk registration need?

> **Answered 2026-09-08.** Bulk registration needs
> **`ROLE_DEVICE_CONTROL_ADMIN`** — taken as known rather than tested. The step-3
> `PUT` is covered by `ROLE_INVENTORY_ADMIN`, already in the manifest.
>
> **User deletion needs no user-management role at all.** `DELETE` on a managed
> object takes `withDeviceUser`, which removes the associated device user with the
> device, so the whole `DELETE /user/{tenant}/users/…` question is moot — along with
> the manifest role it would have cost and the contentious grant that came with it.
>
> So the manifest diff for Option A is one line: `ROLE_DEVICE_CONTROL_ADMIN`.

**Why it matters.** The manifest's `requiredRoles`
([docker/cumulocity.json](../../docker/cumulocity.json)) holds inventory, identity,
measurement, event, alarm, option and tenant-management roles — and **no**
device-control or user-management role. So this almost certainly is a manifest
change, and the question is only how large a one.

Simulate the service user rather than waiting for a deployment: create a user in
the target holding exactly the manifest's `requiredRoles` and nothing else, then
add candidates one at a time until the bulk POST succeeds.

```sh
c8y api POST "/user/$TGT_TENANT/users" --session "$TGT_SESSION" \
    --data '{"userName":"migtest-svc","password":"<pw>","enabled":true}'
# grant exactly the manifest roles, then retry A1 step 3 as this user
```

Candidates to try, in this order: `ROLE_DEVICE_CONTROL_ADMIN`,
`ROLE_USER_MANAGEMENT_ADMIN`, `ROLE_TENANT_MANAGEMENT_ADMIN`. Record the **minimal
set** that works and the exact status and body of each refusal.

Then answer the question behind the question: is that set something a subscribed
tenant's administrator will approve? `ROLE_USER_MANAGEMENT_ADMIN` on a tenant means
the service can create any user, which is a materially bigger grant than the
migration roles it holds today. If the answer is uncomfortable, Option A can still
ship as a CLI-only feature, where the operator's own credentials do the work and no
manifest changes at all.

Register-first made *creation* cheaper — the platform creates the users, so
`POST /user/{tenant}/users` is not involved — but the revert decision of 2026-09-08
brings user management back for a different reason. There are now **three** things
to size, and they may need different roles:

If either of the two remaining calls is ever worth re-running as the
manifest-roles-only user rather than as an administrator, these are they:

```sh
# 1. bulk registration
curl -sS -X POST "https://$TGT_HOST/devicecontrol/bulkNewDeviceRequests" \
    -H "Authorization: Basic $SVC_BASIC" -F "file=@probe.csv;type=text/csv" | jq .

# 2. the step-3 PUT onto a device-owned object, and the revert that follows it
c8y api PUT "/inventory/managedObjects/2410119277" --session migtest-svc \
    --data '{"c8y_TestFragment":{"v":2}}'
c8y api DELETE "/inventory/managedObjects/2410119277?withDeviceUser=true" --session migtest-svc
c8y api GET "/user/$TGT_TENANT/userByName/device_MIGTEST-998" --session "$TGT_SESSION"  # expect 404
```

That last pair is worth running once whatever the roles turn out to be, because it
is the only thing that shows `withDeviceUser` actually removing the user rather
than being accepted and ignored — a revert that silently leaves working
credentials behind is exactly the failure the flag is there to prevent.

**Keep the `migtest-svc` user.** [A7](#a7--can-the-service-user-put-an-object-owned-by-a-device-user)
needs exactly this "manifest roles and nothing more" identity, and a tenant
administrator would pass that test whether or not the service would.

### A3 — Does the tenant accept an administrator-chosen password?

> **Answered 2026-09-08: yes, but not any password.** An administrator-chosen
> password is accepted, and rotation replaces the old one
> ([A4](#a4--what-happens-when-the-same-device-is-registered-twice)). But a
> **15-character alphanumeric password was refused** by the tenant's password
> strength policy, which wants a symbol.
>
> Found the way these things usually are: by handing somebody a generated
> credentials file and watching the registration fail. The generator now emits two
> symbols from a set chosen for where the password has to survive rather than for
> strength — `,` `"` `'` `\` and a backtick are excluded, and the reasons are in
> [internal/registration/password.go](../../internal/registration/password.go).
>
> The trap worth remembering: go-c8y's default symbol set **contains a comma**, so
> "just turn symbols on" would put a field separator into roughly one password in
> eight, in a comma-separated file whose parse failures this endpoint reports as
> `"Error during password validation."` — the same message a real policy refusal
> gives. That is two indistinguishable failures for one message, which is why the
> alphabet is pinned by a test rather than by a comment.

**Why it mattered.** A credentials file nobody can use is worse than no file.
Tenants can enforce password strength, TFA, and a reset on first login.

```sh
c8y api GET "/tenant/loginOptions" --session "$TGT_SESSION" | jq .
c8y api GET "/tenant/currentTenant" --session "$TGT_SESSION" | jq '.customProperties'
curl -sS -o /dev/null -w '%{http_code}\n' \
    -u "$TGT_TENANT/device_MIGTEST-001:$PW1" \
    "https://$TGT_HOST/inventory/managedObjects?pageSize=1"
```

A `200` settles it. Anything else — record the status, the body, and the tenant's
password policy, because that is what Option A's documentation will have to warn
about.

### A4 — What happens when the same device is registered twice?

Mostly answered by [A1 step 6b](#a1--does-a-bulk-row-with-an-existing-external-id-bind-or-duplicate):
a second registration returns `CREDENTIAL_UPDATED`, so rotation is native and the
concept's *"rotation invalidates any file handed out earlier"* costs nothing to
build. Two things remain.

**Does the new password actually replace the old one?** `CREDENTIAL_UPDATED` says
the platform thinks so; only an authentication attempt proves it.

```sh
# the rotated-in password should work
curl -sS -o /dev/null -w 'new: %{http_code}\n' \
    -u "$TGT_TENANT/device_MIGTEST-001:Qw7#kLm9Zx2$" \
    "https://$TGT_HOST/inventory/managedObjects?pageSize=1"
# the original should not
curl -sS -o /dev/null -w 'old: %{http_code}\n' \
    -u "$TGT_TENANT/device_MIGTEST-001:abcdEFGH123!" \
    "https://$TGT_HOST/inventory/managedObjects?pageSize=1"
```

Both working is a platform bug and a hole in the rotation story — report it rather
than designing around it.

**Is the first-attempt failure reproducible?** This decides whether the importer
needs retry logic. Register `MIGTEST-001` a third time with the password that
originally failed:

```sh
printf 'ID,AUTH_TYPE,CREDENTIALS,NAME,TYPE,IDTYPE,ICCID,TENANT,PATH,com_cumulocity_model_Agent.active\n' > third.csv
printf 'MIGTEST-001,BASIC,abcdEFGH123!,MIGTEST-001,c8y_MigrationTest,c8y_Serial,,,,true\n' >> third.csv
curl -sS -X POST "https://$TGT_HOST/devicecontrol/bulkNewDeviceRequests" \
    -H "Authorization: Basic $TGT_BASIC" -F "file=@third.csv;type=text/csv" | jq .
```

| Outcome | Consequence |
| --- | --- |
| `CREDENTIAL_UPDATED` | The original failure was the create-path conflict, reported badly, and it is not a hazard once registration comes first. No retry logic needed |
| Fails again | Something is genuinely refused on repeat registration with a previously-used password. Under register-first every re-run takes this path, so the importer needs a retry or a fresh password per attempt — and the concept's rotation section needs the caveat |

### A5 — Does the device user work end to end?

The connect itself now lives in [A1 steps 6–8](#a1--does-a-bulk-row-with-an-existing-external-id-bind-or-duplicate),
because the binding question cannot be answered without it. What remains here is
whether the *rest* of the device's working life survives the adoption — the parts a
single measurement does not exercise.

```sh
# does it see the operations queued against its own twin?
c8y api GET "/devicecontrol/operations?deviceId=<targetMoId>&pageSize=5" --session "$TGT_SESSION"
c8y api GET "/inventory/managedObjects?owner=device_MIGTEST-001" --session "$TGT_SESSION" \
    | jq '[.managedObjects[] | {id, name}]'

# and can it still reach its children, if the migrated hierarchy has any?
c8y api GET "/inventory/managedObjects/<targetMoId>/childDevices" --session "$TGT_SESSION" \
    | jq '[.references[] | .managedObject | {id, name}]'
```

Record whether the `owner=device_MIGTEST-001` query returns the migrated object —
that is the same question as A1 step 8, from the platform's side rather than the
device's — and whether a migrated parent's children are reachable by the adopted
device user. A gateway that arrives owning its twin but not its children is a
half-migration, and it is better to know now than to design around it later.

## Option C — move the trust anchor

### C2 — Can the same certificate exist in both tenants?

**Why it matters.** It is the difference between a cutover measured in seconds and
one measured in minutes — and if the answer is "no", between a cutover that can be
rolled back and one that cannot.

**1. Register the throwaway CA in the source**, enabled, auto-registration on:

```sh
c8y api POST "/tenant/tenants/$SRC_TENANT/trusted-certificates" --session "$SRC_SESSION" \
    --data "{\"name\":\"migtest-ca\",\"certInPemFormat\":\"$CA_BODY\",\"status\":\"ENABLED\",\"autoRegistrationEnabled\":true}"
```

Record the `fingerprint` it returns; every later call is keyed by it.

**2. Try to pre-stage the same certificate in the target, disabled:**

```sh
c8y api POST "/tenant/tenants/$TGT_TENANT/trusted-certificates" --session "$TGT_SESSION" \
    --data "{\"name\":\"migtest-ca\",\"certInPemFormat\":\"$CA_BODY\",\"status\":\"DISABLED\",\"autoRegistrationEnabled\":true}"
```

**3. If that succeeded, try to flip it** while the source is still enabled:

```sh
c8y api PUT "/tenant/tenants/$TGT_TENANT/trusted-certificates/$FP" --session "$TGT_SESSION" \
    --data '{"status":"ENABLED"}'
```

**4. Then the other ordering**, to measure the alternative: disable in the source,
enable in the target, and time the gap.

Record the status code and error body of every call, and which orderings the
platform permitted.

| Outcome | Cutover shape |
| --- | --- |
| Create-disabled allowed, enable-while-source-enabled allowed | Pre-stage and flip. Window is one API call, and the source stays available as a fallback until you disable it |
| Create-disabled allowed, enable refused while source enabled | Pre-stage, disable source, enable target. Window is two calls — and between them nothing works |
| Create refused outright | Delete from the source, then create in the target. Two calls plus propagation, no fallback, and no way back if the target create fails. Option C needs a documented recovery procedure for exactly that |

Note the constraint from the device-side agent while you are here: whichever
ordering the platform allows, **enable the target before any device is told to
move**, or the agent's bootstrap-credential probe will wave the fleet through into
a failed connection and a mass rollback.

### C3 — Does auto-registration bind to the migrated object?

**Why it matters.** If auto-registration creates a fresh object instead of binding
to the migrated copy, the fleet arrives in the target detached from everything the
migration carried over — the data, the hierarchy, the fragments. That is Option C
failing at its whole purpose, not a rough edge.

**1. Pre-state.** The target has the CA enabled with `autoRegistrationEnabled`, and
the migrated copy of `MIGTEST-001` carries external identity
`c8y_Serial:MIGTEST-001`. **Record the identity's type explicitly** — if
auto-registration looks up a different type than the migration wrote, the binding
cannot happen, and that is a much easier problem to fix than it is to diagnose
later.

```sh
c8y api GET "/identity/externalIds/c8y_Serial/MIGTEST-001" --session "$TGT_SESSION"
c8y api GET "/inventory/managedObjects?text=MIGTEST-001&pageSize=100" --session "$TGT_SESSION" \
    | jq '[.managedObjects[] | {id, name, owner}]'
```

**2. Connect. Do both, in this order.**

The REST probe first — cheap, no MQTT client, and the flow
[remote-targets.md](remote-targets.md#verified-on-a-live-tenant) already proved
triggers auto-registration:

```sh
curl -sS --cert dev.pem --key dev.key \
    -X POST "https://$TGT_HOST:8443/devicecontrol/deviceAccessToken" | jq .
```

Then MQTT, because that is what a thin-edge device actually does and it is the
authoritative test:

```sh
mosquitto_pub -h "$TGT_HOST" -p 8883 --capath /etc/ssl/certs \
    --cert dev.pem --key dev.key -i MIGTEST-001 \
    -t 's/us' -m '100,MIGTEST-001,c8y_MigrationTest' -d
```

**3. Post-state.** Re-run step 1's queries, and:

```sh
c8y api GET "/inventory/managedObjects/<targetMoId>" --session "$TGT_SESSION" | jq '{id, owner, name}'
c8y api GET "/user/$TGT_TENANT/userByName/device_MIGTEST-001" --session "$TGT_SESSION" | jq '{userName, id}'
c8y api GET "/measurement/measurements?source=<targetMoId>&pageSize=5" --session "$TGT_SESSION"
```

| What you see | Consequence |
| --- | --- |
| Identity unchanged, no new object, `owner` is `device_MIGTEST-001`, the measurement lands on the migrated object | Option C works as designed |
| A new managed object, identity moved to it, migrated copy left orphaned | The fleet arrives detached. Option C needs a reconciliation step after cutover — or, if reconciliation cannot be automated, the option is not viable as written |
| Connection refused | Do not conclude anything about binding yet; go to [C4](#c4--does-the-target-demand-proof-of-possession) first, then re-run |

If a new object appeared, record **what it was named and what identity it got** —
that determines whether a post-cutover merge is even possible.

### C4 — Does the target demand proof of possession?

**Why it matters.** This one can quietly remove Option C's best property. The
library exposes a proof-of-possession flow whose `ProofEndToEnd` signs a
verification code with the CA's **private key**. Option C reads
`certInPemFormat` — a public certificate — from the source tenant. Neither the
service nor the source tenant holds the customer CA's private key. If the target
insists on proof before honouring auto-registration, Option C cannot move a CA on
the strength of its public certificate alone.

**1. Read back what the target thinks of the certificate registered in C2:**

```sh
c8y api GET "/tenant/tenants/$TGT_TENANT/trusted-certificates/$FP" --session "$TGT_SESSION" \
    | jq '{status, autoRegistrationEnabled, proofOfPossessionValid,
           proofOfPossessionVerificationCodeUsableUntil, tenantCertificateAuthority}'
```

**2. Note whether `autoRegistrationEnabled: true` was even accepted** while
`proofOfPossessionValid` is false. A platform that silently drops the flag is
telling you the answer.

**3. If C3's connection was refused, run the proof** — with the CA key you still
have, which a real migration would not:

```sh
CODE=$(c8y api POST "/tenant/tenants/$TGT_TENANT/trusted-certificates-pop/$FP/verification-code" \
    --session "$TGT_SESSION" | jq -r '.proofOfPossessionUnsignedVerificationCode')
SIG=$(printf '%s' "$CODE" | openssl dgst -sha256 -sign ca.key | base64 -w0)
c8y api POST "/tenant/tenants/$TGT_TENANT/trusted-certificates-pop/$FP/pop" \
    --session "$TGT_SESSION" \
    --data "{\"proofOfPossessionSignedVerificationCode\":\"$SIG\"}"
```

Then re-run C3 step 2.

| Outcome | Consequence for Option C |
| --- | --- |
| Auto-registration works with `proofOfPossessionValid: false` | The option holds: only a public PEM crosses, exactly as designed |
| Proof required | The copyable material stops being public. Option C becomes *"the tool prepares the registration; a target administrator completes it with the CA key"* — which is honest, still useful, and a different feature from the one in the concept. Say so in the doc rather than shipping something that fails on first use |

### C5 — Which trusted certificates can be moved at all?

Inventory the source, so the tool knows what to offer and what to refuse:

```sh
c8y api GET "/tenant/tenants/$SRC_TENANT/trusted-certificates?pageSize=100" --session "$SRC_SESSION" \
    | jq '[.certificates[] | {name, fingerprint, status, autoRegistrationEnabled,
                              tenantCertificateAuthority, subject, issuer, notAfter}]'
```

Then confirm the exclusion the concept already states: take an entry with
`tenantCertificateAuthority: true` and try to create it in the target. Record what
happens — a refusal, or a duplicate that does nothing. Either way it backs
*"deliberately not in the first cut"* with evidence instead of an assertion, and
tells the UI which certificates to grey out.

Also record `notAfter` on each: a CA close to expiry is a cutover that will need
redoing, and the tool should say so before offering one.

### C6 — What permissions do trusted certificates need?

**Why it matters.** Reading a source tenant's trusted certificates and writing them
to a target is tenant-administration, not device management, and the manifest holds
nothing of the sort. This could be the finding that decides Option C's *shape*.

As the `migtest-svc` user from [A2](#a2--what-permissions-does-bulk-registration-need),
holding only the manifest's roles:

```sh
c8y api GET "/tenant/tenants/$SRC_TENANT/trusted-certificates" --session migtest-svc
c8y api POST "/tenant/tenants/$TGT_TENANT/trusted-certificates" --session migtest-svc --data '...'
```

Record both status codes, then add roles until each works and note the minimum.
`ROLE_TENANT_ADMIN` is the likely answer, and it is a very large grant to ask of
every subscribed tenant — the service could then reconfigure the tenant, not just
migrate devices.

If that is where it lands, weigh two alternatives before adding it to the manifest:
the CLI does the certificate work under the operator's own credentials, or the
service *prepares* the certificate and a target administrator applies it in
Cumulocity's own Administration screen — which is precisely the pattern
[remote-targets.md](remote-targets.md) chose for Option 4, and for the same reason.

### C7 — Can a device be attributed to its CA?

**Why it matters.** The concept asks the tool to *"count the devices in the source
that the run did not cover, and say so before offering a cutover"*. That requires
mapping a device to the CA that signed its certificate. Nothing obvious in the
inventory model records it.

For a device that has connected via a CA-signed certificate:

```sh
c8y api GET "/inventory/managedObjects/<moId>" --session "$SRC_SESSION" | jq 'keys'
c8y api GET "/user/$SRC_TENANT/userByName/device_MIGTEST-001" --session "$SRC_SESSION" | jq .
c8y api GET "/audit/auditRecords?source=<moId>&pageSize=20" --session "$SRC_SESSION" \
    | jq '[.auditRecords[] | {type, activity, text, time}]'
```

Look for anything naming a fingerprint, an issuer or a certificate. Check the
trusted-certificate resource itself for a device count or a back-reference.

| Outcome | Consequence |
| --- | --- |
| Device → CA is derivable | The coverage warning can be exact: *"this CA also signs N devices the run did not cover"* |
| Not derivable | The warning degrades to *"this CA may serve devices outside this migration; we cannot tell"* plus a count of all certificate-authenticated devices in the source. Less useful, still honest — and it needs saying in the concept rather than being discovered during the build |

### C8 — Does the device-side agent complete the move?

The end-to-end for Option C, and the only test here that needs real hardware: a
thin-edge device with the `tedge-environment-migration` package, its certificate
chaining to the throwaway CA, and its `device.id` equal to a migrated external id.

**1. The happy path.** CA enabled in the target (and still enabled in the source),
then:

```sh
sudo tedge-environment-migration.sh --tenant "$TGT_HOST"
```

Expect exit 0 and a `migrationSuccess` event. Confirm the device appears in the
target **bound to its migrated object** — this is C3 again, through the real path
rather than a hand-made handshake — and confirm what happened to its twin in the
source.

**2. The ordering hazard**, which is worth provoking deliberately once so the
runbook can describe it: disable the CA in the source, then point a device at a
target where the CA is *not yet* enabled. The expectation from reading the script
is that the pre-flight probe passes (it authenticates with bootstrap credentials,
not the certificate), the reconnect fails, the rollback fails too, and the run
exits 11 with *"Device may not be connected to either tenant"*.

If that is what happens, the ordering rule in
[credentials.md](credentials.md#the-device-side-half) is confirmed and can be stated
as a hard requirement. Record the exit code and the `envMigrationStatus` twin.

## Option B — self-migration

### B1 — Can a device user call a microservice at all?

**Why it matters.** Option B has the device authenticate to our endpoint with its
source-tenant credentials. Every request to a microservice passes the platform's
API gateway first. If the gateway will not authenticate a device user against a
microservice route — or requires a role a device user cannot hold — Option B is
impossible as written, whatever we build, and no amount of authorizer code changes
that. **Run this before any Option B design work.**

With the microservice deployed and the source tenant subscribed:

```sh
curl -sS -o /dev/null -w '%{http_code}\n' \
    -u "$SRC_TENANT/device_MIGTEST-001:$PW1" \
    "https://$SRC_HOST/service/devicemigration/health"

curl -sS -w '\n%{http_code}\n' \
    -u "$SRC_TENANT/device_MIGTEST-001:$PW1" \
    "https://$SRC_HOST/service/devicemigration/jobs"
```

`/health` is unauthenticated in our service, so a failure there is the gateway, not
us — which is exactly the distinction this test is for. Record both, plus the body
of any refusal.

| Outcome | Consequence |
| --- | --- |
| Both reach the service | Option B is feasible; the work is the deliberate authorizer exception, its tests, rate limiting and audit trail, as the concept says |
| `/health` reached, `/jobs` refused by the gateway | Record what it demands. If it is a role, can a device user hold it? |
| Gateway refuses device users outright | Option B is dead in this form. The alternatives are all much larger — an endpoint outside the platform, or a different authentication model — and the concept should say so instead of leaving B as a tidy third option |

### B2 — Can the service resolve the device a caller owns?

Option B's authorization chain runs from the authenticated caller to the object it
owns. Establish the exact query that yields one unambiguous device:

```sh
curl -sS -u "$SRC_TENANT/device_MIGTEST-001:$PW1" \
    "https://$SRC_HOST/inventory/managedObjects?owner=device_MIGTEST-001&pageSize=10" | jq '[.managedObjects[] | {id, name, owner}]'
curl -sS -u "$SRC_TENANT/device_MIGTEST-001:$PW1" \
    "https://$SRC_HOST/user/currentUser" | jq .
```

Record which call the service should actually use, and whether a device user can
read its own managed object at all without extra roles.

### B3 — What does a gateway owning children look like?

The concept's step 2 — *"the device it owns in that tenant is found"* — assumes one
device per user. A gateway that registered children owns several objects, and that
is common in exactly the fleets Option B targets.

Register a device user, create two child devices owned by it, and re-run B2's
query. If it returns several objects, Option B needs a documented rule for which
one it means — and probably an explicit answer for gateways, which the concept's
[scope boundaries](credentials.md#scope-boundaries-worth-stating) currently resolve
by declaring the gateway to be the device. Confirm that declaration survives
contact with the query.

## Recording the results

For each test: the date, the instance, the exact call, the status code, and the
part of the response that decided it. Then write the consequences into
[credentials.md](credentials.md) — moving each settled row out of *To confirm* and
into *Settled*, and rewriting whatever prose the result contradicts.

If a result kills an option, say so in the concept in the same breath as the
evidence. The pattern to copy is
[remote-targets.md's rejected section](remote-targets.md#rejected), which keeps a
wrong assumption on the record as a reminder of how easily one becomes a fact.
