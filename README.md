# c8y-devicemigration
**EXPERIMENTAL** go-c8y-cli extension for device migration

Exports a device hierarchy from one Cumulocity tenant and recreates it in another.

## Installation

The extension can be installed using the following command.

```sh
c8y extension install Cumulocity-IoT/c8y-devicemigration
```

## Usage examples

Export a device and everything below it into a directory:

```sh
c8y devicemigration export --rootId 12345 --output-directory ./my-export
```

```
Exported Customer 1 (5174376875), 2 asset children, 1 identity
Exported F1 (7874377868), 2 asset children
Exported Normal #1 (37218800), 1 device child
Exported 9 managed objects and 3 external identities to ./my-export
```

Export into a single compressed archive instead:

```sh
c8y devicemigration export --rootId 12345 --output-compressed ./my-export.tgz
```

Device data is not exported unless asked for, so a plain export stays fast:

```sh
c8y devicemigration export --rootId 12345 --output-directory ./my-export \
    --dataTypes measurements,events,alarms --dateFrom -30d
```

```
Exported Customer 1 (5174376875), 2 asset children, 1 identity
Exported F1 (7874377868), 2 asset children, 412 measurements, 38 events, 2 alarms
Exported 9 managed objects and 3 external identities, with 1204 measurements, 38 events and 2 alarms to ./my-export
```

By default only the `asset` and `device` relationships are followed. Use
`--childTypes` to migrate additions as well, or to narrow the export further:

```sh
c8y devicemigration export --rootId 12345 --output-directory ./my-export \
    --childTypes asset,device,addition
```

Or export the root device on its own, following nothing:

```sh
c8y devicemigration export --rootId 12345 --output-directory ./my-export --no-children
```

Activate the target tenant, then recreate the hierarchy there. `import` always
uses the currently active session, so the session has to be switched before it
runs rather than with a `--session` flag on the command itself:

```sh
eval "$(c8y sessions set --sessionFilter target-tenant)"
c8y devicemigration import --input-directory ./my-export
```

Importing an archive works the same way, and the mapping file can be redirected:

```sh
c8y devicemigration import --input-compressed ./my-export.tgz --mapping ./ids.json
```

Each managed object is created with a POST, and go-c8y-cli asks for confirmation
on those for a production session. Pass `--force` to accept everything up front,
which is what you want for anything but a handful of devices:

```sh
c8y devicemigration import --input-directory ./my-export --force
```

`import` reports every object it touches, by name, with the IDs in brackets:

```
Imported Plant Berlin (1 -> 1001)
Imported Pump A (2 -> 1002)
Assigned Pump A (1002) as device child of Plant Berlin (1001)
```

The create lines show the old ID and the ID the target tenant assigned; the
assign lines show the new IDs, since that is what the relationship was built
from. Managed objects without a name are reported as `<unnamed>`.

If a migration turns out to be wrong, `revert` deletes what an import created,
working from the mapping file. It lists everything it intends to delete and asks
before touching anything:

```sh
c8y devicemigration revert --mapping ./id_mapping.json
```

## What gets migrated

The migration includes all properties and fragments of the concerned managed
objects and all external identities. Device protocols are not migrated.

`export` walks the child relationships from the given root and writes, per
device, the raw managed object as `exported_device_<id>.json`, one
`<id>_child_<childType>.json` index per relationship type, and
`<id>_identities.json` for the device's external IDs when it has any. A device
referenced by several parents is exported once, and cycles in the hierarchy
terminate.

Which relationships are followed is controlled by `--childTypes`, a comma
separated list of `asset`, `device` and `addition`. It defaults to
`asset,device`, so additions are skipped unless they are asked for. This applies
to the whole traversal: a device that is only reachable through an addition is
not exported under the default.

`--no-children` turns the traversal off completely and exports just the root
managed object, with no relationship index files. It cannot be combined with
`--childTypes`.

`import` recreates every exported managed object in the currently active session
and then rebuilds the relationships using the IDs assigned by the target tenant.
It restores whatever the export contains and has no child type option of its
own, so the selection is made once, at export time.
Because those IDs are new, the old-to-new mapping is written to
`id_mapping.json` (override with `--mapping`) so the migration can be traced or
scripted against afterwards. It is rewritten after every created object rather
than once at the end, so an import that dies part way through still leaves a
complete record of what it managed to create. Without that, a partial import
would strand objects in the target tenant with nothing to identify them by.

Server managed properties are dropped before a managed object is recreated:
`id`, `self`, `lastUpdated`, `creationTime`, `owner`, the parent and child
reference collections, and the computed `c8y_Availability`,
`c8y_ActiveAlarmsStatus` and `c8y_Connection` fragments. Everything else,
including custom fragments, is sent as exported.

