import { ApiError, type Job } from "../api/client";
import { gettext, message, type Message } from "../i18n";
import { labelFor, type TenantsById } from "../tenants";
import {
  deviceProgress,
  isRunning,
  present,
  type DeviceProgress,
  type StatusPresentation,
} from "../progress";

/** One line of the list: the job, how it reads, and what can be done about it. */
export interface Row {
  job: Job;
  shown: StatusPresentation;

  /**
   * How far through the devices a job is - only while it is running.
   *
   * A finished job has no progress, it has an outcome. The bar used to stay on
   * the row afterwards, because the line it is read from stays in the log, which
   * left a full bar and a dangling caption under a migration that was over.
   */
  progress?: DeviceProgress;

  /** How long it has been going, or how long it ran. */
  took?: Message;

  /** What the job was about: the object migrated, or the run being undone. */
  what: Message;

  /** The tenants, named rather than numbered. Empty for a job's absent side. */
  source: string;
  target: string;

  /**
   * The undos of this migration, newest first.
   *
   * A migration can have more than one: a revert that failed part way through
   * leaves the offer standing, so the next attempt is another job. They are rows
   * of their own no longer - they are read in the row's expansion.
   */
  undos: Undo[];

  revertable: boolean;
  cancellable: boolean;
}

/** One undo of one migration, as its expansion needs it. */
export interface Undo {
  job: Job;
  shown: StatusPresentation;
  took?: Message;
  /** Whether the engine left an account of what this undo deleted. */
  hasOutput: boolean;
}

/**
 * The two listings as one history, newest first.
 *
 * Reverts are jobs too, and reading them separately would hide the fact that
 * somebody already undid a migration - which is the single most important thing
 * to know before undoing it again.
 */
export interface RowContext {
  /** What each tenant is called, for the columns that only recorded an id. */
  tenants?: TenantsById;
  now?: Date;
}

export function rows(
  migrations: Job[],
  reverts: Job[],
  context: RowContext = {},
): Row[] {
  const now = context.now ?? new Date();
  const tenants = context.tenants ?? new Map();
  const undone = undoneRunIds(reverts);
  const byRun = revertsByRunId(reverts);

  // A migration and the undo that cancelled it are one story, so they are one
  // row: the undo is read in the row's expansion rather than somewhere else in
  // the list behind a matching run id.
  //
  // An undo whose migration is not in this listing still gets a row of its own.
  // That happens - a revert run from the command line against a run id whose
  // migration has aged out of the records - and a job that has no parent to sit
  // under must not become a job that is nowhere at all.
  const claimed = new Set<Job>();
  for (const [runId, undos] of byRun) {
    if (migrations.some((job) => job.runId === runId)) {
      undos.forEach((undo) => claimed.add(undo));
    }
  }

  const listed: Job[] = [
    ...migrations,
    ...reverts.filter((revert) => !claimed.has(revert)),
  ];

  // Newest first, which is also what the grid sorts by until somebody clicks a
  // header. Sorted here as well so the order is defined for anything that reads
  // these rows without a grid around them.
  return listed
    .sort((a, b) => (b.queuedAt ?? "").localeCompare(a.queuedAt ?? ""))
    .map((job) => {
      const undos = (job.runId && job.kind !== "REVERT"
        ? byRun.get(job.runId) ?? []
        : []
      ).map((undo) => ({
        job: undo,
        shown: present(undo),
        took: duration(undo, now),
        hasOutput: hasDetail({ job: undo }),
      }));

      return {
        job,
        shown: outcome(job, undos),
        progress: isRunning(job) ? deviceProgress(job.progress) : undefined,
        took: duration(job, now),
        what: describe(job),
        source: labelFor(sourceOf(job), tenants),
        target: labelFor(targetOf(job), tenants),
        revertable: revertable(job, undone),
        undos,
        cancellable: isRunning(job),
      };
    });
}

/**
 * The undos of each migration, newest first, keyed by the run they undid.
 *
 * A revert names its migration by run id and nothing else, which is why the
 * list could not show the relationship before: the id was all there was to go
 * on, and matching ids by eye is not a feature.
 */
