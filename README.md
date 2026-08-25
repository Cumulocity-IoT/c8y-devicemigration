# c8y-devicemigration
**EXPERIMENTAL** go-c8y-cli extension for device migration

Exports a device hierarchy from one Cumulocity tenant and recreates it in another.

## Installation

The extension can be installed using the following command.

```sh
c8y extension install Cumulocity-IoT/c8y-devicemigration
```

## Usage examples

### The whole round trip, in one command

`migrate` exports from one tenant and imports into another in one go, each side
pointed at its own session, so nothing has to be staged by hand or switched
between.

**A session has to be active first.** c8y refuses to run *any* extension
command without one, before `migrate` gets a chance to say anything:

```
WARN   Check existing session failed. commandError: A c8y session has not been loaded.
ERROR  commandError: A c8y session has not been loaded. Please create or activate a session and try again
```

Which session it is does not matter — `migrate` ignores it and uses
`--sourceSession` and `--targetSession`. Activating the source session is the
natural choice, since that is the tenant being read:

```sh
eval "$(c8y sessions set --sessionFilter source-tenant)"

c8y devicemigration migrate --rootId 12345 \
    --sourceSession 'source.eu-latest.cumulocity.com-me.json' \
    --targetSession 'target.eu-latest.cumulocity.com-me.json'
```

It names both tenants and asks before creating anything:

```
Migrating from https://source.eu-latest.cumulocity.com (t11223344)
               to https://target.eu-latest.cumulocity.com (t55667788)
Run id 20260821T091422Z_7f3a9c, marking created objects with c8y_DeviceMigration_20260821T091422Z_7f3a9c
Migrate this hierarchy? Type "yes" to continue:
```

Add the device data, and `--force` to skip the question:

```sh
c8y devicemigration migrate --rootId 12345 \
    --sourceSession source.json --targetSession target.json \
    --dataTypes measurements,events,alarms --dateFrom -30d --force
```

On a large hierarchy, move the data one device at a time so the staging
directory never holds more than one device's worth, and stay resumable:

```sh
c8y devicemigration migrate --rootId 12345 \
    --sourceSession source.json --targetSession target.json \
    --dataTypes measurements,events,alarms --incremental --force
```

If that stops part way through, finish it with the run id it reported:

```sh
c8y devicemigration migrate --rootId 12345 \
    --sourceSession source.json --targetSession target.json \
    --dataTypes measurements,events,alarms \
    --resume 20260821T091422Z_7f3a9c --force
```

Undo a migration, by mapping file or by run id:

```sh
c8y devicemigration revert --mapping ./id_mapping.json --session target.json
c8y devicemigration revert --runId 20260821T091422Z_7f3a9c --session target.json
```

