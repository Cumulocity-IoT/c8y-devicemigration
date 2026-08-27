import { CommonModule } from '@angular/common';
import { Component, EventEmitter, OnInit, Output, signal, ViewEncapsulation } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { BsModalRef } from 'ngx-bootstrap/modal';
import { CoreModule, DateTimePickerComponent } from '@c8y/ngx-components';
import { BsDropdownModule } from 'ngx-bootstrap/dropdown';

import { MigrationService } from '../api/migration.service';
import type { Message } from '../i18n';
import { MigrationFlow, type Choices, type Failure, type Tenant } from './flow';
import { kindLabel } from './labels';
import { dateFromWindow, windowFromDate } from './window';
import { RootPickerComponent } from './root-picker.component';
import type { Readiness } from './steps';

/**
 * Starting a migration: one dialogue, everything in it.
 *
 * A migration is something somebody does occasionally, from the list of what has
 * been done before - so this is a modal over that list rather than a place to
 * navigate to. Inside it there are no steps: there is room for every choice at
 * once, and hiding the target while somebody picks a source only makes them
 * click.
 *
 * The template reads a single immutable snapshot rather than the flow itself.
 * Reading a mutable object from a template is how a screen ends up showing
 * something that changed three actions ago.
 */
interface View {
  tenants: { source: Tenant[]; target: Tenant[] };
  choices: Choices;
  ownerTenant: string;
  preflightPassed: boolean;
  estimate: MigrationFlow['estimate'];
  readiness: Readiness;
  failure?: Failure;

  /** What is wrong with the start date, shown under that field. */
  windowError?: Message;

  busy: boolean;
}

/** The kinds offered as checkboxes, from the flow so there is one list. */
const CHILD_TYPES = MigrationFlow.CHILD_TYPES;
const DATA_TYPES = MigrationFlow.DATA_TYPES;

@Component({
  selector: 'c8y-device-migration',
  standalone: true,
  imports: [
    CommonModule,
    CoreModule,
    FormsModule,
    RootPickerComponent,
    DateTimePickerComponent,
    // For the calendar in the From field's own button. The same arrangement the
    // platform's time-context control uses: a dropdown holding the picker, with
    // insideClick so choosing a date does not close it mid-choice.
    BsDropdownModule
  ],
  templateUrl: './migrate.component.html',
  // Unencapsulated on purpose. The element these rules size - the modal-dialog
  // that ngx-bootstrap creates - is an ancestor of this component, so
  // encapsulated styles cannot reach it, and a plugin's global stylesheet is not
  // guaranteed to be loaded by the shell. Every rule is prefixed with the class
  // this dialogue is opened with, so nothing else in the application is touched.
  encapsulation: ViewEncapsulation.None,
  styles: [
    `
      .device-migration-dialogue.modal-dialog {
        width: calc(100vw - 96px);
        max-width: 1560px;
        margin: 40px auto;
      }
      @media (max-width: 768px) {
        .device-migration-dialogue.modal-dialog {
          width: calc(100vw - 24px);
          margin: 12px auto;
        }
      }
      /* A fixed height, not a maximum: the dialogue is a workspace whose size
         is decided when it opens and never again. Everything inside it - the
         estimate arriving, another data type ticked, a root chosen - would
         otherwise resize it, which moves the button somebody was about to press.
         What does not fit scrolls. */
      .device-migration-dialogue .modal-inner-scroll {
        height: calc(100vh - 240px);
        min-height: 420px;
      }
      /* A floor under what would move, so the list of what is still outstanding
         appearing and disappearing does not push the rest around. The rows
         themselves are always rendered - one per data kind, ticked or not -
         which is what keeps the counts and the ticking from doing it. */
      .device-migration-estimate {
        min-height: 160px;
      }
      /* Fixed, so the columns are where the colgroup says and not where this
         row's content would like them. */
      .device-migration-estimate table {
        table-layout: fixed;
      }
      .device-migration-estimate col.estimate-what {
        width: 30%;
      }
      .device-migration-estimate col.estimate-count {
        width: 14%;
      }
      /* Two panes instead of a bootstrap row, whose negative margins would eat
         the modal body's own padding and leave the content against the edge. */
      .device-migration-panes {
        display: flex;
        flex-wrap: wrap;
        gap: 24px;
        align-items: flex-start;
      }
      .device-migration-panes > .pane-source {
        flex: 3 1 480px;
        min-width: 0;
      }
      .device-migration-panes > .pane-scope {
        flex: 2 1 300px;
        min-width: 0;
      }
    `
  ]
})
export class MigrateComponent implements OnInit {
  /** Emitted once a migration has been accepted, so the list behind can reload. */
  @Output() readonly started = new EventEmitter<string>();