function revertsByRunId(reverts: Job[]): Map<string, Job[]> {
  const byRun = new Map<string, Job[]>();
  for (const revert of reverts) {
    const runId = revert.revert?.runId;
    if (!runId) {
      continue;
    }
    byRun.set(runId, [...(byRun.get(runId) ?? []), revert]);
  }
  for (const undos of byRun.values()) {
    undos.sort((a, b) => (b.queuedAt ?? "").localeCompare(a.queuedAt ?? ""));
  }
  return byRun;
}

/**
 * The one status a row shows.
 *
 * One status, not a status and a qualifier: a migration that was undone is
 * *Reverted*, not "Succeeded - already undone". Two words joined by a dash were
 * two facts the reader had to combine, and the outcome of a migration that no
 * longer exists in the target tenant is not "succeeded".
 *
 * A succeeded undo wins over everything, because it is the end of the story. An
 * undo still running is next. An undo that failed is said out loud rather than
 * left to the expansion: the migration's objects may be half deleted, and a row
 * reading *Succeeded* would be the most misleading thing on the screen.
 */
export function outcome(job: Job, undos: Undo[]): StatusPresentation {
  if (job.kind === "REVERT") {
    // An orphan undo, listed on its own. It is what it is.
    return present(job);
  }

  if (undos.some(({ job: undo }) => undo.status === "SUCCEEDED")) {
    return { label: gettext("Reverted"), tone: "danger" };
  }
  if (undos.some(({ job: undo }) => isRunning(undo))) {
    return { label: gettext("Reverting"), tone: "info" };
  }
  if (undos.length > 0) {
    return {
      label: gettext("Revert failed"),
      tone: "warning",
      caveat: gettext(
        "An undo of this migration did not finish, so some of what it created may already be gone. Undoing it again deletes whatever is left.",
      ),
    };
  }
  return present(job);
}

/**
 * The run ids an undo has already finished for.
 *
 * Only a succeeded revert counts. One that failed or was cut short may have
 * deleted some of what it found and not the rest, and offering to try again is
 * the useful thing to do with that - a revert deletes what carries the marker, so
 * running it twice is not harmful.
 */
function undoneRunIds(reverts: Job[]): Set<string> {
  const done = new Set<string>();
  for (const revert of reverts) {
    if (revert.status === "SUCCEEDED" && revert.revert?.runId) {
      done.add(revert.revert.runId);
    }
  }
  return done;
}

/** What the job was about, in one phrase. */
function describe(job: Job): Message {
  if (job.kind === "REVERT") {
    return message("Undo of {{ runId }}", { runId: job.revert?.runId ?? "" });
  }
  const id = job.request?.rootId ?? "";
  return job.rootName
    ? message("{{ name }} ({{ id }})", { name: job.rootName, id })
    : message("{{ id }}", { id });
}

/** Which tenant a job read from, migration or undo. */
function sourceOf(job: Job): string | undefined {
  return job.kind === "REVERT" ? undefined : job.request?.sourceTenant;
}

/** Which tenant it wrote to - or, for an undo, deleted from. */
function targetOf(job: Job): string | undefined {
  return job.kind === "REVERT"
    ? job.revert?.targetTenant
    : job.request?.targetTenant;
}

/**
 * How long a job has been running, or how long it took.
 *
 * `now` is passed in rather than read here, so this can be tested at all. The
 * list re-reads itself every few seconds while anything is running, which is what
 * keeps a running job's figure moving.
 */
export function duration(job: Job, now: Date): Message | undefined {
  if (!job.startedAt) {
    return undefined;
  }
  const started = new Date(job.startedAt).getTime();
  const until = job.finishedAt
    ? new Date(job.finishedAt).getTime()
    : now.getTime();
  if (Number.isNaN(started) || Number.isNaN(until) || until < started) {
    return undefined;
  }

  return describeSpan(until - started);
}

