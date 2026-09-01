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

Then grant `ROLE_DEVICE_MIGRATION_ADMIN` to whoever should use it. The navigator
entry is hidden without it, because every call the UI makes would be refused.

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
things that happened. Cancel, Revert and Migration log are the grid's **row
actions**, so each appears only where it applies and there are no button columns
to keep aligned.

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
which device data was included and over what window, and whether the target was
reached with a certificate. The undo is the headline of that panel when there is
one, and a migration nobody undid still has something to say. The defaults are
why [`scopeOf`](../ui/src/history/rows.ts) is a tested function rather than a
template reading the request: an omitted `childTypes` means `asset,device`, while
an omitted `dataTypes` really does mean none.

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

**A migration to a remote target is no longer offered for undoing at all.** The
service refuses those — the certificate it reached that tenant with lived in
memory and is gone, so it no longer holds an identity that owns the objects — and
`revertable` now applies the same rule the service does. Offering a button whose
only possible outcome is that refusal is the same mistake as a navigator entry
that only ever leads to one.

**An undo cannot itself be undone, and the expansion says so** rather than
leaving it to the absence of an action. It has to be said in the row's content:
`ActionControl` exposes `showIf` and no disabled state, so a greyed-out *Revert*
carrying the reason in a tooltip cannot be built without abandoning the grid's
row actions for a bespoke button column — which is what this design deliberately
moved away from.

Above the list is an **introduction that stays there**: what this moves, and the
two facts that reassure a first-time user — nothing is read until a migration is
started, and nothing is ever deleted from the tenant the devices came from. It is
not behind a help link because a migration is rare enough that almost everybody
doing one is doing it for the first time.

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
not at all — it is the platform's own trusted-certificate call. That page is
deliberately **not** gated on the migration role, because the person using it is
a target-tenant admin who does not hold it; its navigator entry is visible where
the plugin is installed. For a target without the plugin, the source panel offers
the certificate as a **download** for the manual Administration upload. The
mechanism behind the connection is in
[the microservice docs](microservice.md#remote-targets).

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
