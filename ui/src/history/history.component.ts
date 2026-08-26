import { CommonModule } from '@angular/common';
import { Component, OnInit, signal } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CoreModule } from '@c8y/ngx-components';

import { ApiError, type Job } from '../api/client';
import { MigrationService } from '../api/migration.service';
import { present, type StatusPresentation } from '../progress';

/** One row: the job, and how it should read. */
interface Row {
  job: Job;
  shown: StatusPresentation;
  /** Whether undoing this run is possible at all. */
  revertable: boolean;
}

/**
 * Past migrations, and the button that undoes one.
 *
 * The first screen built, because it is read-only and proves the whole chain in
 * a browser - the session cookie, the role, the job records - before any wizard
 * exists to depend on it.
 */
@Component({
  selector: 'c8y-device-migration-history',
  standalone: true,
  imports: [CommonModule, CoreModule, RouterLink],
  templateUrl: './history.component.html'
})
export class HistoryComponent implements OnInit {
  readonly rows = signal<Row[]>([]);
  readonly loading = signal(true);

  /** What went wrong, in the service's own words. */
  readonly problem = signal<{ title: string; detail: string } | undefined>(undefined);

  constructor(private readonly migration: MigrationService) {}

  async ngOnInit(): Promise<void> {
    await this.load();
  }

  async load(): Promise<void> {
    this.loading.set(true);
    this.problem.set(undefined);

    try {
      // Both listings, because a revert is a job too and the two together are
      // the history of what was done.
      const [migrations, reverts] = await Promise.all([
        this.migration.api.migrations(),
        this.migration.api.reverts()
      ]);

      this.rows.set(
        [...migrations, ...reverts]
          .sort((a, b) => (b.queuedAt ?? '').localeCompare(a.queuedAt ?? ''))
          .map(job => ({ job, shown: present(job), revertable: revertable(job) }))
      );
    } catch (error) {
      this.problem.set(describe(error));
    } finally {
      this.loading.set(false);
    }
  }

  async revert(job: Job): Promise<void> {
    try {
      await this.migration.api.startRevert({ jobId: job.id });
      await this.load();
    } catch (error) {
      this.problem.set(describe(error));
    }
  }
}

/**
 * Whether a run can be undone through the service.
 *
 * A migration marked its objects with the run id, which is how they are found
 * again. A run with noFlag did not, so only its mapping file could find them -
 * and that lived in a job directory the container removed. A revert creates
 * nothing, so there is never anything to undo about one.
 */
function revertable(job: Job): boolean {
  return job.kind !== 'REVERT' && !!job.runId && !job.request?.noFlag;
}

/** A failure, as a screen should put it. */
function describe(error: unknown): { title: string; detail: string } {
  if (!(error instanceof ApiError)) {
    return { title: 'Something went wrong', detail: String(error) };
  }

  switch (error.kind) {
    case 'unauthenticated':
      return { title: 'Sign in again', detail: error.message };
    case 'forbidden':
      return { title: 'Not allowed here', detail: error.message };
    case 'busy':
      return { title: 'The service is busy', detail: error.message };
    default:
      return { title: 'The migration service refused this', detail: error.message };
  }
}
