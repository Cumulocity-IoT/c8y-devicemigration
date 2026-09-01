import { CommonModule } from '@angular/common';
import { Component, OnDestroy, OnInit, signal } from '@angular/core';
import {
  type ActionControl,
  BottomDrawerService,
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
import { kindLabel } from '../migrate/labels';
import { byId } from '../tenants';
import { OutputComponent } from './output.component';
import { RevertConfirmComponent } from './revert-confirm.component';
import { RegisterCertificateComponent } from '../trust/register-certificate.component';
import {
  explain,
  hasDetail,
  type Failure,
  keepingIdentity,
  refreshInterval,
  revertWarnings,
  rows,
  scopeOf,
  serviceAbsent,
  statusTag,
  tagForTone,
  type GridRow,
  type Row,
  type ScopeRow,
  type Undo,
  type UndoRow
} from './rows';
import { StatusCellComponent } from './status-cell.component';
import { TimestampCellComponent } from './timestamp-cell.component';

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

      /* The row's expansion. Everything in it sits on one of two axes: labels
         in the first column, values in the second. The previous layout put a
         tag, a date and a username side by side in a flex row and they lined up
         on none of them. */
      .device-migration-detail {
        padding: 16px 16px 16px 0;
        max-width: 60em;
        font-size: 13px;
      }
      .device-migration-detail dl {
        display: grid;
        grid-template-columns: max-content minmax(0, 1fr);
        gap: 6px 20px;
        margin: 0 0 4px;
        align-items: baseline;
      }
      .device-migration-detail dt {
        font-weight: 400;
        color: var(--c8y-text-muted, #78849b);
        white-space: nowrap;
      }
      .device-migration-detail dd {
        margin: 0;
        min-width: 0;
        overflow-wrap: anywhere;
      }
      /* .tag sets align-self: flex-start, which in a baseline-aligned row lifts
         it above the text beside it. Its own font is smaller, so it is centred
         against the line rather than sharing a baseline with it. */
      .device-migration-detail dd .tag {
        align-self: center;
        vertical-align: middle;
      }
      /* The log button sits inline after the tag, so it must not carry a
         button's own padding or it pushes the line taller than its neighbours. */
      .device-migration-detail dd .btn-link {
        padding: 0;
        margin-left: 8px;
        vertical-align: middle;
        border: 0;
      }
      .device-migration-detail .detail-heading {
        margin: 16px 0 8px;
        font-size: 11px;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        color: var(--c8y-text-muted, #78849b);
      }
      .device-migration-detail .detail-note {
        margin: 12px 0 0;
        color: var(--c8y-text-muted, #78849b);
      }
      .device-migration-detail + .device-migration-detail {
        border-top: 1px solid var(--c8y-component-border-color, #d6dedd);
      }
    `
  ]
})
export class HistoryComponent implements OnInit, OnDestroy {
  readonly rows = signal<GridRow[]>([]);
  readonly loading = signal(true);

  /** What went wrong, in the service's own words. */
  readonly problem = signal<Failure | undefined>(undefined);

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
   * Sortable, with "Timestamp" descending to begin with: the useful order for a
   * list of things that happened is the order they happened in, newest first.
   * Source and target are columns of their own rather than a phrase in "What",
   * because a column can be sorted and read down, and a phrase cannot.
   *
   * Status sits fourth, after what moved and where it moved between. Reading a
   * row is "this hierarchy, from here to there - and it worked": the outcome
   * qualifies the sentence, so it comes after it rather than before it.
   */
  readonly columns: Column[] = [
    text('what', gettext('Migration root')),
    text('source', gettext('Source tenant')),
    text('target', gettext('Target tenant')),
    {
      ...text('status', gettext('Status')),
      // The platform's own tag, in the tone of the outcome. The path stays on
      // the status text, so sorting and searching still work on words.
      cellRendererComponent: StatusCellComponent,
      // Wide enough for the longest of them - "Revert failed" - and no wider.
      gridTrackSize: 'minmax(120px, 0.8fr)'
    },
    {
      ...text('askedFor', gettext('Timestamp')),
      // Newest first, and sorted on the raw timestamp rather than the formatted
      // one: the grid sorts on `path`, and a localised date string does not sort
      // chronologically. See timestamp-cell.component.ts.
      path: 'queuedAt',
      sortOrder: 'desc',
      cellRendererComponent: TimestampCellComponent,
      // A date and a time, and never more than that. The default track for
      // short text is minmax(80px, 1.67fr), which gave a fixed-length value as
      // much room as a tenant name that can run on.
      gridTrackSize: 'minmax(120px, 0.8fr)'
    },
    {
      ...text('took', gettext('Duration')),
      // Numeric for the narrow ratio and the alignment: a column of figures
      // reads down the page when they line up. The widest value it can hold is
      // now "1 h 30 min".
      dataType: ColumnDataType.Numeric,
      gridTrackSize: 'minmax(80px, 0.5fr)'
    },
    text('by', gettext('User')),
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
      callback: (row: GridRow) => this.confirmRevert(row)
    },
    {
      type: 'SHOW_OUTPUT',
      // What it actually is: the engine's own log for this run. "Output" said
      // nothing about whose output or of what.
      text: gettext('Migration log'),
      icon: 'file-text-o',
      showIf: (row: GridRow) => row.hasOutput,
      callback: (row: GridRow) => this.showOutput(row.job)
    }
  ];

  private polling?: ReturnType<typeof setTimeout>;

  constructor(
    private readonly migration: MigrationService,
    private readonly modals: BsModalService,
    private readonly drawers: BottomDrawerService,
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
  /**
   * Opens the migration workspace in the platform's bottom drawer.
   *
   * A drawer rather than a modal because it is the platform's component for
   * exactly this - a wide, scrolling workspace - and it comes at the right size,
   * which a modal did not: that one carried a hand-written width, a breakpoint
   * and a fixed body height, all arithmetic against the viewport.
   *
   * `disableClickOutside` because there is half-filled work in it, and a stray
   * click on the page behind must not throw that away.
   */
  newMigration(): void {
    const drawer = this.drawers.openDrawer(MigrateComponent, {
      disableClickOutside: true
    });

    // The convention the platform's own drawers use: the component settles a
    // promise. It rejects when the operator backs out, which is not an error and
    // has nothing to reload for.
    void drawer.instance.result.then(
      () => void this.load(),
      () => undefined
    );
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

      // keepingIdentity, not a plain set: the grid remembers which rows are
      // expanded by object identity, so replacing every object would collapse a
      // row the reader is in the middle of reading.
      this.rows.update(previous =>
        keepingIdentity(
          previous,
          rows(migrations, reverts, { tenants: byId(tenants.tenants) }).map(row =>
            this.forGrid(row)
          )
        )
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

  /**
   * Asks before undoing, and says what undoing would mean.
   *
   * The one destructive thing this screen does, and it used to happen on a
   * single click of a menu item sitting beside "Migration log". The dialogue
   * runs the undo itself, so a refusal stays on screen with the reason.
   */
  confirmRevert(row: GridRow): void {
    const dialog = this.modals.show(RevertConfirmComponent, {
      class: 'modal-md',
      initialState: { row }
    });

    (dialog.content as RevertConfirmComponent | undefined)?.reverted.subscribe(() => {
      void this.load();
    });
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
      // One status, and only the status: the tag says Reverted rather than
      // Succeeded qualified by something. What cannot be believed about it, and
      // how far a running job has got, are the tooltip's job.
      status: this.say(row.shown.label),
      statusClass: statusTag(row),
      detail: [
        progress &&
          this.say(
            message('roughly at device {{ current }} of {{ total }}', {
              current: progress.current,
              total: progress.total
            })
          ),
        row.shown.caveat && this.say(row.shown.caveat)
      ]
        .filter(Boolean)
        .join(' — '),
      what: this.say(row.what),
      source: row.source,
      target: row.target,
      // The platform's own date formatting, in the reader's language - what the
      // cell shows - beside the raw value the column actually orders by.
      askedFor: this.dates.transform(row.job.queuedAt, 'short') ?? '',
      queuedAt: row.job.queuedAt ?? '',
      took: row.took ? this.say(row.took) : '',
      by: row.job.requestedBy ?? '',
      runId: row.job.runId ?? '',
      revertable: row.revertable,
      cancellable: row.cancellable,
      hasOutput: hasDetail(row),
      scope: this.forScope(row.job),
      revertWarnings: revertWarnings(row).map(warning => this.say(warning)),
      undos: row.undos.map(undo => this.forPanel(undo))
    };
  }

  /**
   * What a migration included, in words.
   *
   * The API's values go through kindLabel rather than being capitalised: a
   * string built at runtime cannot be extracted for translation, and `device` is
   * `Gerät`, which no amount of capitalising would have reached.
   */
  private forScope(job: Job): ScopeRow {
    const scope = scopeOf(job);
    const kinds = (list: string[]) => list.map(kind => this.say(kindLabel(kind))).join(', ');

    return {
      runId: job.runId ?? '',
      relationships: scope.rootOnly
        ? this.say(message('The root object only'))
        : kinds(scope.relationships),
      dataTypes: scope.dataTypes.length
        ? kinds(scope.dataTypes)
        : this.say(message('None')),
      window: this.window(scope.from, scope.to),
      remote: scope.remote
    };
  }

  /** The data window, said only when there is data for it to apply to. */
  private window(from?: string, to?: string): string {
    if (!from && !to) {
      return '';
    }
    if (from && to) {
      return this.say(message('{{ from }} to {{ to }}', { from, to }));
    }
    return from
      ? this.say(message('from {{ from }}', { from }))
      : this.say(message('until {{ to }}', { to: to ?? '' }));
  }

  /** One undo, as its row's expansion shows it. */
  private forPanel(undo: Undo): UndoRow {
    return {
      job: undo.job,
      status: this.say(undo.shown.label),
      statusClass: tagForTone(undo.shown.tone),
      caveat: undo.shown.caveat ? this.say(undo.shown.caveat) : '',
      when: this.dates.transform(undo.job.queuedAt, 'short') ?? '',
      took: undo.took ? this.say(undo.took) : '',
      by: undo.job.requestedBy ?? '',
      runId: undo.job.runId ?? '',
      hasOutput: undo.hasOutput
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
