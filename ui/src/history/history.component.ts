import { CommonModule } from '@angular/common';
import { Component, OnDestroy, OnInit, signal } from '@angular/core';
import {
  BottomDrawerService,
  C8yTranslatePipe,
  type Column,
  ColumnDataType,
  CoreModule,
  DatePipe
} from '@c8y/ngx-components';
import { BsModalService } from 'ngx-bootstrap/modal';

import { type Job } from '../api/client';
import { CREDENTIALS_ISSUED_FRAGMENT, markerFragment } from '../api/fragments';
import { MigrationService } from '../api/migration.service';
import { gettext, message, type Message } from '../i18n';
import { MigrateComponent } from '../migrate/migrate.component';
import { kindLabel } from '../migrate/labels';
import { DeviceMigrationIconComponent } from '../icon/device-migration-icon.component';
import { byId } from '../tenants';
import { saveTextFile } from './download';
import { DeleteConfirmComponent } from './delete-confirm.component';
import { RowActionsComponent } from './row-actions.component';
import { OutputComponent } from './output.component';
import { CollectionProgressComponent } from './collection-progress.component';
import { CutoverComponent } from './cutover.component';
import { RevertConfirmComponent } from './revert-confirm.component';
import { RevertExpiredComponent } from './revert-expired.component';
import { RegisterCertificateComponent } from '../trust/register-certificate.component';
import {
  actionsFor,
  deleteWarnings,
  explain,
  hasDetail,
  type Failure,
  keepingIdentity,
  refreshInterval,
  revertWarnings,
  rows,
  type Scope,
  scopeOf,
  serviceAbsent,
  startTime,
  statusTag,
  tagForTone,
  undosDeleted,
  type GridRow,
  type Row,
  type RowActionKind,
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
  imports: [
    CommonModule,
    CoreModule,
    CollectionProgressComponent,
    DeviceMigrationIconComponent,
    RegisterCertificateComponent
  ],
  templateUrl: './history.component.html',
  // Both are pipes, injected so a row can be built as text; a pipe has to be
  // provided to be injectable.
  providers: [C8yTranslatePipe, DatePipe],
  styles: [
    `
      /* Running text, kept to a readable measure rather than the width of the
         table below it. */
      .intro-text {
        max-width: 68ch;
      }
      /* The emblem leads rather than trails, so it does not need pushing to the
         far edge any more - and at the smaller size it sits beside the text
         instead of towering over it. */
      .device-migration-emblem {
        font-size: 56px;
        opacity: 0.35;
        flex: 0 0 auto;
      }

      /* The platform's .c8y-empty-state sizes its icon through selectors that
         match its own icon classes - .c8y-icon and [class^='dlt-c8y-icon-'] -
         which a custom element does not carry. These are the same values, from
         @c8y/style's _c8y-empty-state.scss, so the two look alike. The colour
         needs nothing: the empty state sets a muted text colour on itself and
         the icon's dark layer is currentColor. */
      .c8y-empty-state .empty-state-emblem {
        display: block;
        margin: 0 auto 16px;
        font-size: 90px;
        opacity: 0.6;
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
   * Sortable, with "Start time" descending to begin with: the useful order for a
   * list of things that happened is the order they happened in, newest first.
   * Source and target are columns of their own rather than a phrase in "What",
   * because a column can be sorted and read down, and a phrase cannot.
   *
   * Status sits fourth, after what moved and where it moved between. Reading a
   * row is "this hierarchy, from here to there - and it worked": the outcome
   * qualifies the sentence, so it comes after it rather than before it.
   */
  readonly columns: Column[] = [
    text('what', gettext('Root object')),
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
      ...text('started', gettext('Start time')),
      // Newest first, and sorted on the raw timestamp rather than the formatted
      // one: the grid sorts on `path`, and a localised date string does not sort
      // chronologically. See timestamp-cell.component.ts.
      path: 'startedOrder',
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
    text('runId', gettext('Run id')),
    {
      // Ours rather than the grid's own row actions, and the reason is in
      // row-actions.component.ts: the grid renders two actions as icons and
      // three as an icon plus a menu, so which affordance a row wore depended on
      // what had happened to that migration. This column is one menu, on every
      // row, always in the same place.
      // Not called "actions": the grid's template switches on a column's name
      // and has a branch of its own for that one, which would render its row
      // actions - of which there are none - instead of this renderer.
      name: 'rowActions',
      header: gettext('Actions'),
      path: 'id',
      dataType: ColumnDataType.Icon,
      sortable: false,
      filterable: false,
      resizable: false,
      // Stays on the right, where the grid's own actions were and where every
      // other Cumulocity list keeps them.
      positionFixed: true,
      gridTrackSize: 'max-content',
      cellRendererComponent: RowActionsComponent
    }
  ];

  /**
   * How each entry of that menu reads, and what it does.
   *
   * The words and the icon here; which of them a given row offers is
   * [`actionsFor`](rows.ts). Splitting it that way is what lets the rule be
   * tested without a translator or a browser, and it is the same split the
   * status column already uses.
   */
  private readonly vocabulary: Record<
    RowActionKind,
    { text: string; icon: string; iconClasses?: string; run: (row: GridRow) => void }
  > = {
    cancel: {
      text: gettext('Cancel'),
      icon: 'ban',
      run: row => void this.cancel(row.job)
    },
    // Offered only while there is something to collect. The service hands the
    // file over once and forgets it, so this disappears the moment it is
    // downloaded - which is also the honest thing to show, because asking again
    // cannot work.
    credentials: {
      text: gettext('Download device credentials'),
      icon: 'download',
      run: row => void this.downloadCredentials(row.job)
    },
    // The other half of a certificate-fleet migration. Offered for as long as
    // the migration stands, not only once: the cutover leaves no record - it
    // creates no job, being two calls rather than a run - so nothing here can
    // say it has already happened. The dialogue answers that by listing the
    // source's truststore live, where a certificate already moved is simply no
    // longer present.
    trustAnchor: {
      text: gettext('Move trust anchor'),
      icon: 'certificate',
      run: row => this.confirmCutover(row)
    },
    revert: {
      text: gettext('Revert'),
      icon: 'undo',
      run: row => this.confirmRevert(row)
    },
    // Not a disabled Revert: this one only explains why undoing is no longer
    // possible, which is the one useful thing left to do about it. Muted, so it
    // does not read as an offer.
    revertExpired: {
      text: gettext('Undo no longer possible'),
      icon: 'undo',
      iconClasses: 'text-muted',
      run: row => this.explainExpiredRevert(row)
    },
    // What it actually is: the engine's own log for this run. "Output" said
    // nothing about whose output or of what.
    log: {
      text: gettext('Migration log'),
      icon: 'file-text-o',
      run: row => this.showOutput(row.job)
    },
    // Forgetting a run, which is not undoing one: this removes the record and
    // leaves both tenants untouched. Last in the menu and furthest from Revert,
    // because the two are destructive in different directions and the click that
    // hits the wrong one is the click that costs the most.
    delete: {
      text: gettext('Remove migration record'),
      icon: 'delete',
      run: row => this.confirmDelete(row)
    }
  };

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
      const [migrations, reverts, tenants, remoteTargets] = await Promise.all([
        this.migration.api.migrations(),
        this.migration.api.reverts(),
        this.migration.api.tenants(),
        // Which certificates this instance still holds, which is what decides
        // whether a migration to a remote tenant can still be undone.
        this.migration.api.remoteTargets()
      ]);

      // keepingIdentity, not a plain set: the grid remembers which rows are
      // expanded by object identity, so replacing every object would collapse a
      // row the reader is in the middle of reading.
      this.rows.update(previous =>
        keepingIdentity(
          previous,
          rows(migrations, reverts, { tenants: byId(tenants.tenants), remoteTargets }).map(row =>
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
   * Why this one cannot be undone any more.
   *
   * It was undoable: the migration is marked and finished and nobody has undone
   * it. What ran out is the certificate, and since that is a deadline rather
   * than a rule about the migration, the row says so instead of quietly
   * dropping the action.
   */
  explainExpiredRevert(row: GridRow): void {
    this.modals.show(RevertExpiredComponent, {
      class: 'modal-md',
      initialState: { row }
    });
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

  /**
   * Moving a certificate fleet's trust anchor, with everything that costs said
   * first.
   *
   * Same shape as the undo confirmation, and for the same reason: the dialogue
   * performs the operation itself, so a refusal stays on screen carrying the
   * service's own words. It matters more here - a refused cutover may have left
   * the source certificate restored, and that message is the only place saying so.
   */
  confirmCutover(row: GridRow): void {
    const dialog = this.modals.show(CutoverComponent, {
      class: 'modal-md',
      initialState: { row }
    });

    (dialog.content as CutoverComponent | undefined)?.movedOver.subscribe(() => {
      void this.load();
    });
  }

  /**
   * Asks before forgetting a run, and says what forgetting means.
   *
   * A separate dialogue from the undo's rather than a shared "are you sure":
   * they destroy different things, and the only sentence worth putting in front
   * of somebody here is which of the two this is.
   */
  confirmDelete(row: GridRow): void {
    const dialog = this.modals.show(DeleteConfirmComponent, {
      class: 'modal-md',
      initialState: { row }
    });

    (dialog.content as DeleteConfirmComponent | undefined)?.removed.subscribe(() => {
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
   * Collects a migration's device credentials and saves them as a file.
   *
   * One chance: the service drops them as it serves them, so anything that goes
   * wrong after the response has arrived loses them. The download is therefore
   * triggered from the body already in hand rather than by navigating the browser
   * to the endpoint - a navigation the platform's session might redirect, or that
   * a popup blocker might swallow, would spend the one read on nothing.
   *
   * The list is refreshed afterwards so the action disappears, which is what
   * tells the operator this cannot be done twice.
   */
  async downloadCredentials(job: Job): Promise<void> {
    if (!job.id) {
      return;
    }
    try {
      const body = await this.migration.api.migrationCredentials(job.id);
      saveTextFile(`credentials-${job.id}.csv`, body);
    } catch (error) {
      this.problem.set(explain(error));
    }
    await this.load();
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
    const grid: GridRow = {
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
      // cell shows - beside the raw value the column actually orders by. Both
      // decided by startTime, which is where the rule is written down and tested.
      started: startTime(row.job, iso => this.dates.transform(iso, 'short') ?? '').shown,
      startedOrder: startTime(row.job, () => '').order,
      took: row.took ? this.say(row.took) : '',
      by: row.job.requestedBy ?? '',
      runId: row.job.runId ?? '',
      isRevert: row.job.kind === 'REVERT',
      scope: this.forScope(row.job),
      revertWarnings: revertWarnings(row).map(warning => this.say(warning)),
      deleteWarnings: deleteWarnings(row).map(warning => this.say(warning)),
      undosDeleted: this.say(undosDeleted(row) ?? ''),
      undos: row.undos.map(undo => this.forPanel(undo)),
      // Filled in below, because both of them need the row they belong to.
      actions: [],
      perform: () => undefined
    };

    // The menu, in words, and the one callback that runs any of it. Functions
    // are invisible to keepingIdentity's comparison, which stringifies a row -
    // so a row that reuses its previous object keeps that object's callback,
    // closed over a row with identical content. The entries themselves are
    // plain text and do take part in the comparison, which is right: a row whose
    // offers changed has changed.
    grid.actions = actionsFor(row).map(kind => {
      const { text, icon, iconClasses } = this.vocabulary[kind];
      // The delete says what it removes, and on an undo listed of its own what
      // it removes is that undo's record - the migration's is not this row's to
      // speak for, and may not be on record at all.
      const named = kind === 'delete' && grid.isRevert ? gettext('Remove undo record') : text;
      return { kind, text: this.say(named), icon, iconClasses };
    });
    grid.perform = kind => this.vocabulary[kind].run(grid);

    return grid;
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
      marker: markerFragment(job.runId ?? ''),
      relationships: scope.rootOnly
        ? this.say(message('The root object only'))
        : kinds(scope.relationships),
      dataTypes: scope.dataTypes.length
        ? kinds(scope.dataTypes)
        : this.say(message('None')),
      window: this.window(scope.from, scope.to),
      remote: scope.remote,
      credentials: this.credentials(scope),
      selfService: scope.selfService
    };
  }

  /**
   * How the devices of this migration got credentials.
   *
   * Said even when the answer is "none", which is the point: the download
   * action disappears once the file has been collected, so without a line here
   * there is nothing left anywhere to distinguish a migration that provisioned
   * credentials from one that did not.
   */
  private credentials(scope: Scope): string {
    if (scope.certificateFleet) {
      // Said as unfinished business rather than as a fact, because that is what
      // it is: the objects are across, the fleet is not, and the action beside
      // this row is the rest of the move. Nothing here can tell whether the
      // anchor has already moved - a cutover leaves no record - so this reads
      // the same before and after, and the dialogue shows the live truth.
      return this.say(message('They keep their certificates — move the trust anchor to complete it'));
    }
    if (scope.selfService) {
      return this.say(message('Each device collects its own'));
    }
    if (scope.credentialsIssued > 0) {
      return this.say(
        message('{{ count }} device users created', { count: scope.credentialsIssued })
      );
    }
    return this.say(message('None'));
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