See [`migrate`](#migrate) for every option, and
[Migrating in detail](#migrating-in-detail) for what the run marker,
`--incremental` and `--resume` actually guarantee.

### Exporting and importing separately

`migrate` is the two commands below chained together. Running them by hand is
worth it when the export has to be inspected, kept, or carried between networks.

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

Activate the target tenant, then recreate the hierarchy there:

```sh
eval "$(c8y sessions set --sessionFilter target-tenant)"
c8y devicemigration import --input-directory ./my-export
```

Every command also takes `--session`, which pins it to one tenant regardless of
what is active. That is how one shell can drive both sides of a migration:

```sh
c8y devicemigration export --rootId 12345 --output-directory ./my-export \
    --session 'source.eu-latest.cumulocity.com-me.json'
c8y devicemigration import --input-directory ./my-export \
    --session 'target.eu-latest.cumulocity.com-me.json'
```

`--session` takes a path, a **full file name including its extension**, or a
glob matched against the session home (`session.home` in `c8y settings list`,
`~/.cumulocity` by default). A bare session name without `.json` does not
resolve, and c8y resolves an ambiguous glob to its first match silently, so
prefer the full name. `migrate` refuses an ambiguous glob rather than picking
one — see [the pre-flight](#the-pre-flight).

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

## Commands

Four commands. `migrate` is the whole round trip; `export`, `import` and
`revert` are the pieces it is built from and are useful on their own.

| Command | What it does |
| --- | --- |
| [`migrate`](#migrate) | Export from one tenant and import into another in one command |
| [`export`](#export) | Write a hierarchy, its external identities and optionally its data to disk |
| [`import`](#import) | Recreate all of that in the active or given tenant |
| [`revert`](#revert) | Delete what an import created, by mapping file or run id |

<!-- BEGIN COMMAND REFERENCE -->

Generated from the commands themselves by `tools/gen-command-docs.sh`.
Run `make docs` after changing an option, or `make check` will say so.

### `migrate`

```
Migrate a device hierarchy from one tenant to another in one command

A session must already be active, because c8y will not run an extension command
without one. Which session it is does not matter: migrate ignores it and uses
--sourceSession and --targetSession. Activating the source session is the
natural choice.

  eval "$(c8y sessions set --sessionFilter source-tenant)"

Usage:
  migrate (--rootId <id>) (--sourceSession <file>) (--targetSession <file>) [--childTypes <types> | --no-children] [--dataTypes <types>] [--dateFrom <date>] [--dateTo <date>] [--ignoreLimits] [--mapping <filename>] [--incremental] [--resume <runid>] [--keepStaging] [--noFlag] [--force]

Options:
    --rootId ID         ID of the root device to migrate (required)
    --sourceSession file
                        Session to read from: a path, a full file name including
                        its extension, or a glob matched against the session
                        home. A bare name without the extension does not resolve
    --targetSession file
                        Session to write to, same forms as --sourceSession
    --childTypes types  Comma separated list of child relationship types to
                        follow, one or more of asset, device and addition
                        [default: asset,device]
    --no-children       Migrate only the root device
    --dataTypes types   Comma separated list of device data to include, one or
                        more of measurements, events and alarms. Defaults to
                        none
    --dateFrom date     Only include data at or after this time. Measurements
                        default to the last 14 days
    --dateTo date       Only include data at or before this time
    --ignoreLimits      Migrate even if an export limit is exceeded
    --mapping filename  File to write the old ID to new ID mapping to
                        [default: id_mapping.json]
    --incremental       Move the hierarchy first and then the data one device
                        at a time, so the staging directory only ever holds a
                        single device's data. Slower, but the disk it needs does
                        not grow with the size of the migration
    --resume runid      Continue the run with this id instead of starting a new
                        one, skipping the devices whose data already arrived.
                        Implies --incremental, and needs the journal that an
                        incremental run writes next to the mapping file
    --keepStaging       Keep the staging directory instead of removing it
    --noFlag            Do not mark created managed objects with the run
                        fragment
    --force             Do not ask for confirmation
```

### `export`

```
Export devices and their data from Cumulocity

Usage:
  export (--rootId <id>) (--output-compressed <filename> | --output-directory <directory>) [--childTypes <types> | --no-children] [--dataTypes <types>] [--dateFrom <date>] [--dateTo <date>] [--ignoreLimits] [--metadataOnly | --dataOnly] [--session <file>]

Options:
    --rootId ID         ID of the root device to export (required)
    --output-compressed filename
                        Output file name for the compressed export
    --output-directory directory
                        Output directory for the uncompressed export
    --childTypes types  Comma separated list of child relationship types to
                        follow, one or more of asset, device and addition
                        [default: asset,device]
    --no-children       Export only the root device without following any
                        child relationships
    --dataTypes types   Comma separated list of device data to include, one or
                        more of measurements, events and alarms. Defaults to
                        none, so no data is exported unless asked for
    --dateFrom date     Only export data at or after this time, absolute or
                        relative, e.g. 2026-01-01T00:00:00Z or -30d.
                        Measurements default to the last 14 days; events and
                        alarms default to the whole history
    --dateTo date       Only export data at or before this time
    --ignoreLimits      Export even if a limit below is exceeded. Not the
                        default, and it warns when used
    --metadataOnly      Export managed objects and external identities only,
                        skipping the data itself. The limits are still checked
                        against whatever --dataTypes selects, so an oversized
                        migration is refused before anything is created
    --dataOnly          Export the data only, skipping external identities and
                        child indexes. The opposite of --metadataOnly, and what
                        migrate --incremental uses to move one device's data
    --session file      Session to use for this command: a path, a full file
                        name including its extension, or a glob matched against
                        the session home. A bare name without the extension does
                        not resolve

Limits:
  Checked before anything is written, and the export stops without leaving a
  partial result behind if one is exceeded:

    at most 25 child objects in the hierarchy
    at most 1000000 measurements per device
    at most 1000 events per device
    at most 1000 alarms per device
```

### `import`

```
Import devices and their data into Cumulocity

Usage:
  import (--input-compressed <filename> | --input-directory <directory>) [--mapping <filename>] [--force] [--session <file>] [--runId <id>] [--dataOnly | --resume]

Options:
    --input-compressed filename
                        Compressed export archive to import
    --input-directory directory
                        Directory containing an uncompressed export
    --mapping filename  File to write the old ID to new ID mapping to
                        [default: id_mapping.json]
    --force             Do not prompt for confirmation before creating or
                        assigning managed objects
    --session file      Session to use for this command: a path, a full file
                        name including its extension, or a glob matched against
                        the session home. A bare name without the extension does
                        not resolve
    --runId id          Mark every created managed object with an empty
                        c8y_DeviceMigration_<id> fragment, so the objects of one
                        migration can be found with a single --fragmentType
                        query. Must be letters, digits and underscores only
    --resume            Continue an import that stopped part way through, using
                        the mapping file as the record of what was already
                        created. Objects listed there are not created again, and
                        only the relationships the target is missing are
                        assigned
    --dataOnly          Import only measurements, events and alarms, resolving
                        each device through an existing mapping file. Creates no
                        managed objects and no relationships, so it can add data
                        to a hierarchy that was migrated earlier
```

### `revert`

```
Revert an import by deleting the managed objects it created

Usage:
  revert (--mapping <filename> | --runId <id>) [--force] [--session <file>]

Options:
    --mapping filename  ID mapping file written by import
    --runId id          Find the objects by their c8y_DeviceMigration_<id>
                        marker fragment instead of a mapping file, for when the
                        mapping was lost or never written. Only finds objects
                        that were marked, so an import run with --noFlag cannot
                        be reverted this way
    --force             Delete without asking for confirmation
    --session file      Session to use for this command: a path, a full file
                        name including its extension, or a glob matched against
                        the session home. A bare name without the extension does
                        not resolve
```

<!-- END COMMAND REFERENCE -->

## Migrating in detail

### The pre-flight

Before `migrate` creates anything it resolves both sessions, names the tenants
and the files they came from, proves the credentials work with an authenticated
call, and asks unless `--force` was passed:

```
Migrating from https://source.eu-latest.cumulocity.com (t11223344)
               to https://target.eu-latest.cumulocity.com (t55667788)
  source session: source.eu-latest.cumulocity.com-me.json
  target session: target.eu-latest.cumulocity.com-me.json
```

The hosts are shown normalised, without the scheme: session files disagree
about whether they include `https://`, and two spellings of one host must not
look like two hosts.

It refuses, before contacting anything, when:

- either session matches no file — a bare name without `.json` is the usual cause
- either session matches **several** files. c8y would take the first without
  saying which, and the wrong tenant here creates objects where they do not
  belong, so `migrate` lists the matches and asks for an exact name
- both sessions are the **same tenant**, compared on normalised host *and*
  tenant id. Duplicating a hierarchy into the tenant it came from is almost
  never what was meant. The pair matters: `t12345` on `eu-latest` and `t12345`
  on another host are different tenants, and refusing that migration would be
  wrong

The identity comes from reading the session files, not from asking c8y.
`c8y sessions get --session X` **does not report session X** — it reports
whichever session is active and ignores `--session`, so with a source session
loaded it answers "source" for both sides and the same-tenant check refuses a
good migration. `host` and `tenant` are plaintext in a session file even when
`password` and `token` are encrypted, so reading them needs no passphrase, no
network and no active session.

For the same reason every call clears the ambient `C8Y_*` variables and sets
`C8Y_SESSION` explicitly. Activating a session exports `C8Y_HOST` and
`C8Y_TENANT`, and those otherwise decide which tenant is talked to whatever
`--session` says. `C8Y_PASSPHRASE` is deliberately kept — without it an
encrypted session file cannot be decrypted at all.

The file each argument resolved to is what the export and import are given, so
the tenants named above are the tenants used. A glob is never resolved twice.

None of this can help with the missing-session error above, because c8y raises
it before the extension is invoked at all.

The export is staged in a temporary directory that is removed afterwards;
`--keepStaging` keeps it and says where.

If a migration stops part way through, the staging directory is kept regardless
— retrying should not have to read the source tenant again — and the exact
`revert` command for what was created is printed, since that is the moment it is
needed:

```
The migration stopped part way through. Undo what it created with:
  c8y devicemigration revert --mapping id_mapping.json --session target.json
  or, if the mapping is lost:
  c8y devicemigration revert --runId 20260821T091422Z_7f3a9c --session target.json
```

A failure before anything was created says nothing about reverting, because
there is nothing to undo.

### The run marker

Every managed object `migrate` creates gets an empty fragment named after the
run:

```json
"c8y_DeviceMigration_20260821T091422Z_7f3a9c": {}
```

The identity is in the fragment *name*, so one indexed query finds everything a
run created, with no client-side filtering:

```sh
c8y inventory find --fragmentType c8y_DeviceMigration_20260821T091422Z_7f3a9c
```

This is in addition to `id_mapping.json`, not a replacement: the body is empty,
so the fragment says which run created an object but not which source object it
came from. Pass `--noFlag` to leave the objects unmarked.

**One fragment per object, deliberately.** There is no second, constant
`c8y_DeviceMigration` fragment, so "what did this tool ever create in this
tenant?" is not an indexed query — `fragmentType` matches a fragment name
exactly, and the inventory query language wildcards fragment *values*, not
names. Answering it means either knowing the run ids, which the mapping files
and journals record, or a client-side `--filter` over the whole inventory. That
was weighed against stamping a second fragment onto every migrated object, and
the single run-scoped marker won.

### Large migrations: `--incremental`

By default the whole export is written to the staging directory and then
imported, so the disk it needs grows with the size of the migration. With
`--incremental` the hierarchy moves first — objects, external identities and
relationships, which stay small however many records are involved — and then the
data moves one device at a time and is discarded as it goes:

```sh
c8y devicemigration migrate --rootId 12345 \
    --sourceSession source.json --targetSession target.json \
    --dataTypes measurements,events,alarms --incremental --force
```

```
Exporting the hierarchy from the source tenant
...
Importing the hierarchy into the target tenant
...
Migrating data for 9 devices, one at a time
Device 1 of 9
Exported Plant Berlin (5174376875)
Device 2 of 9
Exported Line A (7874377868), 412 measurements, 38 events, 2 alarms
Imported 412 measurements for Line A (1004)
Imported 38 events for Line A (1004)
Imported 2 alarms for Line A (1004)
```

The result is the same either way. What changes is that the staging directory
only ever holds one device's data, that a failure surfaces at the first device
rather than after the last one, and that the progress reported means something on
a hierarchy that takes a while. The cost is one extra read per device, plus a
second count query per data type, because the limits below are still checked
across the whole hierarchy up front — an oversized migration has to be refused
before anything exists in the target tenant.

The two halves are available separately, which is what `--incremental` uses
internally: `export --metadataOnly` writes objects and identities without the
data but still checks the limits, and `import --dataOnly` adds data to a
hierarchy that was migrated earlier, resolving each device through an existing
mapping file. `--dataOnly` never writes to the mapping — it is input only.

### Picking up where it stopped: `--resume`

An `--incremental` migration writes a journal next to the mapping file
(`id_mapping.json.journal`). The mapping is already the record of which objects
exist — it is written after every create — so the journal fills the only gap:
which devices' data actually arrived. Together they are enough to finish a run
that stopped:

```sh
c8y devicemigration migrate --rootId 12345 \
    --sourceSession source.json --targetSession target.json \
    --dataTypes measurements,events,alarms \
    --resume 20260821T091422Z_7f3a9c --force
```

```
Reconciling the hierarchy in the target tenant
Already imported Plant Berlin (5174376875 -> 1001)
Imported Line B (7874377868 -> 1005)
Already assigned Line A (1004) as asset child of Plant Berlin (1001)
Migrating data for 9 devices, one at a time
Run stopped while migrating data for (7874377868), so its records are delivered again and that device can end up with duplicates
Device 1 of 9: data already migrated for (5174376875)
```

`--resume` implies `--incremental` and keeps the original run id, so one marker
still covers every object of the run however many attempts it took.

What it guarantees, and what it does not:

- **No duplicate objects.** Anything in the mapping is left alone.
- **No duplicate relationships.** The target is asked which children a parent
  already has, and only the missing ones are assigned. Resuming a finished run
  is a no-op.
- **No lost data.** A device is journalled only once its records are in the
  target, so an interruption means that device is done again rather than
  skipped.
- **Records of the interrupted device can be duplicated.** There is no
  per-record account of what arrived, so the device that was in flight is
  delivered again in full. It is named on resume rather than left as a general
  caveat, so the choice is visible: duplicated measurements on one device
  against missing ones.

Two things it refuses rather than guesses at. A run id the journal does not
know, and a chained migration — which has no per-device commit point, so
resuming it could deliver the same data twice. Both fail before any tenant is
contacted.

If the run was made with `--noFlag` there is no marker, so an object deleted
from the target since the original attempt cannot be told from a live one and the
mapping is taken at its word. With the marker, one `--fragmentType` query finds
what is really still there and anything missing is created again.

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
`c8y` itself, e.g. `-30d`. See [Limits](#limits) for the defaults and the caps
that apply.

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
- **Server managed properties are stripped** from every record before it is
  sent, including `source.self`, which points at the source tenant. `source`
  itself is kept, because `--device` overwrites `source.id` and needs the field
  to be there.
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

### Limits

An export refuses to run when it would be larger than these:

| Limit | Value |
|---|---|
| child objects in the hierarchy, total | 25 |
| measurements per device | 1&nbsp;000&nbsp;000 |
| events per device | 1000 |
| alarms per device | 1000 |

Measurements also default to the **last 14 days** when no `--dateFrom` is given,
because an unbounded measurement export is the one that runs away. Events and
alarms default to the whole history. An explicit `--dateFrom` applies to all
three.

The limits are counted **before anything is written**, using one cheap count
query per device per type rather than fetching the records, so a refusal leaves
nothing behind at all. Every limit is checked for every device before anything
is reported — the run does not stop at the first problem — and the report shows
what was found against what is allowed, with a suggested limit alongside:

```
Export limits exceeded:

  limit                   devices        highest        allowed      suggested
  ---------------------- -------- -------------- -------------- --------------
  child objects                 -             30             25             50
  measurements                 31        2400000        1000000        3000000
  alarms                       31           1200           1000           2000

  measurements: highest on device 7874377868
  alarms: highest on device 4074377869

Suggested values are the current limit scaled up to the smallest whole
multiple that would allow this export.

Measurements are windowed, so --dateFrom -5d would bring them within
the current limit of 1000000 instead (currently -14d).

Nothing was exported. Narrow the export with --childTypes, --dataTypes
or --dateFrom, or pass --ignoreLimits to export regardless.
```

The report is one row per limit rather than per device: a wide hierarchy would
otherwise produce hundreds of near-identical lines, and the worst count is what
decides the suggestion anyway. The device holding that worst count is named
underneath.

Measurements get a second suggestion because they are the only windowed type:
the same linear scaling applied to the window instead of the limit. It is only
offered when the window is expressed in whole days or weeks — an absolute date
cannot be scaled, so no guess is made.

`--dateFrom` and `--dateTo` are passed to `c8y` untouched, so every form it
accepts works: `-30d`, `-14d`, `-6months`, `-2w`, `-30h+30min`, `0s`, or an
absolute `2026-01-01T00:00:00Z`.

If a limit is breached anyway — data can grow between the count and the fetch —
the export stops and deletes what it wrote. It removes only the files it
created: `--output-directory` may point at a directory holding unrelated
content, and that is not the export's to delete.

`--ignoreLimits` turns the checks off. It is not the default and it warns
whenever it is used, whether or not a limit was actually exceeded.

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
objects listed in it. It needs to be told what to delete — either a mapping file
or a run id — and never guesses.

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

### Reverting without the mapping

If the mapping was lost, the run marker is the other way in. `--runId` asks the
tenant which objects carry `c8y_DeviceMigration_<runId>` and plans those:

```sh
c8y devicemigration revert --runId 20260821T091422Z_7f3a9c \
    --session 'target.eu-latest.cumulocity.com-me.json'
```

```
The following managed objects will be deleted from the active session:
  delete Customer 1 (359973220)
  delete F1 (739987216)
```

There is no "imported from" here: the marker body is empty, so it records which
run created an object and not which object it came from. Two consequences worth
knowing before relying on it — an import run with `--noFlag` marks nothing and
cannot be reverted this way, and the plan is built from what the tenant has now,
so an object that was marked and later had the fragment removed is invisible to
it. The mapping remains the more precise record; this is the fallback.

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
copies. `import --resume` is the exception, and only because the mapping tells it
which objects it made itself.

A refused data record does not stop the import — one bad record should not cost a
whole migration — but it does make it **exit non-zero**, with a count of what did
not arrive. A zero exit code means every record landed, which is what lets
`migrate` treat a device as finished.

## Output

Every command reports its own progress on stderr, one line per managed
object, naming it and putting IDs in brackets. The raw responses from `c8y` are
not printed: `export` writes them to the export directory, and `revert` only
needs them to build its plan. This keeps the output the same regardless of a
user's `output` and `view` settings, which otherwise decide whether a full
managed object is rendered for every device the commands touch.

## Development

The test suite runs entirely against a fake `c8y` on `PATH`, so it never touches
a tenant and needs no session.

```sh
make check      # lint, parser freshness, command reference freshness, tests
make test       # tests only (bootstraps bats into tests/vendor on first run)
make test-fast  # the same tests across every core, needs GNU parallel
make lint       # shellcheck, skipped with a warning if not installed
make docs       # regenerate the command reference in README.md
```

The **Commands** section of this README is generated from the `DOC` string of
each command by `tools/gen-command-docs.sh`, between the
`<!-- BEGIN COMMAND REFERENCE -->` markers. Run `make docs` after changing an
option; `make check` and CI fail if it was not. Hand-editing an option list
there is exactly the drift nobody notices until someone follows the README and
it does not work.

`make test` is sequential, which is what you want when debugging one failure.
`make test-fast` is the same suite with `bats --jobs`, and takes about a quarter
of the time; CI uses it. `make test JOBS=4` picks a specific level.

Two things keep the suite quick enough to run casually. Tests are parallel-safe
because every piece of per-test state lives under `$BATS_TEST_TMPDIR` — keep it
that way, and in particular do not assert against the shared temp directory,
since concurrent tests write there too. And identical exports and imports are
memoised per file by `shared_export` and `shared_migration` in
`tests/helpers/load.bash`: the first test needing a given invocation runs it and
the rest get a private copy.

The cache key covers the arguments **and** the `FAKE_C8Y_*` environment. Both
halves matter: per-test paths are normalised out of it or it would never hit,
and the environment is included because two runs with the same arguments but a
different hierarchy fixture are not the same run. A test that mutates what it is
given should call `run_export`/`run_import` directly rather than the memoised
helpers.

The fake can be made to fail on purpose, which is how the interruption paths are
tested: `FAKE_C8Y_FAIL_CREATE_NAME` stops an import at one named object, and
`FAKE_C8Y_FAIL_DATA_AFTER` lets a number of data creates through and then
refuses, so the failure lands *inside* a device's data rather than neatly
between two devices. The full list is at the top of `tests/fake/c8y`.

`tests/fixtures/hierarchy.json` describes the fake tenant. It is shaped
deliberately: three parents have two children of the same type, because a parent
with a single child cannot expose the class of bug where `c8y` consumes the
stdin pipe driving a `while read` loop and only the first child is processed.
It also contains a cycle, an object with two parents, an addition-only branch,
an unnamed object, and names carrying quotes, brackets, tabs and non-ASCII.

Every command parses its arguments with a generated docopt parser held between
the `# docopt parser below/above` markers. After editing a `DOC` string, regenerate
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

The jsonnet templates that strip server managed properties are evaluated for
real in the suite, using `c8y template execute`, which needs no session and no
network. Those tests assert the resulting body rather than the template text,
and they skip when `c8y` is not installed so the suite stays hermetic in CI.

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
