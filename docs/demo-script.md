# Demo scripts

Two recordings: **four minutes on the user interface**, **one minute on the
standalone binary**. Each is a beat-by-beat script — what is on screen, what is
said, and when — written to be read aloud at roughly 145 words a minute, which is
the pace the timings assume. Every narration line below is within its beat's
budget at that pace; if you naturally speak faster, the slack goes into the
pauses marked *(beat)*, not into more words.

They are separate recordings on purpose. The audiences are different — the UI
video is for whoever decides to install this, the command line video for whoever
already lives in a terminal — and a five-minute video is watched less than a four
and a one.

## Before you record

**Neither the microservice nor the UI has been run against a live tenant yet**
(see the status notes in [ui.md](ui.md) and [microservice.md](microservice.md)).
So the recording session is also the first live test, and the two must not be the
same afternoon. Do a full rehearsal run on the real tenants at least a day
earlier, fix whatever it finds, and only then record.

### Tenants and data

| | |
| --- | --- |
| Parent tenant | microservice deployed and subscribed, UI plugin installed into Administration, `ROLE_DEVICE_MIGRATION_ADMIN` granted to the demo user |
| Source tenant | subscribed to the microservice, holding a **small, recognisable hierarchy** — a group of eight to twelve devices with names a viewer can read, not `Device_9f2c` |
| Target tenant | subscribed, and **empty of anything carrying a run marker**, so the migration you record is visibly the only thing there |
| History | three or four records already in the list before you start: one *Completed*, one *Reverted*, one *Failed*. A list with one row in it does not demonstrate a list |

Keep the recorded migration **small and without measurements**, or with a `-1d`
window. Four minutes has about twenty seconds of room for the run itself; a
hierarchy that takes two minutes has to be cut, and a cut in the middle of a
progress bar is the one edit viewers notice.

Have a second, larger completed migration in the history if you want to point at
a realistic duration without waiting for one.

### The screen

- 1920×1080, browser at 110–125 % zoom. Cumulocity's own font is small for video.
- Bookmarks bar hidden, one tab, notifications off, no email client running.
- The installed plugin, not `npm start`. The dev server's URL carries
  `?remotes={"devicemigration-ui":["DeviceMigrationModule"]}`, which is a
  paragraph of noise in the address bar and invites a question that is not what
  the video is about.
- Terminal for the second video: 100×30, 16–18 pt, a one-word prompt. A prompt
  carrying a git branch, a Kubernetes context and a battery percentage costs a
  third of every line.
- Move the mouse deliberately and stop moving it while you talk about something.
  A cursor drifting over a table reads as hesitation.

### Names on screen

Tenant ids and hosts are visible throughout both videos. Use demo tenants whose
names you are happy to publish, rather than blurring them afterwards — a blurred
rectangle in the middle of the frame is more distracting than
`t11223344`.

---

## Video one — the user interface (4:00)

### Beats

