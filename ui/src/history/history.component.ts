import { CommonModule } from '@angular/common';
import { Component, OnDestroy, OnInit, signal } from '@angular/core';
import {
  type ActionControl,
  C8yTranslatePipe,
  type Column,
  ColumnDataType,
  CoreModule,
  DatePipe
} from '@c8y/ngx-components';
import { BsModalService } from 'ngx-bootstrap/modal';

import { type Job } from '../api/client';
import { MigrationService } from '../api/migration.service';
import { gettext, message, type Message } from '../i18n';
import { MigrateComponent } from '../migrate/migrate.component';
import { byId } from '../tenants';
import { OutputComponent } from './output.component';
import { RegisterCertificateComponent } from '../trust/register-certificate.component';
import { explain, hasDetail, refreshInterval, rows, serviceAbsent, type GridRow, type Row } from './rows';

/**
 * What has been migrated, and the button that starts another.
 *
 * The list is the screen somebody arrives at: migrations are occasional, and the
 * useful question on opening this is what happened last time, not what to do
 * next. Starting one is a dialogue over the top of it.
 *
 * The table is the platform's own data grid, given rows rather than a callback -
 * which is what makes it sort, page and search through them itself. The service
 * has no paged listing to offer it, so all the records are fetched and the grid
 * pages what it is given; see docs/ui.md for what that does and does not bound.
 */
@Component({
  selector: 'c8y-device-migration-history',
  standalone: true,
  imports: [CommonModule, CoreModule, RegisterCertificateComponent],
  templateUrl: './history.component.html',
  // Both are pipes, injected so a row can be built as text; a pipe has to be
  // provided to be injectable.
  providers: [C8yTranslatePipe, DatePipe],
  styles: [
    `
      /* Running text, kept to a readable measure rather than the width of the
         table below it. */
      .device-migration-intro .intro-text {
        max-width: 68ch;
      }
      .device-migration-emblem {
        font-size: 96px;
        opacity: 0.35;
        margin-left: auto;
        padding-right: 24px;
      }
    `
  ]
})
export class HistoryComponent implements OnInit, OnDestroy {
  readonly rows = signal<GridRow[]>([]);
  readonly loading = signal(true);

  /** What went wrong, in the service's own words. */
  readonly problem = signal<{ title: string; detail: string } | undefined>(undefined);

  /**
   * Which screen this is. The plugin lives in two kinds of tenant: the one the
   * microservice runs on, where this lists migrations, and a remote target,
   * where the microservice is absent and the useful thing to offer is trusting
   * the incoming certificate. The load below decides which, from whether the
   * service answers.
   */
  readonly mode = signal<'migrations' | 'target'>('migrations');

  /**
   * The columns, and how they behave.
   *
   * Sortable, with "Asked for" descending to begin with: the useful order for a
   * list of things that happened is the order they happened in, newest first.
   * Source and target are columns of their own rather than a phrase in "What",
   * because a column can be sorted and read down, and a phrase cannot.
   */
  readonly columns: Column[] = [
    text('status', gettext('Status')),
    text('what', gettext('What')),
    text('source', gettext('Migrated from')),
    text('target', gettext('Migrated to')),
    { ...text('askedFor', gettext('Asked for')), sortOrder: 'desc' },
    text('took', gettext('Took')),
    text('by', gettext('By')),
    text('runId', gettext('Run id'))
  ];

  /**
   * What can be done to a row.
   *
   * The grid's own row actions, so there are no button columns to keep aligned,
   * and each one appears only where it applies - which is the whole of the rule
   * about what can be undone.
   */
  readonly actions: ActionControl[] = [
    {
      type: 'CANCEL_JOB',
      text: gettext('Cancel'),
      icon: 'ban',
      showIf: (row: GridRow) => row.cancellable,
      callback: (row: GridRow) => void this.cancel(row.job)
    },
    {
      type: 'REVERT_JOB',
      text: gettext('Revert'),
      icon: 'undo',
      showIf: (row: GridRow) => row.revertable,
      callback: (row: GridRow) => void this.revert(row.job)
    },
    {
      type: 'SHOW_OUTPUT',
      text: gettext('Output'),
      icon: 'file-text-o',
      showIf: (row: GridRow) => row.hasOutput,
      callback: (row: GridRow) => this.showOutput(row.job)
    }
  ];

  private polling?: ReturnType<typeof setTimeout>;

  constructor(
    private readonly migration: MigrationService,
    private readonly modals: BsModalService,
    // The pipe rather than a service: this is what the platform gives a plugin
    // for translating in code, and the grid needs finished text.
    private readonly translator: C8yTranslatePipe,
    private readonly dates: DatePipe
  ) {}