External identities are recreated against the new managed object IDs, keeping
the original type and external ID. An external ID is unique per type within a
tenant, so one the target tenant already knows will be rejected: `import`
reports it, counts it, and carries on rather than abandoning the migration
part-way. The closing summary states how many identities were created and how
many were refused, so a partial result is never silent.

## Device data

`--dataTypes` takes a comma separated list of `measurements`, `events` and
`alarms`. It defaults to empty, so no data is collected unless asked for —
measurement volumes make that the only sensible default. `--dateFrom` and
`--dateTo` bound the window and accept the same absolute and relative forms as
`c8y` itself, e.g. `-30d`.

Data lands in per-device folders as newline-delimited JSON, one record per line,
which is what `c8y` emits and what its create commands consume:

```
data/<id>/measurements.jsonl
data/<id>/events.jsonl
data/<id>/alarms.jsonl
data/<id>/binaries/<eventId>      one file per event attachment
```

Records are written **verbatim**; `import` does the stripping, exactly as it
does for the managed objects themselves. That keeps the export a faithful
record and is what makes event attachments correlatable.

On import:

- **Measurements** go through `c8y measurements createBulk`, which posts a
  measurement collection rather than one request per record. Batches are
  deliberately small — a migration should not be the reason a tenant starts
  throttling.
- **Alarms** keep their `status` and `severity`, which are settable at creation.
  `count`, `firstOccurrenceTime` and the audit `history` are dropped: that is
  the "current status, not the full history" intent, and none of them are
  settable through the API anyway.
- **Events** with an attachment are created one at a time so their new ID is
  known before the file is uploaded; the rest go through the fast piped path.
- **Records are sent oldest first.** Two reasons: `c8y_LatestMeasurements` on
  the managed object holds the last value *received*, not the newest by
  timestamp, so an out-of-order backfill would leave a device showing a stale
  reading as its latest; and Cumulocity de-duplicates alarms on source and type
  while one is ACTIVE or ACKNOWLEDGED, so a still-open alarm has to be created
  after the cleared history of its type or it absorbs it.
- **`--processingMode QUIESCENT`** is used for measurements and events, which
  persists the record while suppressing realtime notifications. It is
  documented as applying to those two only — alarm creation is always
  PERSISTENT whatever the header says.

### Operations are not migrated

`c8y operations create` accepts no status and `creationTime` is server-assigned,
so every recreated operation would arrive as `PENDING` — a live command that the
target device would pick up and execute, since pending operations are pushed to
devices on subscribe. Operations also have no `time` field at all, so their
original timing could not be preserved even if the status could. Asking for
`--dataTypes operations` fails with that explanation rather than silently
ignoring it.

### Before importing data into a tenant

Two platform behaviours can ruin a data migration, and neither can be handled
from here:

- **Smart rules and EPL apps will fire on imported data.** No processing mode
  suppresses CEP/Apama. The stock smart rules include *on alarm send SMS*, *on
  alarm send email*, *on alarm execute operation* and *on measurement threshold
  create alarm*, so importing a history into a tenant with those active can
  send real notifications and create real operations on real devices.
  Deactivate Apama-ctrl/Smartrule, or the individual rules, for the duration of
  the import.
- **Retention rules will delete the import.** The documented default removes
  historical data after 60 days, with cleanup running about daily, so a
  two-year history imported into a default tenant is largely gone within a day.
  Raise the target tenant's retention rules first.

Worth checking too: alarm mapping rules on the target can rewrite an incoming
alarm's severity and text by type prefix, and one of the options is *Drop*.

## Reverting a migration

`revert` takes the mapping file written by `import` and deletes the managed
objects listed in it from the currently active session. The mapping is the only
input, and it is required — there is no way to ask `revert` to guess.

It runs in two phases. First it looks up every object in the mapping and prints
what it found, without changing anything:

```
The following managed objects will be deleted from the active session:
  delete Customer 1 (359973220, imported from 5174376875)
  delete F1 (739987216, imported from 7874377868)

Already absent, nothing to do for these:
  skip   409987215 (imported from 37217806)

1 managed objects will be deleted, 1 are already absent.
```

Then it hands the deletions to `c8y inventory delete`, which asks for
confirmation with its own dialog:

```
Confirm (job: 1)
Delete on https://…/inventory/managedObjects/359973220
[y] Yes  [a] Yes to All  [n] No  [l] No to All
```

Every object goes to a single `c8y` invocation rather than one call each,
because "Yes to All" is remembered only within one process: answering `a` once
covers the whole revert, and `l` stops it. `--force` is passed straight through
and suppresses the dialog, which is what you want in a script.

What actually happened is then established by looking each object up again
rather than by trusting exit codes, so the closing report is accurate whether an
object was deleted on request, taken by propagation from its parent, or left
alone because you declined.

