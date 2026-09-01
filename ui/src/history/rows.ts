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

  /** Whether an undo has already finished for this migration. */
  undone: boolean;

  revertable: boolean;
  cancellable: boolean;
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

  // Newest first, which is also what the grid sorts by until somebody clicks a
  // header. Sorted here as well so the order is defined for anything that reads
  // these rows without a grid around them.
  return [...migrations, ...reverts]
    .sort((a, b) => (b.queuedAt ?? "").localeCompare(a.queuedAt ?? ""))
    .map((job) => ({
      job,
      shown: present(job),
      progress: isRunning(job) ? deviceProgress(job.progress) : undefined,
      took: duration(job, now),
      what: describe(job),
      source: labelFor(sourceOf(job), tenants),
      target: labelFor(targetOf(job), tenants),
      revertable: revertable(job, undone),
      undone: !!job.runId && job.kind !== "REVERT" && undone.has(job.runId),
      cancellable: isRunning(job),
    }));
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
  return !!job.runId && !job.request?.noFlag;
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

/** How often it asks while something is. */
export const RUNNING_REFRESH_MS = 10_000;

/**
 * How long to wait before reading the list again.
 *
 * It keeps reading either way, because a migration can be started by somebody
 * else - or from `c8y api` - and a screen that only refreshed its own work would
 * show a stale list to everybody else. Every read costs the service a round trip
 * to the platform to verify the caller, so the quiet case is deliberately slow
 * and the interesting one is not.
 */
export function refreshInterval(list: Array<{ job: Job }>): number {
  return stillRunning(list) ? RUNNING_REFRESH_MS : IDLE_REFRESH_MS;
}

/** A status as one glyph and one colour. */
export interface StatusBadge {
  /** A Cumulocity icon name, for `c8yIcon`. */
  icon: string;
  /** The platform's own text-colour class, carrying the tone. */
  className: string;
}

/**
 * A status as an icon and a colour, rather than a word.
 *
 * The icon is keyed on the job's own status and the colour on the tone `present`
 * decided, because the two are not the same question: cancelled and interrupted
 * are both warnings and want the same colour, and they are not the same thing,
 * so they must not be the same picture.
 *
 * Nothing here is the whole story - the full wording, the progress and any
 * caveat stay on the row and become the icon's tooltip. An icon alone would
 * throw away the caveat, which is the one part somebody has to read.
 */
export function statusBadge(row: Row): StatusBadge {
  return { icon: iconFor(row.job.status), className: colourFor(row.shown.tone) };
}

function iconFor(status: string | undefined): string {
  switch (status) {
    case "QUEUED":
      return "clock";
    case "RUNNING":
      return "refresh";
    case "SUCCEEDED":
      return "check-circle";
    case "FAILED":
      return "times-circle";
    case "CANCELLED":
      return "ban";
    case "INTERRUPTED":
      return "exclamation-triangle";
    default:
      return "exclamation-circle";
  }
}

function colourFor(tone: StatusPresentation["tone"]): string {
  switch (tone) {
    case "info":
      return "text-info";
    case "success":
      return "text-success";
    case "warning":
      return "text-warning";
    case "danger":
      return "text-danger";
    default:
      return "text-muted";
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
  /** The same status as one glyph and one colour, from `statusBadge`. */
  statusIcon: string;
  statusClass: string;
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
export function explain(error: unknown): { title: string; detail: string } {
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
