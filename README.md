# c8y-devicemigration

Tool for migrating a Cumulocity device hierarchy from one
tenant to another.

It exports a hierarchy — managed objects, their parent and child relationships,
their external identities and optionally their measurements, events and alarms —
and recreates it in a second tenant, keeping a mapping so the whole thing can be
reverted.

## Running it

There are two ways to run it, and which you choose is a matter of who does the
migrating and how they work.

### As a command line tool

A single binary that talks to the Cumulocity REST API directly. It reads the
session files `go-c8y-cli` already writes — including encrypted ones — so there
is nothing else to configure, and unlike `c8y` it needs no session to be active
first.

Install it from a [release](docs/cli.md#building-and-installing) — no Go
toolchain required — or build from source:

```sh
make install        # builds and installs to ~/.local/bin/c8y-devicemigration
```

Then migrate a hierarchy from one tenant to another in a single command, each
side pointed at its own session:

```sh
c8y-devicemigration migrate --rootId 12345 \
    --sourceSession source.eu-latest.cumulocity.com-me.json \
    --targetSession target.eu-latest.cumulocity.com-me.json \
    --dataTypes measurements,events,alarms --incremental --force
```

It names both tenants and asks before creating anything; drop `--force` to keep
the confirmation. `export`, `register`, `import` and `revert` are the same round
trip as separate steps, and `preflight` checks a migration without running it.
Add `--credentials` to provision a device user per device so the fleet can
reconnect — see [Device credentials](#device-credentials). Every command and
option is in **[docs/cli.md](docs/cli.md)**. The port is complete but has not yet
been run against a live tenant.

The same commands are also packaged as a **go-c8y-cli extension** that runs as
`c8y devicemigration …`, behaviour-identical to the binary, for anyone already
working in `c8y` — see **[docs/extension.md](docs/extension.md)**.

### In the platform: the microservice and its UI

The **[microservice](docs/microservice.md)** runs the same engine inside
Cumulocity. It is deployed on a parent tenant (management or enterprise) and
subscribed to the tenants it migrates between, so the platform provides a service
user for each and no session file or personal credential is ever involved. Most
people drive it through its **[user interface](docs/ui.md)** — an Angular plugin
that turns a migration into a list of what has been migrated, a dialogue that
starts another, and a progress bar — rather than calling the API by hand. Reach
for this when migrating should be something the platform performs rather than
something an operator runs from a laptop.

Deploy both onto the parent tenant:

```sh
eval "$(c8y sessions set --sessionFilter parent-tenant)"
make service-deploy     # build and upload the microservice
make ui-package         # -> build/devicemigration-ui-<version>.zip
```

Install that UI zip under **Administration → Ecosystem → Extensions** and add it
to an application. Then, in order: subscribe the parent tenant to the
microservice, subscribe the source and target tenants, and grant
`ROLE_DEVICE_MIGRATION_ADMIN` to whoever should migrate. Open the plugin under
Administration and start one with **New migration**. Both the microservice and
the UI are complete but have not yet been run against a live tenant.

Why the command line and the microservice share one engine, and how the two
command line builds are kept in step, is in
**[docs/internals.md](docs/internals.md)**. For working *on* this repository
with an autonomous coding agent, there is a network-restricted sandbox in
**[docs/devcontainer.md](docs/devcontainer.md)**.

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

## Device data (MEA)

`--dataTypes` takes a comma separated list of `measurements`, `events` and
`alarms`. It defaults to empty, so no device data is collected unless asked for —
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

## Device credentials

A migration is a copy, and a device's existing credentials belong to the source
tenant — so without credentials of its own a migrated device is a twin nothing is
connected to. `migrate --credentials` provisions one device user per device and
writes the passwords to a file:

```sh
c8y-devicemigration migrate --rootId 12345 \
    --sourceSession source.json --targetSession target.json \
    --credentials --credentialsFile ./credentials.csv
```

The devices are registered in the target **before** the hierarchy is imported,
because bulk registration creates each managed object owned by the device user it
creates alongside it, and the import then fills that object in. Registering after
an import only rotates a password and leaves the object alone, so the device would
never own its own twin. `export`, `register` and `import` compose as three steps
for anyone staging a migration by hand.

The credentials file is the **only** copy — the passwords are not recorded in the
target tenant or anywhere else, and re-issuing them rotates them rather than
retrieving them. Getting it onto the devices is the operator's own channel.
`revert` deletes the device users along with the objects.

Devices with no external identity, groups, and devices with several identities
and no `c8y_Serial` are reported and skipped rather than guessed at. The details,
including what happens on a resume, are in
**[docs/cli.md](docs/cli.md#device-credentials)**; the microservice offers the
same thing with a one-time download, in
**[docs/microservice.md](docs/microservice.md#device-credentials)**.

Certificate fleets are a different matter: the credential is the chain and the
devices keep it, which is a trust-anchor move rather than a credential hand-out.
Not built — see
[docs/concepts/credentials.md](docs/concepts/credentials.md).

## Limitations

Managed objects, their parent/child relationships, their external identities
and, on request, their measurements, events and alarms are migrated. In
particular the following are **not** covered:

- operations, for the safety reason above
- device protocols
- anything offloaded to the data lake
- audit logs
- binaries attached to managed objects (event attachments *are* migrated)
- applications, tenant options and user or role assignments

Device credentials **are** covered, for basic-authentication fleets: see
[Device credentials](#device-credentials). A certificate fleet keeps its own
chain and needs a different mechanism, which is not built —
[docs/concepts/credentials.md](docs/concepts/credentials.md).

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

## Licensing

This project is licensed under the Apache 2.0 license - see <https://www.apache.org/licenses/LICENSE-2.0>

______________________

These tools are provided as-is and without warranty or support. They do not constitute part of the Cumulocity product suite. Users are free to use, fork and modify them, subject to the license agreement. While Cumulocity GmbH welcomes contributions, we cannot guarantee to include every contribution in the master project.

______________________

You can find additional information in the [Cumulocity Community](https://community.cumulocity.com/).
