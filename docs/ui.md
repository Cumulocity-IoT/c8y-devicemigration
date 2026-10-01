# The user interface

An Angular plugin for Cumulocity, so a migration can be performed by somebody who
has not read any of the rest of this repository.

It drives [the microservice](microservice.md), which means the microservice has to
be deployed and subscribed first — the UI holds no credentials of its own and
talks to no tenant directly.

> **Status: increments B, C and D.** The plugin, the generated API client, the
> list of past migrations with revert, the dialogue that starts a new one, and
> live progress. None of it has been opened in a browser against a real tenant
> yet. The design it was built to is in the
> [UI design](https://claude.ai/code/artifact/42204d1b-d968-4030-b1e9-aeca0cb8c8cd).

## Installing it

```sh
make ui-package        # -> build/devicemigration-ui-<version>.zip
```

Install that zip under **Administration → Ecosystem → Extensions**, then add it to
an application. Administration is where this is documented and tested, because
migrating tenants is an operator task and the microservice only accepts
parent-tenant administrators anyway; nothing stops it being added to Device
Management or a custom application instead.

Then grant `ROLE_DEVICE_MIGRATION_ADMIN` to whoever should use it. Where the
microservice is subscribed the navigator entry is hidden without that role,
because every call the UI makes would be refused. Where it is **not** subscribed
the same entry is shown to everybody, because what it opens there needs no
migration role at all — see [one entry, two screens](#one-entry-two-screens).

**What that screen shows about the plugin is [ui/README.md](../ui/README.md).** The
devkit copies it into the zip as `README.md`, and again as
`DeviceMigrationModule/README.md` because the export names it in `readmePath`, so
both the extension and the plugin inside it have something to say for themselves.
It is written for whoever is deciding whether to install this — what the screens
do, what has to be true first, what is not migrated — and not for whoever is
working on it, which is what this document is for.

## Who can use it, and where

Only in the tenant the microservice is deployed on. A session belongs to one
tenant, and the microservice refuses anybody who is not a user of its own — so
opening the plugin in a subtenant produces a refusal on every screen, naming the
tenant that may use it. That is the microservice's rule, not the UI's, and the UI
shows its words rather than paraphrasing them.

## What it is built on

| | |
| --- | --- |
| `src/api/types.ts` | **generated** from [internal/service/openapi.yaml](../internal/service/openapi.yaml). Never edited by hand |
| `src/api/client.ts` | the API as plain TypeScript over a fetch-like callable |
| `src/api/migration.service.ts` | three lines, handing the client Cumulocity's own `FetchClient` |
| `src/progress.ts` | turning a job into something a screen can show |
| `src/migrate/flow.ts` | the rules: what invalidates what, and what is sent |
| `src/migrate/steps.ts` | what is still missing before a migration can start |
| `src/api/platform-client.ts` | a Cumulocity client that reads another tenant through the proxy |
| `src/migrate/migrate.component.*` | the dialogue |
| `src/migrate/root-picker.component.ts` | the platform's asset selector, pointed at the source tenant |
| `src/migrate/selection.ts` | what that selector's `onSelected` actually means |
| `src/migrate/window.ts` | which start dates the engine will accept, mirrored |
| `src/history/rows.ts` | what the list shows, and what can be done to each row |
| `src/history/status-cell.component.ts` | the status column: the platform's tag, one status, the rest in its tooltip |
| `src/history/timestamp-cell.component.ts` | the timestamp column: locale formatting over a chronologically sortable value |
| `src/history/output.component.ts` | the engine's own output for one job, in a dialogue |
| `src/history/revert-confirm.component.ts` | asking before an undo, and saying what it would delete |
| `src/tenants.ts` | naming a tenant: the company name or the host, always with the id |
| `src/history/history.component.*` | the list, which is the screen |

Two things are deliberate about that shape.

**The types are generated, and staleness fails the build.** `make check` runs
`ui-types-freshness`, which regenerates from the document and refuses to pass if
the committed file differs — the same treatment the docopt parsers and the
command reference already get. It earned its place immediately: with `default:`
values in the document, the generator made `childTypes` a *required* field, so
calling the API the way the API actually allows would not have compiled.

**The logic is not in Angular.** Everything worth testing — which failure means
what, whether a progress line can be believed, whether a run can still be undone,
what invalidates an estimate — is a pure function or a plain class, so it is
tested without a browser or a dependency injector. The Angular pieces are wiring.

## The list, and one dialogue

**The list is the screen.** Migrations are occasional, and the useful question on
opening this is what happened last time — not what to do next. So `/device-migration`
is the history: migrations and reverts together, newest first, with the run id,
who asked, and either **Cancel** or **Revert**. **New migration** opens the
dialogue over it, from the shell's own **action bar** rather than a button placed
over the list — that is where Cumulocity puts a screen's actions, so it is where
somebody looks for them. It is registered inside the migrations branch of the
template, so a target tenant, which has no microservice to start a migration
with, does not offer the action at all.

The table is **the platform's own data grid**, given rows rather than a
`serverSideDataCallback` — which is what makes it sort, page and search through
them itself, and brings its own refresh button and column chooser. Sorting
defaults to **Timestamp, newest first**, which is the useful order for a list of
things that happened.

**The actions are the one part of the grid this screen does not use.** Cancel,
Revert, Migration log and Remove migration record were its **row actions** to
begin with, which is the right thing to reach for and was fine while there were
two of them: the grid renders two as icons in the row, and three or more as one
icon plus a `⋮` menu holding the rest. That threshold is written into its template —
`| slice: 0 : (length > 2 ? 1 : 2)` — and there is no input for it. So the third
action made the *affordance itself* depend on what had happened to a migration:
a run nobody had undone wore a menu, the reverted row under it wore two buttons,
and *Remove migration record* was in a different place on adjacent rows.

There are now six of them — Cancel, Download device credentials, Move trust
anchor, Revert, Migration log and Remove migration record — and no row offers all
six, so under the grid's own rule the layout would differ on almost every line.

The last column is therefore ours: **one `⋮` menu, on every row, holding whatever
that row offers** — the grid's own markup, classes and placement, rendered by
[row-actions.component.ts](../ui/src/history/row-actions.component.ts) for all of
them rather than for the crowded ones. What goes in it is
[`actionsFor`](../ui/src/history/rows.ts), a tested function, in a fixed order:
what the run is still doing, then what can be done to what it created, then
reading about it, then forgetting it. The column is named `rowActions` and not
`actions`, because the grid switches on a column's name and has a branch of its
own for that one.

**Pagination is the grid's, not the service's**, and that distinction matters:
`GET /migrations` and `GET /reverts` have no paging parameters and return every
record in the owner tenant, so the browser holds all of them and the grid pages
what it was given. For hundreds of records — years of weekly migrations — that is
fine. For tens of thousands it would not be, and the answer then is either the
`records.retention.days` setting, which bounds how long records are kept, or a
paged listing in the service. There is none today, and this is the honest
description of that.

**Source and target are columns of their own.** They used to be a phrase in
*What*; a column can be sorted and read down a page, and a phrase cannot. Each
shows the tenant's name and id — or its host and id where tenant management is
not readable — and *Migration root* shows the migrated object by name as well as
by id, from `rootName` on the record. It is named for what it holds: the root of
the hierarchy that moved, not a summary of the job.

**The columns read as a sentence, and Status is fourth.** *Migration root*,
*Source tenant*, *Target tenant*, then *Status*, *Timestamp*, *Duration*, *User*
and *Run id*. A row says "this hierarchy, from here to there — and it worked":
the outcome qualifies the sentence, so it follows it instead of leading it.

**Three columns are narrowed to what they can actually hold.** The grid's default
track for short text is `minmax(80px, 1.67fr)`, which hands a fixed-length value
the same room as a tenant name that can run on. *Status* holds one glyph, so it
is `Icon`, whose own track is `max-content`; *Timestamp* is a date and a time and
no more, so `minmax(120px, 0.8fr)`; *Duration* is `Numeric` — the narrow ratio,
and figures that line up when read down the page — at `minmax(80px, 0.5fr)`.

That last one needed the values shortened before the column could shrink. A
duration used to read *took 1 h 30 min* when finished and *running for 1 h 30
min* while going, and the second was the widest thing the column could hold. Both
prefixes said something the screen already said — the header says Duration, and
the status column says whether it is still running — so the value is now the bare
span either way, and only the number moves.

**The list is ordered by Timestamp, newest first — on the raw value, not the
formatted one.** This is worth writing down because getting it wrong looks like
it works. The grid sorts and searches on whatever a column's `path` points at,
and nothing else: `doClientSideSorting` reads `get(item, path)` and ignores
`sortingConfig`, which only ever reaches a server-side query. Pointed at the
locale-formatted date, it sorted a *string* — so `01/09/26` came before
`28/08/26` and "newest first" quietly meant "alphabetically first". The column's
path is therefore the ISO timestamp the service recorded, which sorts
chronologically because it is UTC and fixed-width, and
[timestamp-cell.component.ts](../ui/src/history/timestamp-cell.component.ts)
renders the locale-formatted text in its place. The one cost is that the search
box matches a timestamp in its ISO form rather than the reader's.

**Status is one of the platform's tags, and it says one thing.** `.tag` is
Cumulocity's own tag component — the stylesheet marks the older `.label`
deprecated in its favour — so *Completed* is the light green on dark green the
platform means by success, and this list looks like the screens around it.

The important half is *one* status. A migration that had been undone used to read
"Succeeded — already undone": two facts joined by a dash that the reader had to
combine, and a wrong one, because the objects of a migration that was reverted are
not in the target tenant any more. The row now says **Reverted**. The full set is
the job's own status until an undo exists, and then the undo's: **Reverting**
while one is going, **Reverted** when one succeeded, and **Revert failed** when
one did not — that last said out loud rather than left in the expansion, because
a half-deleted migration reading *Succeeded* would be the most misleading thing
on the screen. What merely *qualifies* the status — device progress, the caveat
about what cannot be believed — is the tag's tooltip. The decision is
[`outcome`](../ui/src/history/rows.ts) with tests;
[status-cell.component.ts](../ui/src/history/status-cell.component.ts) is wiring.

**A migration and the undo that cancelled it are one row.** The undo used to be a
row of its own, elsewhere in the list, naming its migration by run id and nothing
else — so learning what had been undone meant matching ids by eye, and the two
rows need not even be adjacent. Now every migration is expandable
(`expandableRows="SYNC"`, with the panel projected through the grid's own
`[c8yExpandableRow]`), and the expansion carries everything the undo's row
carried: its outcome as a tag, when it ran, who ran it, how long it took, its own
run id and its own **Migration log**. Including when it failed — which is the
case that matters most, and the reason the undo is not simply folded away into
the migration's status. Several undos of one migration are listed newest first,
which happens when a first attempt failed and a second finished the job.

**Every row expands, so the expansion is about the migration, not only its undo.**
The chevron cannot be hidden per row: the grid builds its own `expander` column
whenever `expandableRows` is on, `uniqBy(columns, 'name')` keeps the built-in one
over any replacement, and there is no row-class or `trackBy` hook to reach it with
CSS. Rather than leave a chevron that sometimes opens onto nothing, the panel
carries what no column does — the full run id, which relationships were followed,
which device data was included and over what window, how the devices got
credentials, and whether the target was reached with a certificate. The undo is
the headline of that panel when there is one, and a migration nobody undid still
has something to say. The defaults are why
[`scopeOf`](../ui/src/history/rows.ts) is a tested function rather than a
template reading the request: an omitted `childTypes` means `asset,device`, while
an omitted `dataTypes` really does mean none.

**The credentials line is said even when it says "None"**, which is the whole
reason it exists. *Download device credentials* is offered only while the file is
still there to collect — the service hands it over once and forgets it — so as
soon as somebody had downloaded it, a migration that provisioned a device user
per device was indistinguishable from one that never did. The count comes from
`credentialsIssued` on the job record rather than from the service's memory, so
it survives the download and a restart.

The other two answers say so as well. A migration whose devices **collect their
own** carries a live count beside it —
[`CollectionProgressComponent`](../ui/src/history/collection-progress.component.ts)
reads two numbers through the inventory proxy, objects carrying this run's marker
and those carrying the issued fragment as well. That is the only signal there is:
the job finishes long before the fleet has moved, so without it a migration whose
devices never managed to reach the endpoint looks exactly like one that is
working. It counts on expansion rather than on load, which is what a component
inside the expansion template buys — `ngOnInit` fires when somebody opens a row,
and the list itself re-reads every few seconds and must not carry two queries per
row along with it.

A **certificate fleet** reads as unfinished business, because it is: the objects
are across and the devices are not, until the trust anchor moves.

**An expanded row survives the refresh.** The grid remembers which rows are open
in a `Map` keyed by the **row object itself**, so handing it a fresh array every
few seconds silently collapsed whatever the reader had open.
[`keepingIdentity`](../ui/src/history/rows.ts) hands back the previous object for
any row whose content is unchanged, so those keys still match; only a row that
genuinely changed becomes a new object, and only that row closes — which is
defensible, because something about it moved on. Comparing by content and not by
id is the part that matters: reusing the old object for a row whose status had
advanced would show the old status for ever.

**An undo whose migration is not in the listing keeps a row of its own** — a
revert run from the command line against a run id whose migration has aged out of
the records is a real thing, and a job with no parent to sit under must not become
a job that is nowhere.

**The expansion is laid out as definition lists on a two-column grid.** Labels on
one axis, values on the other. It was a flex row of a tag, a date and a username
to begin with, which lined up on none of them: `.tag` carries
`align-self: flex-start` and a smaller font than the text beside it, so it rode
up above its own line.

**A migration that has already been undone is not offered for undoing again.** A
successful revert for its run id is what says so; one that failed or was cut
short leaves the offer standing, because a revert deletes what carries the marker
and running it again finds whatever the first one left — which the status says as
*Revert failed*, with the half-deleted caveat in its tooltip.

**An undo is confirmed before it runs.** It is the one destructive thing this
screen does, and it used to happen on a single click of a menu item sitting two
pixels from *Migration log*. The dialogue names the root, the tenant it deletes
from, when the migration ran, what it included, and the run marker itself —
`c8y_DeviceMigration_<runId>` — because that is the part that makes the promise
checkable: it is what somebody pastes into an inventory query to see the objects
for themselves before agreeing.

It states no count, deliberately. The service keeps no plan to ask for: there is
no dry run, and the record carries no tally of what the migration created, so a
number on that screen would be a guess presented as a fact. It states the rule
instead — everything carrying that marker, nothing else, objects already gone
skipped — and then the three ways the result can still surprise somebody, from
[`revertWarnings`](../ui/src/history/rows.ts): a group deletion propagates to
subgroups and can take objects this migration never created; a run that was cut
short cannot be believed about what it did; and an earlier undo that failed may
already have removed some of it. Like the migration dialogue, it is bound to
`c8y-modal`'s `close` input rather than its `onClose` output, so a refusal leaves
the dialogue open with the service's own words instead of vanishing with them.

**A migration to a remote target can be undone for as long as its certificate
lives.** Undoing means presenting the *same* certificate the migration used — a
device token deletes only what it owns, and the objects are owned by the identity
that created them, so a fresh certificate would be a different device user owning
nothing. The service therefore carries the migration's `remoteTargetId` onto the
revert and resolves the client through the same seam a migration does.

That makes the certificate's deadline the thing that decides whether the action is
offered, so the list reads it: `GET /remote-targets` says what this instance still
holds and until when, and
[`liveRemoteTargets`](../ui/src/history/rows.ts) turns that into the set of ids
still good *now*. The button has to stop being offered at the moment the service
stops accepting it — a screen that waits to be told by a refusal is offering
something that cannot work.

**When it has expired, the action changes rather than disappears.** A muted *Undo
no longer possible* takes its place, and clicking it explains why and what still
would work — the command line against that tenant, or deleting the marked objects
in the target itself. This is a separate `revertExpired` rule rather than a branch
of `revertable`, because they are different sentences: not-revertable covers runs
that were never undoable (a revert, an unmarked run, one already undone), while
this one *was*, until a deadline passed. It is a menu entry of its own wearing a
muted icon rather than a disabled *Revert*, because an entry that only explains
should not read as an offer.

**Move trust anchor is the second half of a certificate-fleet migration**, and it
is offered from the row rather than from a screen of its own. It appears for a
succeeded migration that asked for `certificateFleet` and, unlike every other
action here, it does **not** go away once used: a cutover creates no job, so
nothing in the list can know it has happened. The dialogue answers that instead by
reading the source tenant's truststore every time it opens — a certificate that has
already moved is simply no longer there. The cost of that choice is stated in
[the microservice docs](microservice.md#moving-a-trust-anchor-for-a-certificate-fleet):
a CA that needs moving with no migration in the list, or whose migration has aged
out of the records, is reachable only through the API.

[`CutoverComponent`](../ui/src/history/cutover.component.ts) is built on the undo
confirmation's shape — `c8y-modal` with `[close]` bound, so the dialogue performs
the operation itself and a refusal stays on screen carrying the service's own
words. That matters more here than anywhere: a refused cutover may have left the
source certificate restored, and the message is the only place that says so. It
lists unmovable certificates with their reason rather than hiding them, because
*this fleet needs re-enrolling* is the answer somebody needs, not an empty list
that reads as a bug; and it shows the service's coverage warning, which admits
the device count cannot be determined rather than approximating it.

**Auto-registration is a checkbox, set to the source tenant's own answer.** It
decides which devices the certificate may bring into the target — with it on,
anything presenting a chain to that authority registers itself there, including
devices no migration covered. Starting it where the source has it means moving
the certificate changes nothing by itself, and turning it on is a decision
somebody takes; the service used to force it on, and a tenant that had
deliberately trusted a CA without it got it switched on with nothing saying so. A
line under the box says what the current setting means, and which of the three
things it is: unchanged, widened, or off.

On success the dialogue shows `autoRegistrationEnabled` **read back from the
target**, and distinguishes the two things a `false` can mean — what was asked
for, or a target that accepted the certificate while dropping the setting. Only
the second is an alarm; the first is the move working as chosen.

**An undo cannot itself be undone, and the expansion says so** rather than
leaving it to the absence of a menu entry. A row's menu says what can be done,
not what cannot: the reasons belong in the row's content, where there is room for
a sentence.

Above the list is an **introduction that stays there**: what this moves, and the
two facts that reassure a first-time user — nothing is read until a migration is
started, and nothing is ever deleted from the tenant the devices came from. It is
not behind a help link because a migration is rare enough that almost everybody
doing one is doing it for the first time.

**One icon, in all four places the feature appears**: the navigator entry, that
introduction, the empty state, and the microservice's own entry in Administration
and the API documentation. Two icons for one feature reads as two features.

Three of the four are
[`DeviceMigrationIconComponent`](../ui/src/icon/device-migration-icon.component.ts),
which draws the artwork inline. The navigator reaches it through a wrapper named
`c8y-navigator-icon`, and that name is the whole point of the wrapper: a node's
icon is laid out by a **type** selector — `c8y-navigator-icon { padding: 0 0 0
16px; margin-right: 16px; flex-grow: 0; }` — and Angular names a dynamically
created host after its component's selector, so an icon component called anything
else matches none of it and the entry sits flush against the rail with its label
crowding it. That is not a preference — a `dlt-c8y-icon-*`
class renders its SVG as a **CSS mask**, colouring `::before` from `dlt/<name>.svg`
and `::after` from `dlt-filled/<name>.svg` out of theme variables, and a mask
keeps only the alpha. A two-colour icon therefore cannot travel as one file
through that mechanism, which is why this one is two layers and why the plugin
draws them rather than masking them. The dark layer is `currentColor`, so
`text-muted` mutes it exactly as it mutes a real duocolor icon.

The empty state is the awkward one. `c8y-ui-empty-state` renders
`<i [c8yIcon]="icon">` unconditionally, takes an icon **name** and nothing else,
and puts its content slot in the text block below the title — so the icon cannot
be replaced through it. `.c8y-empty-state` is a plain class in `@c8y/style`,
so the markup is reproduced with this icon in place of a named one and the layout
stays the platform's. The two sizing rules that had to come with it are the
platform's own values, copied because its selectors match its icon classes and a
custom element carries neither.

The fourth is the microservice manifest, whose `icon.class` is a CSS class applied
verbatim and cannot reference a file at all. It reads `c8y-icon-device-migration`,
which has to exist platform-side; until it does, that one entry shows nothing while
the three the plugin draws are already right.

When that class ships, the three could go through the platform instead:
`c8yIcon="c8y-device-migration"` resolves to it, because `IconDirective` rewrites a
leading `c8y-` to `c8y-icon-`. That is the version to prefer once it is possible —
a real icon brings the theming and dark-mode handling that hand-drawn paths have to
reproduce.

**The prerequisites are in the dialogue, not the introduction.** What has to be
true before a migration can run — the source subscribed, the target subscribed or
trusted with a certificate — is stated at the top of **New migration** instead,
because that is where somebody acts on it: the source dropdown offers only
subscribed tenants, and the certificate is set up a few fields below. On the list
page it was advice with nothing to do about it.

Whether a run can be undone is decided in one place and pinned by tests, because
three separate things make it impossible: a revert creates nothing, so there is
nothing to undo about one; a run started with `noFlag` marked nothing, so only its
mapping file could find those objects and that file did not outlive the container;
and a run still going has not finished creating what an undo would delete.

**A row can be removed from the list, which is not the same as undoing it.**
*Remove migration record* deletes the service's record of a run —
`DELETE /migrations/{id}` — and nothing else: both tenants are untouched, and
whatever the migration created stays where it is. Two destructive actions now sit
in one menu and they destroy opposite things, so the confirmation leads with the
difference rather than with "this cannot be undone", which is true of either.

**Deleting a migration deletes the undos of it.** The service does that, and it
has to: a revert record names its migration by run id and nothing else, so one
left behind is a row that can say only *undo of 20260101T…*. The dialogue says
how many are going before it happens, from
[`undosDeleted`](../ui/src/history/rows.ts). An undo listed on its own — one whose
migration has aged out — deletes only itself, through `DELETE /reverts/{id}`; the
row knows which it is, and the confirmation is worded accordingly.

**The warning that matters is about a migration nobody has undone.** Its record is
what *Revert* reads, so deleting it is the moment undoing stops being something
this screen can do: the objects keep their run marker and are still findable, by
somebody who kept the run id, from the command line. That sentence is
[`deleteWarnings`](../ui/src/history/rows.ts) and it appears only on the rows it
is true of — a migration already reverted has nothing left to strand, and says
nothing.

**A run that is still going cannot be removed**, its own or one of its undos:
the job is still writing the record this would delete. The action is hidden by
[`deletable`](../ui/src/history/rows.ts) and refused by the service with a 409 for
the same reason, so a job that starts between the menu opening and the button
being pressed leaves the dialogue open carrying the service's own words instead of
half a deletion.

**The engine's own log is offered per row**, on the rows that have any, as
*Migration log* — which is what it is; *Output* named neither whose nor of what.
It is the only account of what a run actually did and the only way to diagnose a
failure, and since the dialogue is gone by the time a job runs, the list is where
it has to be. The dialogue it opens carries the same name, so the action and the
window agree.

**A running migration is followed in the list, not in the dialogue.** The list
reads itself again every **30 seconds** normally and every **second** while
something is running — so closing the dialogue, or reloading the page, loses
nothing. It never stops entirely, because a migration can be started from another
session or from `c8y api` and a list that only followed its own work would go
quietly out of date.

The gap between the two numbers is the whole point. Every read costs the service
three calls and a round trip to the platform to verify the caller, so idling is
deliberately slow; but a migration is the one thing on this screen worth
watching, and at ten seconds it did not look like anything was happening — the
device counter and the progress bar moved in jumps, which reads as a stuck job
rather than a working one. The fast cadence is bounded in two ways: it lasts only
as long as a job does, and the next read is scheduled in `load`'s `finally`, so it
is a gap *between* reads rather than a rate. A slow answer stretches the cycle
instead of queueing another request behind it.

It also depends on [`keepingIdentity`](../ui/src/history/rows.ts) to be bearable:
reading every second while replacing every row object would collapse an expanded
row a second after it was opened.

**Starting one is a bottom drawer, not a modal.** `BottomDrawerService.openDrawer`
with the platform's own `c8y-bottom-drawer` — the component Cumulocity provides
for exactly this, a wide scrolling workspace over the page you came from. It
arrives at the right size by definition, which is the point: the modal it
replaced carried a hand-written width, a mobile breakpoint and a fixed body
height, all arithmetic against the viewport, and all of it unencapsulated because
the element being sized (`.modal-dialog`) was an ancestor the component could not
otherwise reach. Every one of those rules is gone, and so is
`ViewEncapsulation.None`.

The drawer hosts its content in a full-height flex column, so the dialogue is the
three parts the platform's own drawers use: a `card-header` that stays, an
`inner-scroll flex-grow` body that takes what is left and scrolls inside it, and a
`card-footer` holding Cancel and Start. The component carries `d-contents` so its
children become that column's own items. It is opened with `disableClickOutside`,
because there is half-filled work in it and a stray click on the page behind must
not throw that away.

**It settles a promise rather than emitting an event.** `result` resolves with the
new job's id and rejects when the operator backs out — the convention the
platform's drawers use, and what the list awaits to decide whether to reload. The
`started` output it used to carry said the same thing twice and is gone.

**The dialogue has no steps.** Source on the left, target and scope on the right,
what would move underneath. There is room for all of it, and hiding the target
picker while somebody chooses a source only makes them click. Nothing is gated:
every choice updates the summary, and the summary lists what is still outstanding
rather than disabling **Start migration** with no explanation.

**The start date is checked where it is typed.** `-14d` and the absolute forms
the engine accepts are mirrored in [window.ts](../ui/src/migrate/window.ts),
deliberately no wider than
[internal/export/window.go](../internal/export/window.go) — JavaScript's own
`Date.parse` takes things Go refuses, and accepting them here would only move the
refusal. A typo is a message under the field and a blocked **Start**, not a round
trip and an alert at the top saying "That cannot be done". The service stays the
authority: a refusal from it that names the date is shown on the field too.

It is **one field with two ways to fill it in**: type the syntax, or open the
calendar in the button beside it — `c8y-date-time-picker` in a dropdown, the same
arrangement the platform's own time-context control uses. The text is the value
and the picker writes into it, so there is nowhere for the two to disagree. The
picker shows nothing while the field holds a relative window, because `-14d` is a
rule rather than a moment; it is rebuilt when that happens, since its `writeValue`
ignores an empty string and could not otherwise be cleared. The help text says
which form to prefer.

**The estimate's columns are declared, not inferred.** A table is laid out from
whatever is in it, so a row reading *not included* where another has a figure made
every column shift sideways as the numbers arrived. The widths are in a
`colgroup` with `table-layout: fixed`, and the value column stays numeric — a kind
that is not included says so in the notes column beside it.

**The dialogue does not change size.** The drawer's own geometry sees to that
now: it is the same panel whatever is in it, and the body scrolls. A dialogue that
grows while somebody is reading it moves the button they were about to press,
which is what the fixed height used to be defending against by hand.

Inside it, the same rule applies to the parts that would otherwise jump: the
estimate has a row for every data kind whether or not it is ticked — reading
*not included* until it is — with dashes until the counts arrive, and the From
field is always there, disabled until there is device data for it to apply to.

**The credentials question has one answer, so it is radios.** *How will the
migrated devices authenticate?* — nothing, a device user each downloaded once,
each device collecting its own, or they keep their own certificates. The service
refuses any pair of them, so a set of checkboxes would invite a combination that
is rejected on submit. One help line follows the answer rather than restating the
label: what is handed over and once, who has to ask, and that a certificate fleet
is only half migrated until its anchor moves.

**Two of those answers disappear for a remote target**, hidden rather than
disabled, with a line saying why. Such a target is reached with a device access
token whose effective roles are the device ones; bulk registration needs
`ROLE_DEVICE_CONTROL_ADMIN` and the token does not hold it, so neither issuing
answer is possible there. The certificate answer stays, because it registers
nothing — it only names an owner on objects that same token already owns.

**Choosing the first answer asks the service whether it is available**, because
that one depends on what the target tenant already has: a device there under one
of the external identities the migration would register cannot be given a new
device user without losing the credential it uses today, so the service refuses
such a migration outright. Choosing the answer runs a pre-flight with the
hierarchy; a refusal is shown under the radios as the list of devices involved,
named on both sides, and Start stays disabled until another answer is chosen. The
check is re-run whenever anything it depends on changes — the hierarchy, the
target, or which relationships are followed — and a refusal already found is kept
until the new answer replaces it, because a stale block is recoverable and a
stale pass is not.

**The second answer has limits the dialogue does not check**, because they are
about the fleet rather than about the two tenants and nothing here can see them.
Letting devices collect their own credentials only works for devices that have a
device user of their own: a child device behind a gateway authenticates as the
gateway and has none, so it is migrated like any other and arrives without
credentials. And the service identifies a collecting device by its username,
which places trust in the source tenant's user administration while credentials
are outstanding. Both are set out in
[the microservice docs](microservice.md#limitations-of-this-route); they are
worth reading against the fleet before choosing this route, because nothing
reports them afterwards.

The summary's credentials row uses the same number for all three answers and
labels it differently: users created, devices able to collect, devices owning
their twins. Its note — *of N objects; the rest are groups or have no usable
external identity* — is load-bearing for a certificate fleet in a way it is not
elsewhere: those devices do not merely arrive without a password, they arrive
unable to write their own twin, and the note says so.

**The relationship choice is folded away until it is asked for.** It answers a
question most people do not have — the default follows assets and devices, which
is what almost every migration wants — so what is on screen is a sentence saying
what would happen, with *(Configure hierarchy migration)* beside it opening the
checkboxes and *(Hide)* folding them back. The checkboxes carry no heading of
their own: the sentence immediately above them is the heading, and a second one
would say the same thing twice.

The sentence **stays on screen while they are open**, and is **derived from the
choices** rather than fixed, so it reads back what each tick just did and folding
the controls away never hides a choice somebody has just made. It is also why
there is no separate hint about unticking everything: with nothing ticked the
sentence says *only this object is migrated, without any children*, which is the
same thing the hint used to say and is now said in the one place that is always
true. Unticking everything and asking for no children are the same request, which
is how it is built as well.

It is assembled in [`hierarchy.ts`](../ui/src/migrate/hierarchy.ts) from whole
sentences rather than fragments, so a translator gets grammar rather than a list
to reassemble, and so the wording is testable without a browser.

**Relationships and device data are both multi-select.** Checkboxes over the
API's own values, `asset`/`device`/`addition` and
`measurements`/`events`/`alarms`, sent in the order the API documents them.
Unticking every relationship sends `noChildren`, which is not the same as sending
nothing: the API's default is `asset,device`, so silence would have migrated more
than was asked for.

**The target can be a tenant this instance is not subscribed to.** A checkbox
under the target dropdown opens a panel for a remote target — a different parent,
or a different platform. The operator enters its host and tenant, the service
mints a certificate and shows the **public CA** to register on that target, and
once it is registered the connection is checked like any other preflight. The
private half of the certificate never leaves the service.

Registering that CA on the target is the friction the platform's own screen
imposes — it wants a file, and what the source shows is text. So the plugin
carries a **Trust a migration certificate** page
([src/trust](../ui/src/trust)): opened in the target tenant, by that tenant's own
admin, it takes the pasted certificate and registers it as trusted with
auto-registration, using the admin's own session and touching the microservice
not at all — it is the platform's own trusted-certificate call. It is deliberately
**not** gated on the migration role, because the person using it is a
target-tenant admin who does not hold it. For a target without the plugin, the
source panel offers the certificate as a **download** for the manual
Administration upload. The mechanism behind the connection is in
[the microservice docs](microservice.md#remote-targets).

### One entry, two screens

There is **one navigator entry, always called Device migration**, and one route
behind it. Which of the plugin's two screens that route opens is decided by the
tenant rather than by the reader:

| | |
| --- | --- |
| the microservice **is** subscribed | the migration list — the feature proper |
| it is **not** | the trust-a-certificate page, which is the one useful thing such a tenant can do |

The navigator used to carry both as separate entries. That put the plugin's
internals in front of people with no way to read them: *Trust a migration
certificate* exists as a separate screen only because a target tenant has no
microservice, which is not something somebody browsing a menu should have to
know. What they want is the feature, and the feature has one name.

The question behind it is asked once, of the platform, by
[`serviceReachable`](../ui/src/api/availability.ts): a `GET` on the microservice's
`/health`. That route is chosen because it is the only one the service answers
**without credentials**, so a positive answer means *this tenant is subscribed and
the service is up* and says nothing about who is asking. Any other route would
conflate an unsubscribed tenant with a caller lacking a role — and those two need
opposite screens. A network failure counts as unreachable, because the screen that
needs no microservice is the one that can still do something.

That same answer decides the entry's **permission**, which is why the navigator
node is built by a factory rather than written as a literal: gated on the
migration role where the entry leads to the migration list, ungated where it leads
to the certificate page. [`AvailabilityService`](../ui/src/api/availability.service.ts)
caches the promise rather than the value, so the navigator and the route share one
request and cannot disagree.

The route renders **neither** screen until the answer arrives. Rendering one and
swapping it would start the migration list loading jobs from a service the tenant
may not have, and would show a reader a screen that is then replaced by a
different one.

**The device picker is the platform's own asset selector.** `c8y-asset-selector`,
with its search, its drill-down, its paging and the look every other picker in
Cumulocity has — because it is the same component. Nothing about it is patched or
reimplemented here.

Making it read *another tenant* is the whole trick, and it is worth understanding
because it is where this would break. The selector reads through
`InventoryService`, which talks to the tenant the browser is signed in to. So
[root-picker.component.ts](../ui/src/migrate/root-picker.component.ts) provides
its own `InventoryService` built on a client that sends every request to the
microservice's [inventory proxy](microservice.md#the-inventory-proxy) for the
source tenant, and provides component-scoped copies of every other service in
that chain — `InventorySearchService`, `AssetNodeService`, `GroupNodeService`,
`DeviceGroupService` — so each of them resolves the proxied client rather than the
application's own.

One more thing about that component is worth writing down, because it looked
exactly like a broken picker: `onSelected` does not emit the selected object. It
emits an envelope — `{ items, change }` — where `items` is the selection *after*
the change, one object in single-select and an array in multi. Reading it as the
object meant every click was silently dropped and the dialogue went on asking for
a device. The reading of it lives in
[selection.ts](../ui/src/migrate/selection.ts) with tests, and clearing the
selection needs no special case: it arrives as a selection with nothing in it.

The completeness of that provider list is what makes it safe, and an incomplete
one fails in the worst way available: a service resolved from the root injector
reads the tenant the browser is signed in to and confidently lists the wrong
devices. Two things guard against it. The proxied client refuses to send anything
at all until it has been told which tenant to read, so a missed wiring is an
error rather than a wrong answer. And the picker is keyed on the tenant, so
choosing a different source destroys it and builds a new one — the selector and
its services cache what they have fetched, and a cache filled from one tenant
must never be shown for another.

This replaced a `c8y-data-grid` fed from our own inventory endpoints. That
worked, and it had neither real paging nor real search — both of which the
selector has, along with the rest of what a picker eventually needs. Those
endpoints are still there and still documented: they are a reasonable way to
script the same question, and they are what any client that is not a Cumulocity
UI would use.

Two rules are worth stating because they are easy to get wrong and were tested
first:

- **Changing anything that changes what would move voids the estimate.** A new
  data window, different child types, a different root. An estimate that
  describes a different migration from the one about to start is worse than no
  estimate.
- **A new target has to prove itself again.** Choosing a different target
  re-runs the pre-flight rather than carrying the old verdict over, so nobody
  starts a migration on the strength of a check that happened for a different
  tenant.

The progress bar counts the engine's own `Device N of M` lines. If that wording
changes the bar disappears and the log does not — a bar inventing a number is
worse than no bar.

It is shown **only while a job is running**, which is not the same as "whenever a
line can be found": the line stays in the log after the job is over, and reading
it from there left finished migrations wearing a full bar for good. A finished job
has an outcome, not progress. What every row does carry is how long it has been
going, or how long it took.

### The credentials download

A migration can provision a device user per device — the dialogue's **Device
credentials** box — and the passwords are then downloadable from the migration's
row, once.

Once is not a figure of speech. The service holds the file in memory and drops it
as it serves it, so the action disappears after the download and there is no way
to ask again: the row's offer is driven by `credentialsAvailable`, which the
service computes from its own memory rather than from the job record. The
dialogue says so before anything runs, because a migration whose credentials
nobody collects leaves devices registered with passwords no one has.

**The offer appears only on your own migrations.** Because the file is handed
over once, collecting a colleague's would leave them without it, so the service
hands the credentials only to the caller who started that migration — and reports
`credentialsAvailable` as false to everybody else, so the action is not shown
rather than shown and refused. Other people's migrations are still listed in
full; it is only the download that is theirs.

The download fetches the body with the platform's own client and only then hands
it to the browser as a file, rather than pointing a link at the endpoint. A
navigation the session might redirect, or that a popup blocker might swallow,
would spend the one read on nothing. The object URL is revoked in a `finally`, so
the blob does not keep a list of working passwords reachable for the life of the
page.

The box is **hidden** for a remote target rather than disabled: that target is
reached with a device access token, which cannot register devices at all, so
there is nothing to explain about a control that could never be used.

## Languages

Every string the plugin shows goes through the platform's own translation pipe,
with the English source text as its key — the gettext arrangement Cumulocity
uses. The catalogues are [ui/src/locales](../ui/src/locales):

| | |
| --- | --- |
| `de.po` | German, complete |
| `en.po` | British English — nearly empty by design: the source *is* British English, and a key with no entry falls back to it |
| `en-US.po` | American English: the spellings that differ, which today means `Cancelled` → `Canceled` |

They are imported from `src/main.ts` for their side effect. The build reads every
`.po` file in the module graph and emits one `<language>.json` per `Language:`
header; at runtime the shell fetches those from
`/apps/devicemigration-ui/<language>.json` and merges them over its own
dictionary. Nothing else is wired up — no service, no hook.

**Which languages a user can choose is the platform's list, not the plugin's.**
Shipping `de.po` does not add German to the language menu; it means that a user
who has German selected sees this plugin in German rather than in English.

Three things about the arrangement are worth knowing:

- **Strings built at runtime cannot be extracted**, so the API's own values —
  `asset`, `measurements` — are translated through a marked table in
  [labels.ts](../ui/src/migrate/labels.ts) rather than by capitalising them.
  Capitalisation is not translation: `device` is `Gerät`, and no amount of it
  would have got there.
- **A sentence with a number in it is one string**, not fragments concatenated:
  `took {{ minutes }} min` is a key a translator can rearrange, and German needs
  to (`dauerte {{ minutes }} Min.`). That is why durations come back from
  [rows.ts](../ui/src/history/rows.ts) as a key and its parameters rather than as
  finished text.
- **The service's own error messages are not translated.** They are shown as they
  arrive: somebody may have to search for one, and a paraphrase makes that
  harder. They still pass through the pipe, where a key with no entry falls back
  to itself.

`make ui-test` checks the catalogues against the source: every string the plugin
can show has a German entry, no entry survives the string it belonged to, nothing
is left blank, and every `{{ parameter }}` survives translation. It also checks
the templates themselves — a string can be in every catalogue and still arrive in
English if the `| translate` is missing, which is exactly what happened to the
checkbox labels. All of it fails loudly, because the alternative is invisible: the
fallback is English, and English is what the team reads.

**Timestamps are the platform's**, through its `c8yDate` pipe, which formats in
the language and date format the person reading has chosen — `27/08/2026` for
`en`, `8/27/26` for `en-US`, `27.08.2026` for `de`. Angular's own `date` pipe
formats in the application's build-time locale, which is not the same thing and
was what this used at first.

## Development

```sh
make ui-install                                       # once; the Web SDK is a large tree
cd ui && npm start -- --url https://your-tenant.cumulocity.com
make ui-test ui-typecheck                             # what runs in make check
```

That serves this plugin as a **remote** and proxies everything else to a real
Administration, which is the only way to see it in its host application. It
prints the URL to open:

```
🐚 Shell pointing to: http://localhost:4200/apps/administration/index.html?remotes={"devicemigration-ui":["DeviceMigrationModule"]}
```

**Pass `--url`.** The dev server's own default is `https://demos.cumulocity.com`,
so leaving it out points a development session at a shared public system rather
than saying it needs a tenant. It is deliberately not set in `angular.json` for
the same reason — a checked-in default would be somebody else's tenant.

**Inside [the devcontainer](devcontainer.md), add `--host 0.0.0.0`.** The server
binds localhost by default, which inside a container is a port the published
mapping cannot reach — so from there it is
`npm start -- --url https://<tenant>.cumulocity.com --host 0.0.0.0`, and the
browser on the host still opens `localhost:4200`, which the sandbox pins to the
host's loopback.

The shell is configured in `angular.json` as `shell: administration`. Two things
about that configuration are easy to get wrong and were:

- **`buildTarget` is required** by the dev-server builder. Without it `ng serve`
  refuses to start, while `ng build` is perfectly happy — so a plugin can build
  and be impossible to run locally.
- **A plugin must not emit an `index.html`.** The shell already has one, and two
  assets claiming that filename makes the dev server fail with a conflict that
  says nothing about shells. There is no `index` in the build options and no
  `src/index.html`.

### Versions, and one pin worth knowing about

The Web SDK is `1024.16.12`, which needs Angular 21 and Node 20.19 or newer. Two
things about the toolchain are not what older examples suggest:

- **`c8ycli` no longer exists.** Building is `ng build` with `@c8y/devkit`'s
  Angular builders; `c8y-toolkit` only deploys.
- **`monaco-editor` is pinned to 0.52.2** and must stay pinned. The devkit build
  always compiles Monaco's workers from paths like
  `monaco-editor/esm/vs/editor/editor.worker.js`, and from 0.53 Monaco added an
  `exports` map that rewrites `./X.js` to `./esm/vs/X.js` — so that request
  resolves to `esm/vs/esm/vs/…` and the build fails on five workers this plugin
  does not even use.

## What is not here

- **No end-to-end test.** There is no browser test and no mocked platform
  standing in for one: it would prove only that the mock agrees with itself. The
  pure logic is tested; the wiring is verified by running it against a tenant.
- **No export or import screens.** Both exist to produce and consume files, and a
  browser is the wrong place for either. They stay in
  [the command line tool](cli.md).
- **No tenant subscription screen.** The platform already has one, and doing it
  here would duplicate a permission model that is not ours.
- **No credential distribution.** The download hands over a file; getting it onto
  the devices is the operator's own channel. Pushing it through the source tenant
  as an operation would work and is tempting, and it would write plaintext
  passwords into that tenant's operation log — so it belongs in a runbook that
  accepts that, not in a button here.