/**
 * A span of milliseconds, in the largest unit that says something useful.
 *
 * The bare span, for a running job as much as a finished one: the column is
 * called Duration and the status column already says whether the figure is
 * still climbing, so "took" and "running for" were both saying something twice
 * - and the longer of them, "running for 1 h 30 min", was the only value wide
 * enough to stop the column being narrow.
 *
 * Still whole sentences rather than a number and a unit stuck together, because
 * German abbreviates the units differently and a translator has to be able to
 * move them.
 */
function describeSpan(milliseconds: number): Message {
  const seconds = Math.round(milliseconds / 1000);
  if (seconds < 60) {
    return message("{{ seconds }} s", { seconds });
  }

  const total = Math.round(seconds / 60);
  if (total < 60) {
    return message("{{ minutes }} min", { minutes: total });
  }

  const hours = Math.floor(total / 60);
  const minutes = total % 60;
  if (minutes === 0) {
    return message("{{ hours }} h", { hours });
  }
  return message("{{ hours }} h {{ minutes }} min", { hours, minutes });
}

/**
 * Whether a run can be undone through the service.
 *
 * A migration marks everything it created with its run id, which is how a revert
 * finds it again. Four things make that impossible: a revert creates nothing, so
 * there is never anything to undo about one; a run started with `noFlag` marked
 * nothing, so only its mapping file could find those objects - and that file lived
 * in a job directory the container has since removed; a run still going has not
 * finished creating what an undo would delete; and a run that has already been
 * undone has nothing left to delete.
 */
export function revertable(job: Job, undone: Set<string> = new Set()): boolean {
  if (job.kind === "REVERT" || isRunning(job)) {
    return false;
  }
  if (job.runId && undone.has(job.runId)) {
    // Already undone, successfully. Offering it again would delete nothing and
    // report success, which reads as though something had been done.
    return false;
  }
  if (job.request?.remoteTargetId) {
    // The service refuses these: the certificate it reached that tenant with
    // lived in memory and is gone, and deleting what the migration created needs
    // an identity that owns it. Offering a button whose only outcome is that
    // refusal is worse than not offering it - the same rule as the role-gated
    // navigator entry.
    return false;
  }
  return !!job.runId && !job.request?.noFlag;
}

/**
 * What somebody should be told before undoing a migration.
 *
 * The engine keeps no plan a screen could show - there is no dry run to ask, and
 * the record carries no count of what was created - so a confirmation cannot
 * promise "12 objects". What it can do is be exact about the rule (everything
 * carrying this run's marker) and honest about the three ways the result can
 * surprise somebody.
 */
export function revertWarnings(row: Row): string[] {
  const warnings: string[] = [
    gettext(
      "Deleting a group also deletes whatever Cumulocity propagates that deletion to, which can include objects this migration never created.",
    ),
  ];

  if (row.job.countsVerified === false) {
    warnings.push(
      gettext(
        "This run was cut short, so its own account of what it created cannot be relied on. The undo deletes whatever still carries the run marker.",
      ),
    );
  }
  if (row.undos.length > 0) {
    warnings.push(
      gettext(
        "An earlier undo of this migration did not finish, so some of what it created may already be gone. Undoing it again deletes whatever is left.",
      ),
    );
  }
  return warnings;
}

/**
 * Whether a row has anything to show beyond its own summary.
 *
 * The engine's output is the only account of what a run actually did, and it is
 * how a failure is diagnosed. There is nowhere else to read it - the dialogue is
 * gone by the time a job runs - so the list has to offer it, and only offer it
 * where there is something to read.
 */
export function hasDetail(row: { job: Job }): boolean {
  return !!row.job.error || !!row.job.progress?.length;
}

/** Whether anything in the list is still going. */
export function stillRunning(list: Array<{ job: Job }>): boolean {
  return list.some((row) => isRunning(row.job));
}

/** How often the list asks again when nothing is running. */
export const IDLE_REFRESH_MS = 30_000;

/**
 * How often it asks while something is.
 *
 * A second, because a migration is the one thing on this screen worth watching
 * and at ten it did not look like anything was happening: the device counter and
 * the progress bar moved in jumps, which reads as a stuck job rather than a
 * working one.
 *
 * The cost is real and bounded. Each read is three calls to the service, each of
 * which costs it a round trip to the platform to verify the caller - but the
 * next read is scheduled only once the previous one has finished, so this is a
 * gap between reads and not a rate. A slow answer stretches the cycle instead of
 * queueing another request behind it, and the fast cadence lasts only as long as
 * a job does.
 */