| Time | On screen | Say |
| --- | --- | --- |
| 0:00–0:08 | Administration open. Click **Device migration** in the navigator. | "This is the device migration plugin. It moves a device hierarchy from one Cumulocity tenant to another." |
| 0:08–0:20 | The list, at rest. Do not move the mouse. | "The list is the screen, because the useful question on opening this is what happened last time. Each row is a hierarchy, where it went, and whether it worked." |
| 0:20–0:32 | Trace the header row left to right. Hover the *Completed* tag so its tooltip opens. | "Migration root, source tenant, target tenant — then status. Newest first. The tag says one thing, and what merely qualifies it is in the tooltip." |
| 0:32–0:46 | Expand the completed row. Let the panel settle, then point at the run id and the scope. | "Every row expands: the full run id, which relationships were followed, which device data came with it. And when somebody has undone a run, the undo is in here too, rather than being a second row somewhere else." |
| 0:46–0:55 | Collapse it. Open the ⋮ menu on the same row, then close it. | "One menu per row, in the same place on every one of them, holding whatever that row offers." |
| 0:55–1:04 | Click **New migration** in the action bar. The drawer opens. | "Starting one is up here, in the action bar. It opens as a drawer over the list — no steps, everything visible at once." |
| 1:04–1:20 | Open **Migrate from**. Show the list of tenants, choose the source. *(beat)* | "Source on the left. Migrate from offers only the tenants this microservice is subscribed to — nothing else could be read, so nothing else is offered." |
| 1:20–1:40 | The asset selector loads. Type two letters into its search, drill into the group, select it. | "This is the platform's own asset selector — the same search and drill-down as everywhere else — except that it is reading the source tenant, through the microservice. Nothing is written to either tenant until a migration starts." |
| 1:40–1:52 | Choose the target from its dropdown. Point at, but do not tick, the remote-target checkbox. | "Target on the right. And if the target sits under a different parent, or on another platform entirely, this sets it up with a certificate instead." |
| 1:52–2:10 | Tick relationships: assets, devices. Tick measurements and events. | "Scope underneath. Which relationships to follow — assets, devices, additions. Which device data comes with it: measurements, events, alarms, none of them by default, because measurement volumes make that the only sensible default." |
| 2:10–2:24 | In the From field type `-14dd`. Let the error appear. Delete the extra `d`. | "And a window for that data: a relative one like this, an absolute date, or the calendar in the field. It is checked as you type, against exactly what the engine accepts." |
| 2:24–2:44 | The estimate fills in. Point at the object count and the busiest device. Untick alarms — the estimate clears — then re-run it. | "Then, what would actually move: managed objects, external identities, each kind of data, and the busiest device named. Change anything that changes what would move, and the estimate is discarded rather than left describing a different migration." |
| 2:44–2:54 | Point at the summary. Click **Start migration**. | "The summary says what is still outstanding, rather than disabling the button without saying why. Start migration." |
| 2:54–3:16 | The drawer closes; the row appears with a progress bar. Let it run. Reload the page mid-run. | "The drawer closes, and the run is followed in the list. The bar counts the engine's own progress lines, and while something is running the list re-reads itself every second. Reload the page — nothing is lost. A run somebody started from the API shows up here too." |
| 3:16–3:30 | The row turns *Completed*. Point at Duration, User, Run id. | "Completed. How long it took, who ran it, and the run id — and every object it created in the target carries a marker naming that run." |
| 3:30–3:42 | ⋮ → **Migration log**. Scroll it a little. Close. | "The migration log is the engine's own output for that run: the same text the command line prints, and the way to diagnose a failure." |
| 3:42–3:58 | ⋮ → **Revert**. Let the confirmation open. Point at the marker string. Click **Cancel**. | "And revert. It names the root, the tenant it deletes from, and the marker itself — so you can paste that into an inventory query and see the objects for yourself before you agree." |
| 3:58–4:00 | The list, at rest. | "Nothing is ever deleted from the tenant the devices came from." |

### Speaker notes

**0:08 — hold still.** The twelve seconds on the list at rest are the only time a
viewer gets to read the table. Talking over a moving cursor wastes them.

**0:32 — the expansion is the surprise.** Most people expect a history row to be
inert. Let the panel finish animating before saying anything about it; if the
demo tenant has a reverted row, expand *that* one instead, because the undo
inside it is the better version of this beat.

**1:20 — the asset selector is the claim worth making slowly.** It is the
platform's own component reading a *different tenant*. If a viewer does not
notice that, the beat has failed. Searching before drilling in makes it obvious
the data is live rather than a screenshot.

**2:10 — type the typo deliberately.** `-14dd` is in the script because a
validation message appearing under the field, instead of after a round trip, is
the point. Type it at normal speed; a slow deliberate typo reads as staged.

**2:24 — untick alarms on camera.** The estimate clearing is the second rule this
screen is built on, and it is only visible if something invalidates it while
being watched.

**2:54 — the run.** If the rehearsal shows the migration finishing in under
fifteen seconds, add a device or two to the hierarchy: a bar that fills instantly
demonstrates nothing. If it runs long, cut between the reload and the completed
row with a hard cut, not a fade — and cut on a moment where the bar has visibly
moved, so the cut cannot be read as a stall.

**3:42 — do not actually revert.** Cancel out. Reverting leaves the target tenant
needing a reset before the next take, and the confirmation dialogue is the whole
point of the beat anyway.

**If a take goes wrong after 2:44**, reset by reverting the run from the list and
deleting the record. That is two minutes of work between takes, so budget for
three attempts rather than one.

### What this video deliberately leaves out

