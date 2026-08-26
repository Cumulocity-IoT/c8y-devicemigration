/**
 * Which step of the wizard is reachable, and why not.
 *
 * Kept as data rather than as conditions scattered through templates: "Next is
 * disabled" is something a user has to be able to understand, and the reason
 * belongs beside the rule. Pure, so the rules are testable without rendering
 * anything.
 */

export type StepId = 'introduction' | 'source' | 'target' | 'review' | 'progress';

export const STEPS: readonly StepId[] = [
  'introduction',
  'source',
  'target',
  'review',
  'progress'
] as const;

export interface WizardState {
  sourceTenant?: string;
  rootId?: string;
  targetTenant?: string;

  /** Set once the pre-flight proved both service users. */
  preflightPassed?: boolean;

  /** Set once an estimate came back, and whether it allows the migration. */
  estimated?: boolean;
  withinLimits?: boolean;

  /** Set once a migration has been accepted. */
  jobId?: string;
}

export interface StepAvailability {
  reachable: boolean;
  /** Why not, phrased for a person. Empty when reachable. */
  because: string;
}

/** Whether a step can be opened, and what is missing if it cannot. */
export function availability(step: StepId, state: WizardState): StepAvailability {
  switch (step) {
    case 'introduction':
    case 'source':
      return reachable();

    case 'target':
      if (!state.sourceTenant) {
        return blocked('Choose the tenant to migrate from.');
      }
      if (!state.rootId) {
        return blocked('Choose the device or group to migrate.');
      }
      return reachable();

    case 'review':
      if (!state.targetTenant) {
        return blocked('Choose the tenant to migrate to.');
      }
      if (state.targetTenant === state.sourceTenant) {
        return blocked('The source and target cannot be the same tenant.');
      }
      if (!state.preflightPassed) {
        return blocked('Waiting for both tenants to be checked.');
      }
      return reachable();

    case 'progress':
      if (!state.jobId) {
        return blocked('No migration has been started.');
      }
      return reachable();
  }
}

/** Whether the migration itself may be started. */
export function canStart(state: WizardState): StepAvailability {
  const review = availability('review', state);
  if (!review.reachable) {
    return review;
  }
  if (!state.estimated) {
    return blocked('Waiting for the estimate.');
  }
  if (!state.withinLimits) {
    return blocked(
      'This migration is over the export limits. Narrow the data window or the child types.'
    );
  }
  return reachable();
}

/** The step after this one, or undefined at the end. */
export function next(step: StepId): StepId | undefined {
  return STEPS[STEPS.indexOf(step) + 1];
}

/** The step before this one, or undefined at the start. */
export function previous(step: StepId): StepId | undefined {
  const at = STEPS.indexOf(step);
  return at > 0 ? STEPS[at - 1] : undefined;
}

function reachable(): StepAvailability {
  return { reachable: true, because: '' };
}

function blocked(because: string): StepAvailability {
  return { reachable: false, because };
}