export const RUNNING_REFRESH_MS = 1_000;

/**
 * How long to wait before reading the list again.
 *
 * It keeps reading either way, because a migration can be started by somebody
 * else - or from `c8y api` - and a screen that only refreshed its own work would
 * show a stale list to everybody else. Every read costs the service a round trip
 * to the platform to verify the caller, so the quiet case is deliberately slow
 * and the interesting one is deliberately not: thirty seconds against one.
 */
export function refreshInterval(list: Array<{ job: Job }>): number {
  return stillRunning(list) ? RUNNING_REFRESH_MS : IDLE_REFRESH_MS;
}

/**
 * A status as the platform's own tag class.
 *
 * `.tag` rather than `.label`: they are the same colours - both resolve to
 * `$status-<tone>-light` on `$status-<tone>-dark` - and the stylesheet marks
 * `.label` deprecated, "to be replaced by Tag". Success is therefore the light
 * green with dark green text, which is the one the platform means by success.
 *
 * `present` and `outcome` decide the tone; this is the only place that knows
 * what Cumulocity calls it. A status rendered without its tone reads as five
 * identical grey words, which is worse than no tag at all.
 */
export function statusTag(row: Row): string {
  return tagForTone(row.shown.tone);
}

/** The tag classes for one tone, wherever a status is shown. */
export function tagForTone(tone: StatusPresentation["tone"]): string {
  switch (tone) {
    case "info":
      return "tag tag--info";
    case "success":
      return "tag tag--success";
    case "warning":
      return "tag tag--warning";
    case "danger":
      return "tag tag--danger";
    default:
      return "tag tag--default";
  }
}

/**
 * One row as the platform's grid wants it: flat, and already in words.
 *
 * The grid reads a path out of each row and sorts, filters, searches and pages on
 * what it finds - so anything a column shows has to be a plain value on the row
 * itself, and it has to be finished text. A translation key would sort as a key.
 * The component builds these, because it is the only place with a translator.
 */
export interface GridRow {
  /** The whole job, for the row actions to work on. */
  job: Job;

  id: string;

  /**
   * The status as finished text: what the icon's tooltip says, and what the
   * grid sorts, searches and filters on. The icon is a picture of it, not a
   * replacement for it.
   */
  status: string;
  /** The same status as the platform's tag classes, from `statusTag`. */
  statusClass: string;
  /**
   * What qualifies the status without being part of it: how far a running job
   * has got, and what cannot be believed about a finished one. The tag's
   * tooltip, so the column itself stays one word.
   */
  detail: string;
  what: string;
  source: string;
  target: string;
  /** The timestamp as the reader's locale writes it: what the cell shows. */
  askedFor: string;
  /**
   * The same moment as the service recorded it, ISO and UTC.
   *
   * The Timestamp column orders and searches on this, not on `askedFor`: the
   * grid sorts whatever `path` points at, and a localised date string sorts
   * alphabetically - which puts 01/09 before 28/08.
   */
  queuedAt: string;
  took: string;
  by: string;
  runId: string;

  /** Read by the actions' showIf, which the grid calls with the row. */
  revertable: boolean;
  cancellable: boolean;
  hasOutput: boolean;

  /** What the migration included, for the expansion to show. */
  scope: ScopeRow;

  /** What the confirmation says before undoing this, from `revertWarnings`. */
  revertWarnings: string[];

  /** The undos, finished for the expansion to show. Empty if never undone. */
  undos: UndoRow[];
}

/** What a migration moved, in words, for its expansion. */
export interface ScopeRow {
  runId: string;
  relationships: string;
  dataTypes: string;
  /** Empty when no window was given. */
  window: string;
  remote: boolean;
}

/** One undo in a row's expansion, already in words. */
export interface UndoRow {
  job: Job;
  status: string;
  statusClass: string;
  caveat: string;
  when: string;
  took: string;
  by: string;
  runId: string;
  hasOutput: boolean;
}