Say none of this on camera; know it in case of questions afterwards.

- Installing the plugin and subscribing the tenants. That is
  [ui.md](ui.md#installing-it) and a different video.
- The *Trust a migration certificate* page, and remote targets generally. It is
  pointed at once, at 1:40, and it needs a second tenant's admin on screen to
  show properly.
- Export and import as separate steps. They are files, and the browser is the
  wrong place for files — which is what video two is for.

---

## Video two — the command line tool (1:00)

A single unbroken take in one terminal. Type the commands live; do not paste.
Pre-type nothing except the long `migrate` line, which is written out below and
should be recalled from shell history with an up-arrow so the sixty seconds are
not spent typing flags.

### Beats

| Time | On screen | Say |
| --- | --- | --- |
| 0:00–0:07 | `c8y-devicemigration --version` | "Same engine, without the platform: one binary that talks to the REST API directly." |
| 0:07–0:16 | `ls ~/.cumulocity/*.json` — the session files, one of them clearly encrypted | "It reads the session files go-c8y-cli already writes, encrypted ones included — and unlike c8y, it needs no active session." |
| 0:16–0:28 | Run `preflight` (below). Let the four output lines land. | "Preflight resolves both sides and proves the credentials work before anything is created. It names the tenants it matched, rather than the glob you typed." |
| 0:28–0:38 | Recall the `migrate` line. Press Tab on `--dataTypes` to show the completion. | "Migrate is the whole round trip in one command. Tab completes sessions and data types — the root id deliberately is not, because that would mean a network call on every tab press." |
| 0:38–0:44 | The confirmation prompt appears. Press `y`. | "It asks once, in the shape c8y asks, naming both tenants." |
| 0:44–0:54 | Output streams: export lines, the run marker, import lines, `Device 3 of 12`. | "Export, then import, then device data one device at a time — so a large hierarchy never has to fit on disk all at once." |
| 0:54–1:00 | `Migration complete. Reverse it with:` and the revert command under it. | "And it finishes by printing the command that undoes it." |

### The commands, verbatim

```sh
c8y-devicemigration --version

c8y-devicemigration preflight \
    --sourceSession source.eu-latest.cumulocity.com-me.json \
    --targetSession target.eu-latest.cumulocity.com-me.json

c8y-devicemigration migrate --rootId 12345 \
    --sourceSession source.eu-latest.cumulocity.com-me.json \
    --targetSession target.eu-latest.cumulocity.com-me.json \
    --dataTypes measurements,events --incremental
```

Deliberately **without `--force`**, so the confirmation at 0:38 happens.

### Speaker notes

**Run `make install-completion` before recording**, and start a fresh shell.
Completion is a beat in this video, and it silently completes nothing if the
binary is not on `PATH` under its own name.

**Clear the scrollback between takes** (`clear && printf '\e[3J'`), so a retake
does not open on the tail of the previous one.

**The Tab press at 0:28 needs a real pause.** Press it, stop, let the completion
list render for a full second, then keep talking. Completions that flash past are
not evidence of anything.

**The output at 0:44 will outrun the narration.** That is fine and reads as
speed. What must not happen is the run *finishing* before 0:54 — if the rehearsal
shows that, widen the data window or add devices, rather than slowing the
terminal down.

**If the run fails on camera, keep the take.** The failure path prints the
staging directory and the command to undo what was created, which is a better
sixty seconds than the happy one. Re-record the narration for the last two beats
against it.

---

## Optional pickups

Short clips worth recording while everything is set up, for cutting into either
video later or for answering a question in writing.

| Clip | Length | Why |
| --- | --- | --- |
| The refusal in a subtenant | 10 s | Opening the plugin where it may not be used names the tenant that may. It is the clearest statement of the permission model. |
| Remove migration record | 15 s | Its confirmation leads with the difference from a revert, which is the distinction people get wrong. |
| A revert, start to finish | 30 s | The confirmation, the *Reverting* status, then *Reverted* on the same row. Costs a target-tenant reset. |
| `c8y devicemigration migrate …` | 10 s | The same thing as the go-c8y-cli extension, for an audience that lives in `c8y`. |
| The German UI | 10 s | Switch the user's language and reload. Cheap, and it answers a question that always gets asked. |
