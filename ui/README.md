# Cumulocity Device Migration plugin

Cumulocity UI plugin that moves a device hierarchy from one tenant
to another. Installed into Administration (or any other
application), it adds a **Device migration** screen: a list of everything that has
been migrated, and a dialogue that starts the next one.

A migration copies managed objects, their children and their
external identities, and optionally their measurements, events and alarms. Nothing
is ever deleted from the tenant the devices came from — they are copied — and every
copy carries a marker that lets the whole run be reverted from the same screen.

The plugin holds no credentials and talks to no tenant directly. It drives the
**device migration microservice**, which must be deployed on this tenant and
subscribed to the tenants being migrated between; the platform provides the service
user for each side, so no personal credential is involved in a migration.

## Before you begin

- The **microservice is deployed** on this tenant and subscribed to it.
- The **source tenant is subscribed** to the microservice — the *Migrate from*
  dropdown offers subscribed tenants only.
- The **target tenant is subscribed** as well, or — for a tenant under a different
  parent, or on another Cumulocity instance — trusted with a certificate, which the
  dialogue sets up for you (see [Migrating somewhere this instance is not
  subscribed](#migrating-somewhere-this-instance-is-not-subscribed)).
- Whoever migrates has **`ROLE_DEVICE_MIGRATION_ADMIN`**. Without it the navigator
  entry is hidden, because every call the screen makes would be refused.

The plugin is usable in the tenant the microservice is deployed on. Opened in a
subtenant, it shows the *Trust a migration certificate* page instead — that is the
one thing a target tenant does here.

## The migration list

`Device migration` in the navigator is the screen: what has been migrated, newest
first, because the useful question on opening this is what happened last time.

- **The platform's own data grid**, so it sorts, pages, searches, refreshes and
  chooses columns exactly like every other list in Cumulocity.
- **Columns read as a sentence**: *Migration root* (the migrated object, by name and
  id), *Source tenant*, *Target tenant*, *Status*, *Timestamp*, *Duration*, *User*
  and *Run id*.
- **One status per row**, as a platform tag: *Running*, *Completed*, *Failed*,
  *Reverting*, *Reverted*, *Revert failed*. What merely qualifies it — how many
  devices are through, or a caveat about a run that was cut short — is in the tag's
  tooltip.
- **A migration and the undo that cancelled it are one row.** Expand it for the full
  run id, which relationships were followed, which device data was included and over
  what window, whether the target was reached with a certificate — and, when there
  is one, the undo: its outcome, when it ran, who ran it, its own run id and its own
  log.
- **One menu per row**, in the same place on every one of them, holding whatever
  that row offers: *Cancel* while a migration runs, *Revert*, *Migration log* and
  *Remove migration record*.
- **Migration log**: the engine's own output for that run, which is the way to
  diagnose a failure.
- **Remove migration record** per row, to tidy up a history that has served its
  purpose. It deletes the record of the run and nothing else — both tenants are
  untouched — and it takes the undos of that migration with it, because an undo
  names its migration by run id and nothing else. It is confirmed first, and the
  confirmation says which of those two things is about to happen. Records can also
  expire on their own: see *Housekeeping*.
- **Live progress.** The list re-reads itself every 30 seconds, and every second
  while something is running, with a progress bar counting devices as they are done.
  Closing the dialogue or reloading the page loses nothing, and a migration started
  from another session or from the API shows up here too.

## Starting a migration

**New migration** in the action bar opens a bottom drawer over the list. It has no
steps: source on the left, target and scope on the right, and what would move
underneath.

- **Pick the device or group with the platform's own asset selector** — the same
  search, drill-down and paging as everywhere else, except that it is reading the
  *source* tenant through the microservice. A single device can move, or a whole
  hierarchy.
- **Choose which relationships to follow**: assets, devices, additions. Ticking
  nothing migrates just the chosen object.
- **Choose which device data comes with it**: measurements, events, alarms. None by
  default — measurement volumes make that the only sensible default.
- **Bound the data with a start date**, typed as a relative window such as `-14d` or
  as an absolute date, or chosen from the calendar in the field. The syntax is
  checked as you type, against exactly what the engine accepts, so a typo is a
  message under the field rather than a failed migration.
- **See what would move before it moves.** A live estimate counts managed objects,
  external identities and each kind of device data, names the busiest device, and
  says plainly when a limit would be exceeded. Change anything that changes what
  would move and the estimate is discarded rather than left describing a different
  migration.
- **Nothing is gated.** Every choice updates the summary, and the summary lists what
  is still outstanding instead of disabling **Start migration** without saying why.
- **Both tenants are checked** before anything is read, and choosing a different
  target re-runs that check rather than carrying the old verdict over.

Nothing is read from either tenant until a migration is started.

## Undoing a migration

Every migration is marked with `c8y_DeviceMigration_<runId>`, and **Revert** deletes
everything carrying that marker in the target tenant and nothing else.

- **It is confirmed first**, and the confirmation names the root, the tenant it
  deletes from, when the migration ran, what it included, and the marker itself — so
  the promise is checkable: paste that marker into an inventory query and see the
  objects for yourself before agreeing.
- **It states the ways a result can still surprise you**: deleting a group
  propagates to its subgroups, a run that was cut short cannot be believed about what
  it created, and an earlier failed undo may already have removed some of it.
- **A migration that has already been undone is not offered again.** One that failed
  part way through still is, because running it again finds whatever the first
  attempt left.
- **An undo cannot itself be undone**, and the row says so rather than leaving it to
  a missing menu item.

Undoing is not the same as **Remove migration record**, which deletes the record
of a run and leaves what it created in place. Removing the record of a migration
nobody has undone is the point at which this screen can no longer undo it — the
objects keep their run marker, so they are still findable from the command line by
somebody who kept the run id, and the confirmation says so before it happens.

## Housekeeping

Records do not have to be removed by hand. The microservice keeps a finished job's
record for as long as its `records.retention.days` setting says, and **zero — the
default — keeps them forever**, because the record is the only account of what was
done to a customer's tenant. An operator sets it in *Administration → Settings* on
the tenant the service runs on; expired records are swept away hourly. Pruning goes
by age alone, so a migration and a much later undo of it expire separately.

## Migrating somewhere this instance is not subscribed

The target can be a tenant under a different parent, or on another Cumulocity
system. Tick *Target is not listed here*, enter its host and tenant id, and the
microservice mints a certificate for the connection. The private half never leaves
the service.

Registering the public half on the target is a page in this same plugin: **Trust a
migration certificate**, opened in the target tenant by that tenant's own
administrator. It takes the pasted certificate and registers it as trusted with
auto-registration, using that admin's own session — it needs no migration role and
does not touch the microservice. For a target that does not have this plugin
installed, the certificate can be downloaded and added under *Administration →
Device management → Trusted certificates* instead.

Once it is trusted, the connection is checked from the dialogue like any other
target. A migration to a remote target can be undone for as long as its certificate
lives; when that deadline passes, **Revert** is replaced by *Undo no longer
possible*, which explains what still would work.

## What is not migrated

Operations, device protocols, device credentials (so migrated devices do not
reconnect on their own), audit logs, binaries attached to managed objects (event
attachments *are* migrated), anything offloaded to the data lake, and applications,
tenant options or user and role assignments.

Two platform behaviours are worth handling on the target tenant before migrating
device data: **smart rules and EPL apps fire on imported data** — no processing mode
suppresses them, and the stock rules include *send SMS*, *send email* and *execute
operation* — and **retention rules will delete it**, the documented default removing
historical data after 60 days.

Existing objects in the target are never matched or updated: every migration creates
new objects, so migrating the same hierarchy twice produces two copies.#

## Plugin versions

>= 0.5.0

| | |
| --- | --- |
| Angular | 21.x |
| WebSDK | 1024.16.12 |
| node | 20.19 or newer |
| npm | 10.x |

## How to start

The plugin is packaged as a zip and installed under **Administration → Ecosystem →
Extensions**, then added to an application:

```
npm run build
```

To run it against a real tenant while developing, serve it as a remote in front of a
real Administration:

```
npm start -- --url https://{{your-tenant}}.cumulocity.com
```

Pass `--url`; the dev server's own default is a shared public system. The command
prints the URL to open, which loads the shell with this plugin as a remote:

```
🐚 Shell pointing to: http://localhost:4200/apps/administration/index.html?remotes={"devicemigration-ui":["DeviceMigrationModule"]}
```

In Module Federation terminology this plugin is the `remote` and Administration is
the `shell`; the shell loads `DeviceMigrationModule` at runtime. The shell needs to
be at least the same WebSDK version as this plugin — if the tenant runs an older
Administration, clone a current one with the devkit and point `--shell` at that.

Deploying needs no special handling:

```
npm run deploy
```

## Useful links

📘 Explore the Knowledge Base
Dive into a wealth of Cumulocity IoT tutorials and articles in our [Tech Community Knowledge Base](https://techcommunity.cumulocity.com/).

✍️ Share Your Feedback
Your input drives our innovation. If you find a bug, please create an issue in the [repository](https://github.com/Cumulocity-IoT/c8y-devicemigration).

These tools are provided as-is and without warranty or support. They do not constitute part of the Cumulocity GmbH product suite. Users are free to use, fork and modify them, subject to the license agreement. While Cumulocity GmbH welcomes contributions, we cannot guarantee to include every contribution in the master project.