  async ngOnInit(): Promise<void> {
    await this.load();
  }

  ngOnDestroy(): void {
    clearTimeout(this.polling);
  }

  /** Opens the dialogue, and reloads once it has started something. */
  newMigration(): void {
    const dialogue = this.modals.show(MigrateComponent, {
      // The dialogue sizes itself; see the styles on MigrateComponent.
      class: 'device-migration-dialogue',
      ignoreBackdropClick: true
    });

    dialogue.content?.started.subscribe(() => {
      void this.load();
    });
  }

  /** The engine's own output for one job. */
  showOutput(job: Job): void {
    this.modals.show(OutputComponent, {
      class: 'modal-lg',
      initialState: { job }
    });
  }

  async load(): Promise<void> {
    this.loading.set(true);
    this.problem.set(undefined);

    try {
      // All three together: the tenants are what turn an id in a record into a
      // name somebody recognises, and the two listings are the history.
      const [migrations, reverts, tenants] = await Promise.all([
        this.migration.api.migrations(),
        this.migration.api.reverts(),
        this.migration.api.tenants()
      ]);

      this.rows.set(
        rows(migrations, reverts, { tenants: byId(tenants.tenants) }).map(row => this.forGrid(row))
      );
      this.mode.set('migrations');
    } catch (error) {
      if (serviceAbsent(error)) {
        // Not an error: the plugin is on a tenant the microservice is not on, so
        // this is a migration target. Offer to trust the certificate instead of
        // apologising for a service that was never meant to be here.
        this.mode.set('target');
        this.problem.set(undefined);
      } else {
        this.problem.set(explain(error));
      }
    } finally {
      this.loading.set(false);
      this.poll();
    }
  }

  async revert(job: Job): Promise<void> {
    try {
      await this.migration.api.startRevert({ jobId: job.id });
      await this.load();
    } catch (error) {
      this.problem.set(explain(error));
    }
  }

  async cancel(job: Job): Promise<void> {
    if (!job.id) {
      return;
    }
    try {
      await this.migration.api.cancelMigration(job.id);
      await this.load();
    } catch (error) {
      this.problem.set(explain(error));
    }
  }

  /**
   * One row, as the grid needs it: flat, and in words.
   *
   * The translating happens here because this is the only place with a
   * translator - the grid sorts and searches on what is in the row, so a key
   * would sort and search as a key.
   */
  private forGrid(row: Row): GridRow {
    const progress = row.progress;
    return {
      job: row.job,
      id: row.job.id ?? '',
      // Status, and what qualifies it: how far a running job has got, and what
      // cannot be believed about a finished one. In the column rather than
      // beside it, so it sorts and searches with the status it belongs to.
      status: [
        this.say(row.shown.label),
        progress &&
          this.say(
            message('roughly at device {{ current }} of {{ total }}', {
              current: progress.current,
              total: progress.total
            })
          ),
        row.undone && this.say(message('already undone')),
        row.shown.caveat && this.say(row.shown.caveat)
      ]
        .filter(Boolean)
        .join(' — '),
      what: this.say(row.what),
      source: row.source,
      target: row.target,
      // The platform's own date formatting, in the reader's language.
      askedFor: this.dates.transform(row.job.queuedAt, 'short') ?? '',
      took: row.took ? this.say(row.took) : '',
      by: row.job.requestedBy ?? '',
      runId: row.job.runId ?? '',
      revertable: row.revertable,
      cancellable: row.cancellable,
      hasOutput: hasDetail(row)
    };
  }

  /** A message as text, in the reader's language. */
  private say(message: Message | string): string {
    const { text, params } = typeof message === 'string' ? { text: message, params: {} } : message;
    return text ? this.translator.transform(text, params) : '';
  }

  /**
   * Reads the list again, sooner while something is running.
   *
   * Always reading, because a migration can be started from another session or
   * from the command line and a list that only followed its own work would go
   * quietly out of date. How often is refreshInterval's decision.
   */
  private poll(): void {
    clearTimeout(this.polling);
    // Nothing to poll on a target tenant - there is no service to ask.
    if (this.mode() === 'target') {
      return;
    }
    this.polling = setTimeout(() => void this.load(), refreshInterval(this.rows()));
  }
}

/** A sortable text column reading one field of the row. */
function text(name: string, header: string): Column {
  return {
    name,
    header,
    path: name,
    dataType: ColumnDataType.TextShort,
    sortable: true,
    filterable: false
  };
}