/**
 * The fresh rows, reusing the objects of the ones that have not changed.
 *
 * The grid tracks which rows are expanded in a `Map` keyed by the **row object
 * itself**. Handing it a brand new array every few seconds therefore threw away
 * every expansion, which is why an expanded row collapsed on its own the moment
 * the list refreshed - a job the reader was in the middle of reading.
 *
 * So an unchanged row is handed back the object it was last time. Only a row
 * whose content really differs becomes a new object, and only that row loses its
 * expansion - which is defensible, because something about it actually changed.
 *
 * Identity is compared by content rather than by id alone: reusing the old
 * object for a row whose status has moved on would show yesterday's status
 * forever.
 */
export function keepingIdentity(
  previous: GridRow[],
  fresh: GridRow[],
): GridRow[] {
  const before = new Map(previous.map((row) => [row.id, row]));
  return fresh.map((row) => {
    const old = before.get(row.id);
    return old && JSON.stringify(old) === JSON.stringify(row) ? old : row;
  });
}

/**
 * What a migration was asked to move.
 *
 * The columns say which object moved and between which tenants; they do not say
 * what "it" included, and that is the question somebody has a week later. It
 * lives in the row's expansion, which also means no row is an expansion that
 * says nothing - every migration has a scope, whether or not it was ever undone.
 *
 * The defaults are the point of doing this in a function: an omitted
 * `childTypes` is not "none", it is `asset,device`, and an omitted `dataTypes`
 * really is none. Reading the raw request would get that backwards.
 */
export interface Scope {
  /** The relationship types followed, or empty when only the root moved. */
  relationships: string[];
  rootOnly: boolean;
  /** The kinds of device data included, empty when none was. */
  dataTypes: string[];
  from?: string;
  to?: string;
  /** Whether the target was reached with a certificate rather than a subscription. */
  remote: boolean;
}

export function scopeOf(job: Job): Scope {
  const request = job.request;
  const rootOnly = !!request?.noChildren;
  const childTypes = request?.childTypes;

  return {
    // The API's own default, which is what silence on the request means.
    relationships: rootOnly ? [] : split(childTypes ?? "asset,device"),
    rootOnly,
    dataTypes: split(request?.dataTypes ?? ""),
    from: request?.dateFrom,
    to: request?.dateTo,
    remote: !!request?.remoteTargetId,
  };
}

function split(list: string): string[] {
  return list
    .split(",")
    .map((item) => item.trim())
    .filter(Boolean);
}

/**
 * Whether a failure means the microservice is not deployed on this tenant.
 *
 * That is not an error to apologise for: it is the signal that this is a target
 * tenant of a remote migration, where the plugin is installed but the service is
 * not. The gateway answers a call to an absent microservice with a 404 naming it,
 * so a 404 on the very first load - or a message that says as much - is read as
 * "this is a target tenant", and the screen offers to trust a certificate rather
 * than showing a scary refusal.
 */
export function serviceAbsent(error: unknown): boolean {
  if (!(error instanceof ApiError)) {
    return false;
  }
  return error.status === 404 || /not found|not subscribed|microservice/i.test(error.message);
}

/**
 * A failure, as a screen should put it.
 *
 * The title is ours and translated. The detail is the service's own sentence,
 * passed through untouched - it is written to be read, and paraphrasing a message
 * somebody may have to search for helps nobody. It goes through the translation
 * pipe as well, where it falls back to itself.
 */
export interface Failure {
  title: string;
  detail: string;
}

export function explain(error: unknown): Failure {
  if (!(error instanceof ApiError)) {
    return { title: gettext("Something went wrong"), detail: String(error) };
  }

  switch (error.kind) {
    case "unauthenticated":
      return { title: gettext("Sign in again"), detail: error.message };
    case "forbidden":
      return { title: gettext("Not allowed here"), detail: error.message };
    case "busy":
      return { title: gettext("The service is busy"), detail: error.message };
    default:
      return {
        title: gettext("The migration service refused this"),
        detail: error.message,
      };
  }
}