  readonly childTypes = CHILD_TYPES;
  readonly dataTypes = DATA_TYPES;

  private readonly flow: MigrationFlow;

  readonly view = signal<View | undefined>(undefined);
  readonly search = signal('');



  constructor(
    private readonly migration: MigrationService,
    private readonly modal: BsModalRef
  ) {
    this.flow = new MigrationFlow(migration.api);
  }

  /**
   * Whether Start should refuse to be pressed.
   *
   * A method rather than an expression in the template, because the modal's own
   * header reads it before the first snapshot exists and a half-written
   * condition there is not type-checkable.
   */
  blocked(): boolean {
    const view = this.view();
    return !view || view.busy || !view.readiness.ready;
  }

  async ngOnInit(): Promise<void> {
    await this.flow.loadTenants();
    this.refresh();
  }

  /** One of the API's values, as a word on the screen. See labels.ts. */
  label(kind: string): string {
    return kindLabel(kind);
  }

  /**
   * The moment the From field names, for the picker beside it to show.
   *
   * A string, and not a Date: the picker's writeValue takes strings and ignores
   * anything else, so a Date would silently never appear in it. An empty string
   * is ignored too, which is why the template rebuilds the picker when this
   * changes rather than expecting it to clear itself.
   */
  pickedMoment(): string {
    return windowFromDate(dateFromWindow(this.view()?.choices.dateFrom ?? ''));
  }

  /** Whether a kind is ticked. */
  ticked(field: 'childTypes' | 'dataTypes', kind: string): boolean {
    return this.flow.selected(field, kind);
  }

  async toggle(field: 'childTypes' | 'dataTypes', kind: string): Promise<void> {
    this.flow.toggle(field, kind);
    await this.flow.ensureEstimate();
    this.refresh();
  }

  // --- choosing --------------------------------------------------------------

  async chooseSource(tenant: string): Promise<void> {
    this.flow.chooseSource(tenant);
    this.refresh();
  }

  async chooseTarget(tenant: string): Promise<void> {
    await this.flow.chooseTarget(tenant);
    await this.flow.ensureEstimate();
    this.refresh();
  }

  /** What the asset selector chose, or nothing once it was cleared. */
  async chooseRoot(root: { id: string; name: string } | undefined): Promise<void> {
    this.flow.chooseRoot(root);
    await this.flow.ensureEstimate();
    this.refresh();
  }

  /**
   * A date chosen from the picker, written into the field as an instant.
   *
   * The field stays the one place the value lives: the picker is a second way to
   * fill it in, not a second value. Typing `-14d` is still the better answer and
   * the help text says so - a relative window keeps meaning the same thing next
   * week.
   */
  async pickDate(picked: Date | string | undefined): Promise<void> {
    await this.changeScope({ dateFrom: windowFromDate(picked) });
  }

  async changeScope(change: Partial<Choices>): Promise<void> {
    this.flow.changeScope(change);
    await this.flow.ensureEstimate();
    this.refresh();
  }



  // --- starting --------------------------------------------------------------

  /**
   * Starts the migration, and closes only if the service accepted it.
   *
   * Bound to c8y-modal's `close` **input**, not its `onClose` output, and the
   * difference matters: the output fires after the modal has already hidden
   * itself, whereas supplying the input makes closing our decision. A refusal -
   * a full queue, an unsubscribed tenant - therefore leaves the dialogue open
   * with the service's own words on it, instead of vanishing and taking the
   * reason with it.
   *
   * An arrow property because the modal calls it as a plain callback.
   *
   * Once the job exists there is nothing left here to show: the list behind is
   * where a running migration is followed.
   */
  readonly submit = async (): Promise<void> => {
    await this.flow.start();
    this.refresh();

    const job = this.flow.job;
    if (!job?.id) {
      return;
    }

    this.started.emit(job.id);
    this.modal.hide();
  };

  /** Takes a fresh snapshot for the template. */
  private refresh(): void {
    this.view.set({
      tenants: { source: this.flow.sourceTenants, target: this.flow.targetTenants },
      choices: { ...this.flow.choices },
      ownerTenant: this.flow.ownerTenant,
      preflightPassed: this.flow.preflightPassed,
      estimate: this.flow.estimate,
      readiness: this.flow.readiness,
      failure: this.flow.failure,
      windowError: this.flow.windowError,
      busy: this.flow.busy
    });
  }
}
