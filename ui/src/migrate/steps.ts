import { gettext } from '../i18n';

/**
 * What is still missing before a migration can start.
 *
 * On one screen there is no Next button to disable, so the question changes from
 * "may I open the next step" to "what is left". Both answers come from here, and
 * both are sentences a person can act on rather than booleans.
 */

export interface MigrationState {
  sourceTenant?: string;
  rootId?: string;
  targetTenant?: string;

  /** Set once the pre-flight proved both service users. */
  preflightPassed?: boolean;

  /** Set once an estimate came back, and whether it allows the migration. */
  estimated?: boolean;
  withinLimits?: boolean;

  /** What is wrong with the start date, when something is. */
  windowProblem?: string;

  /**
   * Set when the chosen credential option cannot be provisioned into this
   * target, because it already has devices under the same external identities.
   *
   * Its own flag rather than a count, because there is nothing to tune: the
   * answer is to choose a different way for the devices to authenticate, or to
   * settle the duplicates in the two tenants first.
   */
  credentialsRefused?: boolean;
}

export interface Readiness {
  ready: boolean;
  /** Everything outstanding, in the order it has to be dealt with. */
  outstanding: string[];
}

/** Everything still needed, and whether that is nothing. */
export function readiness(state: MigrationState): Readiness {
  const outstanding: string[] = [];

  if (!state.sourceTenant) {
    outstanding.push(gettext('Choose the tenant to migrate from.'));
  }
  if (!state.rootId) {
    outstanding.push(gettext('Choose the device or group to migrate.'));
  }
  if (!state.targetTenant) {
    outstanding.push(gettext('Choose the tenant to migrate to.'));
  } else if (state.targetTenant === state.sourceTenant) {
    outstanding.push(gettext('The source and target cannot be the same tenant.'));
  } else if (!state.preflightPassed) {
    outstanding.push(gettext('Checking that both tenants can be reached.'));
  }

  // The field says what is wrong with it, so this only has to say that
  // something is: a start date nobody can parse would be refused by the
  // service, and starting on it would waste a job to find that out.
  if (state.windowProblem) {
    outstanding.push(gettext('The start date cannot be read.'));
  }

  // Only worth mentioning once there is something to estimate; before that the
  // missing pieces above are the answer.
  if (state.sourceTenant && state.rootId && state.targetTenant) {
    if (!state.estimated) {
      outstanding.push(gettext('Working out how much would move.'));
    } else if (!state.withinLimits) {
      outstanding.push(
        gettext('This migration is over the export limits. Narrow the data window or the child types.')
      );
    }
  }

  // Last, because it is about one answer to one question rather than about
  // whether the migration can run at all - and the devices it names are listed
  // beside the credentials box, where the choice that caused it was made.
  if (state.credentialsRefused) {
    outstanding.push(
      gettext('Device credentials cannot be provisioned into this tenant. Choose another way for the devices to authenticate.')
    );
  }

  return { ready: outstanding.length === 0, outstanding };
}