Propagation means an object can already be gone by the time its own delete
request is sent, which Cumulocity answers with a 404. Those are expected, so
`revert` passes `--silentStatusCodes 404 --silentExit` to keep them out of the
output.

Under `--force` it additionally holds back whatever c8y writes to stderr and
shows it only if an object survived, so a clean revert is silent and a failed
one still explains itself. Without `--force` stderr is left alone, because that
is the stream c8y renders its confirmation dialog on — capturing it would hide
the prompt while c8y waited for an answer.

Because the mapping is written as the import progresses, this works for a
**partial import** — the case it mainly exists for. An import that fails on its
fourth object leaves three in the tenant and three in the mapping, and `revert`
removes exactly those three.

Deleting a managed object takes its external identities and its parent/child
references with it, so `revert` does not delete those separately. That has been
confirmed against a live tenant, so a reverted migration leaves no external IDs
behind to collide with a later re-import. Note that
Cumulocity propagates a deletion to subgroups when the object is a group, which
can remove objects this import never created; the plan says so before you
confirm.

Anything still present when `revert` finishes is reported and makes it exit
non-zero, so a declined confirmation and a genuine failure are both visible.
Rerunning is safe: objects already gone are listed as absent and skipped.

## Limitations

Managed objects, their parent/child relationships, their external identities
and, on request, their measurements, events and alarms are migrated. In
particular the following are **not** covered:

- operations, for the safety reason above
- device protocols
- anything offloaded to the data lake
- audit logs
- binaries attached to managed objects (event attachments *are* migrated)
- device credentials, so migrated devices do not reconnect on their own
- applications, tenant options and user or role assignments

There is no per-record ID mapping for data. On tenants with enhanced time series
enabled there is no stable per-measurement identity to map, so traceability, if
ever needed, would mean stamping a fragment onto each record rather than keeping
a mapping file.

Existing objects in the target tenant are never matched or updated: every import
creates new managed objects, so importing the same export twice produces two
copies.

## Output

All three commands report their own progress on stderr, one line per managed
object, naming it and putting IDs in brackets. The raw responses from `c8y` are
not printed: `export` writes them to the export directory, and `revert` only
needs them to build its plan. This keeps the output the same regardless of a
user's `output` and `view` settings, which otherwise decide whether a full
managed object is rendered for every device the commands touch.

## Development

The test suite runs entirely against a fake `c8y` on `PATH`, so it never touches
a tenant and needs no session.

```sh
make check     # what CI runs: lint, parser freshness, tests
make test      # tests only (bootstraps bats into tests/vendor on first run)
make lint      # shellcheck, skipped with a warning if not installed
```

`tests/fixtures/hierarchy.json` describes the fake tenant. It is shaped
deliberately: three parents have two children of the same type, because a parent
with a single child cannot expose the class of bug where `c8y` consumes the
stdin pipe driving a `while read` loop and only the first child is processed.
It also contains a cycle, an object with two parents, an addition-only branch,
an unnamed object, and names carrying quotes, brackets, tabs and non-ASCII.

Both commands parse arguments with a generated docopt parser held between the
`# docopt parser below/above` markers. After editing a `DOC` string, regenerate
it or every invocation will exit 70:

```sh
docopt.sh commands/export      # pipx install docopt-sh==2.0.3
```

### What the tests cannot cover

The fake encodes assumptions about real `c8y` behaviour, listed at the top of
`tests/fake/c8y`. All of them have been confirmed against a live tenant, and
every command path has been exercised there at least once: export, import
including external identities, and revert. Any change to those assumptions
should be recorded in that file, since a fake that quietly drifts is worse than
no test at all.

The suite still cannot see a real tenant, so after changing anything that talks
to `c8y`, smoke it manually:

1. `c8y devicemigration export --rootId <id> --output-directory ./smoke`
2. Check one `exported_device_*.json` looks like the managed object, and that
   each `*_child_*.json` is a JSON array holding every child.
3. Switch to a throwaway tenant and import with `--force`.
4. Confirm the reported link count matches the number of references in the
   export, and spot-check the hierarchy in the UI.
5. Check the `Added external identity ...` lines appear for any device that had
   external IDs, and that those IDs resolve in the target tenant.

## Licensing

This project is licensed under the Apache 2.0 license - see <https://www.apache.org/licenses/LICENSE-2.0>

______________________

These tools are provided as-is and without warranty or support. They do not constitute part of the Cumulocity product suite. Users are free to use, fork and modify them, subject to the license agreement. While Cumulocity GmbH welcomes contributions, we cannot guarantee to include every contribution in the master project.

______________________

You can find additional information in the [Cumulocity Community](https://community.cumulocity.com/).
