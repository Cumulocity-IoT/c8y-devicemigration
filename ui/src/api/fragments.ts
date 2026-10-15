/**
 * Fragment names the service writes onto managed objects.
 *
 * These are a contract with the platform's data, not with the API: the service
 * writes them and this plugin queries for them, so neither end can change one
 * alone. They are spelled out in Go as `importer.MarkerPrefix` and
 * `service.IssuedFragment`, and a Go test reads this file and fails if the two
 * ever disagree.
 *
 * Here rather than inline because a prefix written out by hand in a template is
 * exactly how a duplicate drifts unnoticed.
 */

/**
 * Prefixed to a run id to name the fragment stamped on everything a migration
 * created: `c8y_DeviceMigration_<runId>`.
 *
 * It is what makes a run findable afterwards - by the revert, by a resume, and
 * by anyone counting what a migration produced.
 */
export const MARKER_PREFIX = 'c8y_DeviceMigration_';

/** The fragment of a run, from its id. */
export function markerFragment(runId: string): string {
  return MARKER_PREFIX + runId;
}

/**
 * Recorded on a device's migrated object once its credential has been handed
 * over, so it is handed over exactly once.
 *
 * The fragment, never the secret. Counting objects that carry it is how far a
 * device-self-service migration has got; removing it from one object is how an
 * administrator lets a device that lost its password collect again.
 */
export const CREDENTIALS_ISSUED_FRAGMENT = 'c8y_DeviceMigrationCredentialsIssued';
