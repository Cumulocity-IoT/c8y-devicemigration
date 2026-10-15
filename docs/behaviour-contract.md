# Behaviour contract

What the commands do, down to the bytes they write and the lines they print —
the behaviour that is relied on and must not change by accident. Every command
and flag is in [docs/cli.md](cli.md).

Where this document and the code disagree, **the code wins** — see
[Known quirks](#known-quirks) for the places where the code deliberately does
something its help text does not suggest.

## Shared behaviour

| Thing | Contract |
| --- | --- |
| output | progress to **stderr**, nothing to stdout |
| exit code | 0 on success, 1 for a usage error or a refusal |
| names | tabs and newlines become spaces; an empty or all-spaces name is reported as the literal `<unnamed>` |
| counts | singular iff the count is 1, so every count reads as prose |

## Session resolution

`--session` is optional on `export`, `import` and `revert`. The order is:

1. an explicit `--session` value — wins outright, ambient `C8Y_*` ignored
2. the ambient `C8Y_*` variables, i.e. an active session
   (`C8Y_BASEURL` / `C8Y_URL` / `C8Y_HOST`, plus `C8Y_TENANT`,
   `C8Y_USERNAME` / `C8Y_USER`, `C8Y_PASSWORD`, `C8Y_TOKEN`)
3. the file `C8Y_SESSION` names

(2) is preferred over (3) deliberately: `eval "$(c8y sessions set ...)"` exports
credentials already decrypted, so that route needs no `C8Y_PASSPHRASE`, while the
file's still do. A host with no credentials is not a session and does not shadow
(3). Nothing is printed about which route was taken; `preflight` says what a
session resolves to.

A `--session` value is resolved as a file: an existing path wins, otherwise it is
matched as a glob against the session home. Zero matches and more than one match
are distinct refusals — c8y itself would silently take the first, and the wrong
tenant here creates objects where they do not belong.

The files are read directly because **`c8y sessions get --session X` does not
report session X** — it reports the active session and ignores `--session`.
`host` and `tenant` are plaintext even in encrypted session files.

Hosts are normalised — `http://` then `https://` stripped, one trailing `/`
stripped, lowercased — before comparing **and** before displaying, or two
spellings of one host look like two hosts and the same-tenant refusal never
fires. That refusal needs host **and** tenant to match: `t12345` on two different
hosts are different tenants.

## export

Defaults: `--childTypes asset,device`; everything else empty or false.

Limits: 25 child objects, 1 000 000 measurements, 1000 events and 1000 alarms per
device. Measurements default to `--dateFrom -14d`.

### Artefacts

| Pattern | Shape |
| --- | --- |
| `exported_device_<id>.json` | the response body **verbatim** |
| `<id>_identities.json` | `[{"type":…,"externalId":…}]`, two-space indent, keys in that order, trailing newline. Written only when the device has identities, and not under `--dataOnly` |
| `<id>_child_<type>.json` | `["<childId>",…]`, two-space indent, trailing newline. One file per child type with ≥1 child |
| `data/<id>/{measurements,events,alarms}.jsonl` | NDJSON, one record per line, verbatim; the file is deleted when empty |
| `data/<id>/binaries/<oldEventId>` | attachment bytes, named by the **source** event id |

`data/<id>/` is removed if it ends up empty. Nothing is stripped on the way out —
`import` decides what to strip.

### Traversal

Per device: write the object, then the identities, then **all** child index files
for **all** requested child types, then emit the progress line, and only then
descend. That way a device's progress line is complete before its children
appear.

The limits pre-flight and the real pass keep separate visited sets. A node with
two parents is fetched once; a cycle terminates.

### Limits

The limits are checked before anything is written: every requested type is
counted for every planned id, and **all** violations are collected before any is
reported. `child objects` is the number of planned ids minus one — the root is
not a child.

The table is `printf '  %-22s %8s %14s %14s\n'` with header
`limit / devices / highest / allowed`, a rule row of dashes, and rows ordered
child objects, measurements, events, alarms. For child objects the devices column
is `-`. Under the table, each data limit names the device with the highest count.

## import

`--mapping` defaults to `id_mapping.json`.

Marker fragment: `c8y_DeviceMigration_<runId>` with an **empty body**, so one
indexed query finds a run. `runId` must be `[A-Za-z0-9_]` only, because it
becomes part of a fragment name.

### Invariants

- **Records keep their backslashes.** A record carrying embedded JSON must not
  have them doubled in transit.
- **The mapping is written after every created object**, so a part-finished
  import is always revertable.
- **Relationships are linked in a final pass.** A relationship can only be
  assigned once both ends exist, and the second may be created much later.
- **`--dataOnly` never writes the mapping.** The mapping is its input, and
  truncating it to `{}` at startup would take the revert record with it.
- **A failed data record makes the import exit non-zero**, after all summaries.
  Individual failures do not stop the run, but a zero exit would let `migrate`
  journal a device as finished with records missing.
- **A missing exported attachment is deliberately not counted as a failure.**
  Retrying cannot fix it, and a resume would loop forever.

## revert

- Two phases: the plan is built and printed in full before anything is deleted.
  It asks **once**, then deletes each object with its own request. `--force`
  skips the question.
- The outcome is established by **re-reading every object**, never by trusting
  the delete response, so an object deleted on request, one taken by propagation
  from its parent, and one left alone because the confirmation was declined are
  all reported correctly. `Delete` in the library treats a 404 as success, so
  trusting it would report an object that was never there as deleted.
- A plan built from a mapping prints `delete <name> (<newId>, imported from
  <oldId>)`; one built from a run id knows no origin and prints
  `delete <name> (<newId>)`.

`import` has no confirmation at all: creating objects is recoverable through
`revert`, which deleting is not.

## migrate

Run id format: `<YYYYMMDD>T<HHMMSS>Z_<6 hex>`.

The phases, and the lines that name them:

| Phase | Chained | Incremental |
| --- | --- | --- |
| export | `Exporting from the source tenant` | `Exporting the hierarchy from the source tenant` |
| import | `Importing into the target tenant` | `Importing the hierarchy into the target tenant`, or `Reconciling the hierarchy in the target tenant` on a resume |
| data | part of the one import | `Migrating data for N devices, one at a time`, then `Device N of M` per device |

`--resume` is validated **first**, before any network call and before a staging
directory exists, so a wrong run id costs nothing. It reads the run from the
journal rather than trusting the caller.

On a resume the hierarchy is re-imported rather than skipped. That import is
idempotent — objects in the mapping are left alone and only the relationships the
target is missing are assigned — and it is what repairs a hierarchy that stopped
half way or that an object was deleted from.

### Journal

`<mapping>.journal`, appended, three line forms:

| Line | Written | Meaning |
| --- | --- | --- |
| `runId <id>` | once, after truncating | which run this journal belongs to |
| `started <oldId>` | before a device's data | that device is in flight |
| `data <oldId>` | after its import succeeds | that device's data has arrived |

Only the incremental path writes one; a chained run imports all data in one step
and could never honestly claim a device was finished. `started X` with no
matching `data X` identifies exactly the interrupted device.

### What a failure says

The staging directory is kept on failure, so a retry does not have to read the
source tenant again, and the exact `revert` command is printed. None of that is
printed for a failure in pre-flight, confirmation or resume validation, which
touched nothing. An attempt that failed before its first import does not report
an earlier attempt's objects as its own leftovers.

## Announcing the run id

`import` prints, once, before it creates anything:

```
Run id RUN_ABC, marking created objects with c8y_DeviceMigration_RUN_ABC
```

Only when a marker is in effect, and never under `--dataOnly`, which creates no
objects and so marks nothing. Before the first object, so an import that dies
part way through has still said which run it was.

The line comes from whatever applies the marker, so **`migrate` does not print it
itself** — two identical lines in one run read like a bug. `migrate` still
reports the id on the `--noFlag` incremental path: nothing is marked there, but
the id is needed to resume, so it has to come from somewhere.

There is no way to *enumerate* the run ids in a tenant: `fragmentType` matches a
fragment name exactly, with no prefix search.

## Refusing an import into the source tenant

`migrate` compares its two sessions, but nothing stops `export` and `import`
being run by hand with the same tenant on both sides — which duplicates the whole
hierarchy inside its own tenant. `import` therefore refuses it, before creating
anything, unless `--allowSameTenant` is given.

**It works by looking up an exported object by its SOURCE id.** Managed object
ids are tenant-scoped, so an id that resolves to an object with the same name,
type *and* creation time is the same object, and therefore the same tenant. Up to
three objects are tried, because the obvious way for one probe to miss is an
object deleted from the source since the export.

It deliberately does **not** compare the export's `self` host against the
session's host: `self` carries whatever host the platform chose — a tenant-id
domain rather than a friendly one, a custom domain, or in an anonymised export a
host that was never real — so a mismatch says nothing. Gating the probe on a
match would make the check pass silently on exactly the imports it exists to
stop.

## Device data

| Thing | Contract |
| --- | --- |
| batch size | 50 measurements per request. Smallest for measurements because they arrive in the largest numbers, and far inside Cumulocity's ~1 MB document, soft 1000-element array and hard five-minute request limits |
| processing mode | `QUIESCENT` for measurements and events. **None for alarms**: it is documented as applying to measurements and events only, and alarm creation is PERSISTENT regardless, so asking would be a claim the platform does not honour |
| ordering | measurements, then events, then alarms, each **oldest first** |
| `source` | rewritten to the new device on every record; the exported one names a device in the source tenant |
| dates | relative `-N` in s, m, h, d or w, or absolute; anything else is **refused**, because exporting the wrong window quietly is worse than not starting |
| attachments | downloaded only for events carrying `c8y_IsBinary`; the file is named by the **source** event id and uploaded against the **new** one |
| a missing attachment | reported, **not** counted as a failure: the export is what is incomplete, retrying cannot fix it, and a resume would loop forever |
| a refused record | reported, counted, and the import **exits non-zero** after finishing the rest |

Two orderings that are not cosmetic. `c8y_LatestMeasurements` on the managed
object holds the last value *received*, not the newest by timestamp, so an
out-of-order backfill leaves a device showing a stale reading as its latest. And
Cumulocity de-duplicates alarms on source and type while one is ACTIVE or
ACKNOWLEDGED, so a still-open alarm created before the cleared history of the
same type would absorb it and only bump its count.

## Library defaults that had to be undone

The go-c8y v2 library is helpful in places where this tool cannot be, and two of
those defaults change observable behaviour:

- **`Identity.Create` calls `IgnoreConflict()`**, so a 409 comes back with a nil
  `Err` and `Status == StatusDuplicate`. Left alone, an external id the target
  tenant already knows would be reported as freshly created — the opposite of
  what an operator needs to hear, because a collision means the target already
  knows that device under that id. The import therefore checks
  `StatusDuplicate` as well as `Err` and reports it as a refused identity.
- **`ChildAssets.Assign` and its siblings also call `IgnoreConflict()`.** Here
  the default is what we want: re-assigning an existing relationship is a no-op
  rather than an error, which is the property a resume needs. Left as is,
  deliberately.

## Finding a run's objects

**`fragmentType` and `query` are mutually exclusive, and `query` wins.** Cumulocity
ignores `fragmentType`, `type`, `owner` and the other simple filters whenever a
`query` parameter is present. So the marker condition has to go *inside* the
query:

```
query=$filter=has(c8y_DeviceMigration_<runId>)
```

which is exactly what `c8y inventory find --fragmentType X` sends.

This matters more than it looks, because paging the inventory *adds* a query: the
library ANDs an `_id gt '<last>'` keyset into it to walk pages in order. Setting
`FragmentType` alongside that means the filter is silently dropped and the search
returns the whole tenant — so a revert by run id would plan to delete the whole
tenant rather than one import.

Two consequences for anything that queries by marker:

- express the condition as `Query: "$filter=has(<fragment>)"`, never as
  `FragmentType`
- page it. The platform's default page size is **5**, and a truncated query is
  worse than a failed one: in a resume's prune, every mapping entry past the
  first page looks deleted, so the resume recreates it.

`internal/testfixture` models both rules — the query/fragmentType exclusivity and
the page limit — so a test cannot pass against a fixture more forgiving than the
platform.

## Byte-level facts, verified

- Child index and identity files are pretty-printed with a two-space indent and a
  trailing newline: `json.MarshalIndent(v, "", "  ")` plus `\n`.
- Identity key order is `type` then `externalId` — the reverse of the c8y
  `--select externalId,type` order.
- Real managed objects contain `600.0` and explicit `null`s. Go's `encoding/json`
  renders `float64(600)` as `600` and cannot tell an absent field from an
  explicit null, so bodies must never round-trip through `map[string]any`. Use
  raw-JSON edits (`sjson`) instead.

The fixture server holds itself to the same rules, or the tests that check
numbers survive are checking nothing:

- records are served as raw JSON, never decoded into `map[string]any`, or `22.0`
  arrives as `22`
- `json.Marshal`'s HTML escaping is off, or `<!DOCTYPE html>` in the fixture's
  alarm text arrives as `\u003c!DOCTYPE html\u003e` — valid JSON that decodes
  identically, but not what the platform sends

## Known quirks

Places where the code does something its help text does not suggest. Each is
relied on, so none is fixed in passing.

1. `export --dataOnly` still writes child index files and still recurses; only
   the identity fetch is skipped. It is correct in practice only because
   `migrate` always pairs it with no child types.
2. `export --dataOnly` still writes `exported_device_<id>.json`, and
   `import --dataOnly` reads each device's name from that file — load-bearing,
   not incidental.
3. `import` reports its device count as the whole mapping, which on a resume
   includes rows carried over from an earlier attempt.
