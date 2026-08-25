# Behaviour contract

What the Go port must reproduce. Derived by reading `commands/{export,import,revert,migrate}`
in full; every claim carries a `file:line` so it can be re-checked rather than trusted.

Where this document and the code disagree, **the code wins** — see
[Known contradictions](#known-contradictions) for the places where the bash code
and its own help text already disagree.

## Shared behaviour

| Thing | Where | Contract |
| --- | --- | --- |
| `use_session` | `export:828`, `import:661`, `revert:294` | export `C8Y_SESSION`, unset `C8Y_HOST C8Y_BASEURL C8Y_URL C8Y_TENANT C8Y_USER C8Y_USERNAME C8Y_PASSWORD C8Y_TOKEN`. `C8Y_PASSPHRASE` is never unset. |
| staging dir | `export:812`, `import:645`, `revert:278` | `mktemp -d "${TMPDIR%/}/c8y-devicemigration.XXXXXXXX"`; `migrate:623` uses `c8y-devicemigration-staging.XXXXXXXX` and does **not** strip a trailing slash |
| `plural n a b` | `export:315` | `a` iff `n == 1` else `b` |
| `read_name` | `export:268`, `import:237` | `.name // empty`, tabs and newlines to spaces; empty or all-spaces becomes the literal `<unnamed>` |
| `--help` | docopt harness | help to **stdout**, exit 0 |
| parse error | docopt harness | message + Usage to **stderr**, exit 1 |

All four scripts are `set -e`, so any unguarded `c8y` failure aborts with c8y's own
exit code.

## export

Usage, verbatim (`export:7`):

```
export (--rootId <id>) (--output-compressed <filename> | --output-directory <directory>) [--childTypes <types> | --no-children] [--dataTypes <types>] [--dateFrom <date>] [--dateTo <date>] [--ignoreLimits] [--metadataOnly | --dataOnly] [--session <file>]
```

Defaults (`export:134`): `--childTypes asset,device`; everything else empty or false.

Limits (`export:147`): `MAX_CHILD_OBJECTS=25`, `MAX_MEASUREMENTS_PER_DEVICE=1000000`,
`MAX_EVENTS_PER_DEVICE=1000`, `MAX_ALARMS_PER_DEVICE=1000`,
`DEFAULT_MEASUREMENT_DATE_FROM=-14d`.

### Artefacts

| Pattern | Shape |
| --- | --- |
| `exported_device_<id>.json` | the response body **verbatim** |
| `<id>_identities.json` | `[{"type":…,"externalId":…}]`, jq pretty-print, keys in that order, trailing newline. Written only when the device has identities, and not under `--dataOnly` |
| `<id>_child_<type>.json` | `["<childId>",…]`, jq pretty-print, trailing newline. One file per child type with ≥1 child |
| `data/<id>/{measurements,events,alarms}.jsonl` | NDJSON, one record per line, verbatim; the file is deleted when empty |
| `data/<id>/binaries/<oldEventId>` | attachment bytes, named by the **source** event id |

`data/<id>/` is removed if it ends up empty (`export:703`). Nothing is stripped on
the way out — `import` decides what to strip (`export:356`).

### Traversal

Per device (`export_device_recursive`, `export:727`): write the object, then the
identities, then **all** child index files for **all** requested child types, then
emit the progress line, and only then descend. This ordering is deliberate so a
device's progress line is complete before its children appear (`export:761`).

Two independent space-padded visited sets — `plannedIds` for the limits pre-flight
(`export:374`) and `exported_ids` for the real pass (`export:731`). A node with two
parents is fetched once; a cycle terminates.

### Limits

`check_limits` (`export:523`) runs before anything is written, counts every
requested type for every planned id, and collects **all** violations before
reporting any. `childObjects = count(plannedIds) - 1` — the root is not a child.

The table (`export:441`) is `printf '  %-22s %8s %14s %14s\n'` with header
`limit / devices / highest / allowed`, a rule row of dashes, and rows ordered
children, measurements, events, alarms. For `children` the label becomes
`child objects` and the devices column `-`.

The window suggestion (`export:513`) understands only `-Nd` and `-Nw`, is the
*tightest* across kinds because `--dateFrom` is one flag, and is omitted entirely
when no window is computable.

## import

Usage (`import:7`); `--mapping` defaults to `id_mapping.json` (`import:114`).

Constants (`import:183`): `MEASUREMENT_BATCH_SIZE=50`, `DATA_WORKERS=2`,
`DATA_DELAY=200ms`, `DATA_PROCESSING_MODE=QUIESCENT`. Alarms get **no** processing
mode — creation is always PERSISTENT (`import:399`).

Marker fragment: `c8y_DeviceMigration_<runId>` with an **empty body**, so one
indexed `fragmentType` query finds a run (`import:158`). `runId` must be
`[A-Za-z0-9_]` only, because it becomes part of a fragment name.

Strip expressions: `STRIP_PROPERTIES` (`import:126`), `EVENT_STRIP_JQ` (`import:268`),
`ALARM_STRIP_JQ` (`import:273`).

### Invariants that were bugs once

- **`sort_by_time` must not use `@tsv`** (`import:278`). `@tsv` escapes backslashes
  and corrupted records carrying embedded JSON.
- **The mapping is written after every created object** (`import:548`), so a
  part-finished import is always revertable.
- **`link_children` is a final pass** (`import:729`). It needs the complete mapping
  to resolve a child that has not been created yet.
- **`--dataOnly` never writes the mapping** (`import:218`). An earlier version
  truncated it to `{}` at startup and took the revert record with it.
- **A failed data record makes the import exit non-zero** (`import:755`), after all
  summaries. Individual failures do not stop the run, but a zero exit would let
  `migrate` journal a device as finished with records missing.
- **A missing exported attachment is deliberately not counted as a failure**
  (`import:363`). Retrying cannot fix it, and a resume would loop forever.
- Within a device: measurements, then events, then alarms, all **oldest first**
  (`import:293`). `c8y_LatestMeasurements` holds the last value *received*, and
  Cumulocity de-duplicates alarms on source+type while ACTIVE or ACKNOWLEDGED.

## revert

Usage (`revert:7`): `(--mapping <filename> | --runId <id>) [--force] [--session <file>]`.

- The `inventory get` probes must **not** be silenced; a non-zero exit *is* the
  "already absent" signal (`revert:116`).
- Deletion is **one** invocation with repeated `--id`, because "Yes to All" is held
  in-process (`revert:196`).
- The outcome is established by **re-reading every object** (`revert:235`), never by
  trusting exit codes, so a propagated delete and a declined confirmation both
  report the truth.
- Plan rows are `<oldId>\t<newId>\t<state>\t<name>`, `state ∈ {present, missing}`.
  A marker-built plan uses `-` as the oldId, and prints no "imported from" clause.
- `print_plan` returns `presentCount` on **stdout**; everything else is stderr.

## migrate

Usage (`migrate:14`). Run id format: `<YYYYMMDD>T<HHMMSS>Z_<6 hex>` (`migrate:187`).

### Session resolution

`resolve_session_file` (`migrate:220`): an existing file path wins; otherwise glob
against the session home. Zero matches and more than one match are distinct
refusals — c8y itself would silently take the first, and the wrong tenant here
creates objects where they do not belong.

This reads the files directly because **`sessions get --session X` does not report
session X** — it reports the active session and ignores `--session` (`migrate:212`).
`host` and `tenant` are plaintext even in encrypted session files.

`normalise_host` (`migrate:269`): strip `http://` then `https://`, strip one
trailing `/`, lowercase. Applied before comparing **and** before displaying, or two
spellings of one host look like two hosts and the same-tenant refusal never fires.

The same-tenant refusal needs host **and** tenant to match: `t12345` on two
different hosts are different tenants.

### Journal

`<mapping>.journal`, appended, three line forms:

| Line | Written | Meaning |
| --- | --- | --- |
| `runId <id>` | once, after truncating | which run this journal belongs to |
| `started <oldId>` | before a device's data | that device is in flight |
| `data <oldId>` | after its import returns 0 | that device's data has arrived |

Only the incremental path writes one; a chained run imports all data in one step
and could never honestly claim a device was finished (`migrate:369`).
`started X` with no matching `data X` identifies exactly the interrupted device.

### Exit behaviour

`trap on_exit EXIT` is installed **after** pre-flight, confirmation and resume
validation (`migrate:625`), so failures in those phases print no staging or revert
hints. `importStarted` (`migrate:500`) distinguishes "this attempt created nothing"
from "stopped part way through", so a failure before the first import does not
report an earlier attempt's objects as this one's leftovers.

## Session resolution in the Go tool

`--session` is optional on `export`. The order is:

1. an explicit `--session` value — wins outright, ambient `C8Y_*` ignored, which
   is what `commands/export` achieves by clearing them in `use_session`
2. the ambient `C8Y_*` variables, i.e. an active session
   (`C8Y_BASEURL` / `C8Y_URL` / `C8Y_HOST`, plus `C8Y_TENANT`,
   `C8Y_USERNAME` / `C8Y_USER`, `C8Y_PASSWORD`, `C8Y_TOKEN`)
3. the file `C8Y_SESSION` names

(2) is preferred over (3) deliberately: `eval "$(c8y sessions set ...)"` exports
credentials already decrypted, so that route needs no `C8Y_PASSPHRASE`, while the
file's still do. A host with no credentials is not a session and does not shadow
(3).

Nothing is printed about which route was taken — `commands/export` prints nothing
either, and the outputs are compared byte for byte.

## Byte-level facts, verified

- Child index and identity files are jq pretty-printed: two-space indent, trailing
  newline. `json.MarshalIndent(v, "", "  ")` plus `\n` reproduces both.
- Identity key order is `type` then `externalId` — the reverse of the c8y
  `--select externalId,type` order.
- jq 1.7.1 **preserves** `600.0` and explicit `null`s, and
  `tests/fixtures/export-9object/` contains both. Go's `encoding/json` renders
  `float64(600)` as `600`, so bodies must never round-trip through
  `map[string]any`. Use raw-JSON edits (`sjson.DeleteBytes`) instead.

## Known contradictions

Places where the bash code and its own documentation disagree. The port mirrors
the **code**; the help text is corrected separately.

1. `export --dataOnly` still writes child index files and still recurses, despite
   its help text. Only the identity fetch is gated (`export:750`). It is correct in
   practice only because `migrate` always pairs it with `--no-children`
   (`migrate:571`).
2. `export --dataOnly` still writes `exported_device_<id>.json` (`export:738`), and
   `import --dataOnly` depends on that file for `backfill_names` (`import:451`) —
   load-bearing, not incidental.
3. `import` reports its device count as the whole mapping (`import:735`), which on a
   resume includes rows carried over from an earlier attempt.
4. `inventory children assign` is unguarded (`import:632`): a failure aborts with
   c8y's exit code and no message of its own.
5. `migrate` does not strip a trailing slash from `TMPDIR` (`migrate:623`) unlike the
   other three.
6. `revert --runId --force` swallows c8y's stderr and replays it only when something
   survived (`revert:225`), so a transient non-404 error on a fully propagated
   delete is dropped.
