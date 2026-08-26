import type { components } from './api/types';

type Job = components['schemas']['Job'];

/**
 * Turning a job into something a screen can show.
 *
 * The engine reports progress by printing lines. There is no percentage in the
 * API and no streaming endpoint, so a bar has to be inferred - and the honest
 * way to infer one is to say when it cannot be. If the engine's wording ever
 * changes, `deviceProgress` returns nothing and the log is still there, rather
 * than a bar inventing a number.
 */

/** "Device 3 of 9", which the incremental data phase prints per device. */
const DEVICE_LINE = /^Device (\d+) of (\d+)\b/;

export interface DeviceProgress {
  current: number;
  total: number;
  /** 0 to 100, for a bar that is explicitly approximate. */
  percent: number;
}

/**
 * How far through the devices a job is, from the last line that said so.
 *
 * Undefined when no line said - a hierarchy without data never enters the
 * per-device phase, and a run that failed early never got there either.
 */
export function deviceProgress(progress: string[] | undefined): DeviceProgress | undefined {
  if (!progress) {
    return undefined;
  }

  // Backwards: the last statement is the current one.
  for (let i = progress.length - 1; i >= 0; i--) {
    const match = DEVICE_LINE.exec(progress[i]);
    if (!match) {
      continue;
    }

    const current = Number(match[1]);
    const total = Number(match[2]);
    if (!total || current > total) {
      // A line shaped like the one we want but not meaning it.
      continue;
    }
    return { current, total, percent: Math.round((current / total) * 100) };
  }
  return undefined;
}

/** The run id the engine announced, which is what a revert needs. */
export function runIdFrom(job: Job): string | undefined {
  return job.runId || undefined;
}

export type JobStatus = NonNullable<Job['status']>;

/** Whether nothing more will happen to this job. */
export function isFinished(job: Job): boolean {
  return ['SUCCEEDED', 'FAILED', 'CANCELLED', 'INTERRUPTED'].includes(job.status ?? '');
}

/** Whether polling should continue. */
export function isRunning(job: Job): boolean {
  return job.status === 'QUEUED' || job.status === 'RUNNING';
}

/**
 * How a status should read and look. Semantic colour, separate from any accent:
 * an interrupted job is a warning, not a failure, because nobody chose it and
 * something may still exist in the target.
 */
export interface StatusPresentation {
  label: string;
  tone: 'info' | 'success' | 'warning' | 'danger' | 'muted';
  /** Said out loud when the job's own numbers cannot be believed. */
  caveat?: string;
}

export function present(job: Job): StatusPresentation {
  switch (job.status) {
    case 'QUEUED':
      return { label: 'Waiting', tone: 'muted' };
    case 'RUNNING':
      return { label: 'Running', tone: 'info' };
    case 'SUCCEEDED':
      return { label: 'Succeeded', tone: 'success' };
    case 'FAILED':
      return {
        label: 'Failed',
        tone: 'danger',
        caveat: job.runId
          ? 'Objects may already exist in the target tenant. Undo it with this run id.'
          : undefined
      };
    case 'CANCELLED':
      return {
        label: 'Cancelled',
        tone: 'warning',
        caveat: caveatFor(job)
      };
    case 'INTERRUPTED':
      return {
        label: 'Interrupted',
        tone: 'warning',
        caveat: caveatFor(job)
      };
    default:
      return { label: job.status ?? 'Unknown', tone: 'muted' };
  }
}

/**
 * A run that was cut short cannot vouch for what it did: the engine has no
 * cancellation checks, so a cancelled import counts records it never sent as
 * failures and a cancelled revert reports objects as deleted that are still
 * there. `countsVerified` is the API saying exactly that, and it must not be
 * quietly dropped on the way to a screen.
 */
function caveatFor(job: Job): string {
  if (job.countsVerified) {
    return '';
  }
  return job.runId
    ? 'What reached the target tenant is not known. Check it by run id before retrying.'
    : 'What reached the target tenant is not known.';
}
