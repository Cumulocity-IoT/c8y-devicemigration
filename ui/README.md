# Cumulocity Device Migration plugin

Cumulocity UI plugin that moves a device hierarchy from one tenant
to another. Installed into Administration (or any other
application), it adds a **Device migration** screen: a list of everything that has
been migrated, and a dialogue that starts the next one.

A migration copies managed objects, their children and their
external identities, and optionally their measurements, events and alarms. The
devices and their data are never deleted from the tenant they came from — they are
copied — and every copy carries a marker that lets the whole run be reverted from
the same screen.

The plugin holds no credentials and talks to no tenant directly. It drives the
**device migration microservice**, which must be deployed on this tenant and
subscribed to the tenants being migrated between; the platform provides the service
user for each side, so no personal credential is involved in a migration.

## Before you begin

- The **microservice is deployed** on this tenant and subscribed to it.
- The **source tenant is subscribed** to the microservice — the *Source tenant*
  list offers subscribed tenants only.
- The **target tenant is subscribed** as well, or — for a tenant under a different
  parent, or on another Cumulocity instance — trusted with a certificate, which the
  dialogue sets up for you (see [Migrating somewhere this instance is not
  subscribed](#migrating-somewhere-this-instance-is-not-subscribed)).
- Whoever migrates has **`ROLE_DEVICE_MIGRATION_ADMIN`**. Without it the navigator
  entry is hidden, because every call the screen makes would be refused.

The plugin is usable in the tenant the microservice is deployed on. Opened in a
tenant the microservice is not subscribed to, it shows the *Trust a migration
certificate* page instead — that is the one thing a target tenant does here, and
it needs no migration role, so the navigator entry is shown to everybody there.

Either way there is **one entry, called Device migration**. Which of the two
screens it opens is worked out from whether the microservice answers in that
tenant, rather than being something to choose.

## The migration list

`Device migration` in the navigator is the screen: what has been migrated, newest
first, because the useful question on opening this is what happened last time.

- **The platform's own data grid**, so it sorts, pages, searches, refreshes and
  chooses columns exactly like every other list in Cumulocity.
- **Columns read as a sentence**: *Root object* (the migrated object, by name and
  id), *Source tenant*, *Target tenant*, *Status*, *Start time*, *Duration*,
  *User* and *Run id*.
- **One status per row**, as a platform tag: *Waiting*, *Running*, *Succeeded*,
  *Failed*, *Cancelled*, *Interrupted*, *Reverting*, *Reverted*, *Revert failed*.
  What merely qualifies it — how many devices are through, or a caveat about a
  run that was cut short — is in the tag's tooltip.
- **A migration and the undo that cancelled it are one row.** Expand it for the
  full run id, which relationships were followed, which device data was included
  and over what window, how the devices got credentials (and, where they collect
  their own, how many have), whether the target was reached with a certificate —
  and, when there is one, the undo: its outcome, when it ran, who ran it, its own
  run id and its own log.
- **One menu per row**, in the same place on every one of them, holding whatever
  that row offers: *Cancel* while a migration runs, *Download device
  credentials*, *Move trust anchor*, *Revert* (or *Undo no longer possible*),
  *Migration log* and *Remove migration record*.
- **Download device credentials**, for a migration that issued them: the file of
  device users and their passwords. The service keeps no other copy: it hands
  the file over once, to whoever started the migration, and only within an hour,
  so the entry disappears once it has been downloaded.
- **Move trust anchor**, for a succeeded migration whose devices kept their
  certificates: moves the trusted certificate of the authority that issued them
  from the source tenant to the target, which is what brings the fleet across.
  It is confirmed first, and the confirmation says what that costs — until the
  move finishes, no device chained to that certificate can reach either tenant.
- **Migration log**: the engine's own output for that run, which is the way to
  diagnose a failure.
- **Remove migration record** per row, to tidy up a history that has served its
  purpose. It deletes the record of the run and nothing else — both tenants are
  untouched — and it takes the undos of that migration with it, because an undo
  names its migration by run id and nothing else. It is confirmed first, and the
  confirmation says which of those two things is about to happen. Records can also
  expire on their own: see *Housekeeping*.
- **Live progress.** The list re-reads itself every 30 seconds, and every second
  while something is running; meanwhile the status tooltip says roughly which
  device the migration has reached. Closing the dialogue or reloading the page
  loses nothing, and a migration started from another session or from the API
  shows up here too.

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
- **Say how the migrated devices will authenticate.** Their credentials belong to
  the source tenant, so the dialogue asks *How will the migrated devices
  authenticate?*: *No credentials* (the default, right for a rehearsal), *New
  credentials, provided as download* (a device user per device, the passwords
  downloadable once from the list), *Devices collect new credentials* (each
  device fetches its own with the credential it already has) or *They keep their
  existing certificates* (nothing is issued; the fleet follows once its trust
  anchor is moved).
- **See what would move before it moves.** A live estimate counts managed objects,
  external identities and each kind of device data, sets the busiest device
  against the per-device limit, and says plainly when a limit would be exceeded.
  Change anything that changes what would move and the estimate is discarded
  rather than left describing a different migration.
- **Nothing is left unexplained.** Every choice is on the one screen, and beside
  **Start migration** is a list of what is still outstanding, so the button is
  never disabled without saying why.
- **Both tenants are checked** before anything is written, and choosing a
  different target re-runs that check rather than carrying the old verdict over.

**Nothing is written to either tenant until a migration is started.** Reading is
a different matter and happens as you fill the dialogue in: the picker browses
the source tenant, and the preview walks the chosen hierarchy to count what would
move.

## Undoing a migration

Every migration is marked with `c8y_DeviceMigration_<runId>`, and **Revert** deletes
everything carrying that marker in the target tenant, with the device users the
migration created for those devices, and nothing else.

- **It is confirmed first**, and the confirmation names the root, the tenant it
  deletes from, when the migration ran, what it included, and the marker itself — so
  the promise is checkable: paste that marker into an inventory query and see the
  objects for yourself before agreeing.
- **It states the ways a result can still surprise you**: deleting a group
  propagates to its subgroups, credentials the migration issued stop working, a
  certificate fleet whose trust anchor has already moved does not go back but
  registers fresh, empty objects, a run that was cut short cannot be believed
  about what it created, and an earlier failed undo may already have removed
  some of it.
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
system. Choose *Connect a different tenant…* in the *Target tenant* list, enter
its host and tenant id, and the microservice mints a certificate for the
connection. The private half never leaves the service. No credential can be
issued into such a target, so the dialogue offers only *No credentials* and *They
keep their existing certificates* for it.

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

Operations, device protocols, audit logs, binaries attached to managed objects
(event attachments *are* migrated), anything offloaded to the data lake, and
applications, tenant options or user and role assignments.

Two platform behaviours are worth handling on the target tenant before migrating
device data: **smart rules and EPL apps fire on imported data** — no processing mode
suppresses them, and the stock rules include *send SMS*, *send email* and *execute
operation* — and **retention rules will delete it**, the documented default removing
historical data after 60 days.

Existing objects in the target are never matched or updated: every migration creates
new objects, so migrating the same hierarchy twice produces two copies.

## Plugin versions

Plugin versions 0.5.0 and later are built with:

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
Dive into a wealth of Cumulocity tutorials and articles in the [Cumulocity Community](https://community.cumulocity.com/).

✍️ Share Your Feedback
Your input drives our innovation. If you find a bug, please create an issue in the [repository](https://github.com/Cumulocity-IoT/c8y-devicemigration).

These tools are provided as-is and without warranty or support. They do not constitute part of the Cumulocity product suite. Users are free to use, fork and modify them, subject to the license agreement. While Cumulocity GmbH welcomes contributions, we cannot guarantee to include every contribution in the master project.
