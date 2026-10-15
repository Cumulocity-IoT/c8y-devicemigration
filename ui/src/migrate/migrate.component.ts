import { CommonModule } from '@angular/common';
import { Component, OnInit, signal } from '@angular/core';
import { FormsModule } from '@angular/forms';
import {
  BottomDrawerRef,
  C8yTranslatePipe,
  CoreModule,
  DateTimePickerComponent
} from '@c8y/ngx-components';
import { BsDropdownModule } from 'ngx-bootstrap/dropdown';
import { BsModalService } from 'ngx-bootstrap/modal';

import type { PreflightConflict } from '../api/client';
import { MigrationService } from '../api/migration.service';
import { gettext, type Message } from '../i18n';
import {
  MigrationFlow,
  type Choices,
  type CredentialChoice,
  type Failure,
  type SelectableItem
} from './flow';
import { HelpComponent } from './help.component';
import { hierarchyNote, type Note } from './hierarchy';
import { kindLabel } from './labels';
import { dateFromWindow, windowFromDate } from './window';
import { RemoteTargetComponent } from './remote-target.component';
import { RootPickerComponent } from './root-picker.component';
import type { Readiness } from './steps';

/**
 * Starting a migration: one drawer, everything in it.
 *
 * A migration is something somebody does occasionally, from the list of what has
 * been done before - so this is a drawer over that list rather than a place to
 * navigate to. Inside it there are no steps: there is room for every choice at
 * once, and hiding the target while somebody picks a source only makes them
 * click.
 *
 * The template reads a single immutable snapshot rather than the flow itself.
 * Reading a mutable object from a template is how a screen ends up showing
 * something that changed three actions ago.
 */
interface View {
  /**
   * The options each select offers, and which of them is chosen.
   *
   * The chosen one is the *item*, not its value: c8y-select's writeValue takes a
   * SelectableItem and its change emits one, so the dialogue has to hand it the
   * object rather than the tenant id it keeps.
   */
  tenants: { source: SelectableItem[]; target: SelectableItem[] };
  selectedSource?: SelectableItem;
  selectedTarget?: SelectableItem;
  choices: Choices;
  ownerTenant: string;
  preflightPassed: boolean;
  estimate: MigrationFlow['estimate'];
  readiness: Readiness;
  failure?: Failure;

  /** What is wrong with the start date, shown under that field. */
  windowError?: Message;

  /** The remote target just established, so its CA can be shown to register. */
  remoteTarget?: MigrationFlow['remoteTarget'];

  /**
   * Whether a credential can be issued into the target now chosen - false for a
   * remote target, which is reached with a device access token that cannot
   * register a device user.
   *
   * On the view rather than read through the flow, because the template only
   * ever sees this snapshot - and it has to be re-read on every refresh anyway,
   * since choosing a remote target changes the answer.
   */
  canIssueCredentials: boolean;

  /**
   * What would be migrated, as a sentence, for while the relationship choice is
   * folded away. Derived on every refresh so it cannot outlive the choices it
   * describes.
   */
  hierarchyNote: Note;

  /**
   * The devices the target tenant already has under the same external identity,
   * which is what makes provisioning a device user per device unavailable.
   *
   * Listed rather than counted: the answer is to go and look at those devices in
   * the two tenants, and a number gives nobody anywhere to start.
   */
  credentialConflicts: PreflightConflict[];

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
    HelpComponent,
    DateTimePickerComponent,
    // For the calendar in the From field's own button. The same arrangement the
    // platform's time-context control uses: a dropdown holding the picker, with
    // insideClick so choosing a date does not close it mid-choice.
    BsDropdownModule
  ],
  // A pipe has to be provided to be injectable. One string is built as text
  // rather than rendered through the template - the conjunction inside the
  // hierarchy note, which a pure function needs handed to it.
  providers: [C8yTranslatePipe],
  templateUrl: './migrate.component.html',
  // The drawer lays its content out as a card, and expects the content to be
  // the card's own contents rather than a box inside it.
  host: { class: 'd-contents' },
  styles: [
    `
      /* No sizing here. The bottom drawer is the right size by definition, and
         card-header / inner-scroll / card-footer decide what scrolls - so there
         is no width, margin, breakpoint or fixed body height to work out against
         the viewport, and no ViewEncapsulation.None to reach an ancestor with. */

      /* A floor under what would move, so the list of what is still outstanding
         appearing and disappearing does not push the rest around. The rows
         themselves are always rendered - one per data kind, ticked or not -
         which is what keeps the counts and the ticking from doing it. */
      /* The preview is a panel, not part of the form: a light surface and a
         floor under its height, so counts arriving never resize the dialogue
         somebody is reading. */
      .device-migration-preview {
        border: 1px solid var(--c8y-component-border-color, #d6dedd);
        border-radius: 4px;
        padding: 8px 16px 16px;
        min-height: 150px;
      }
      /* Two columns: the counts answer two questions - how much hierarchy, how
         much data - and reading one does not require the other. */
      .device-migration-preview .preview-columns {
        display: grid;
        grid-template-columns: 1fr 1fr;
        gap: 0 32px;
      }
      @media (max-width: 767px) {
        .device-migration-preview .preview-columns {
          grid-template-columns: 1fr;
        }
      }
      /* Fixed, so the figures line up across both tables rather than sitting
         wherever each table's own longest label puts them. */
      .device-migration-preview table {
        table-layout: fixed;
      }
      .device-migration-preview td:first-child {
        width: 50%;
      }
      .device-migration-preview td + td {
        width: 22%;
      }

      /* Two panes instead of a bootstrap row, whose negative margins would eat
         the drawer body's own padding and leave the content against the edge. */
      .device-migration-panes {
        display: flex;
        flex-wrap: wrap;
        gap: 24px;
        align-items: flex-start;
      }
      /* Equal halves: the tenants have a row of their own, so the two panes
         carry one question each. */
      .device-migration-panes > .pane-source,
      .device-migration-panes > .pane-scope {
        flex: 1 1 360px;
        min-width: 0;
      }

      /* The two tenants, same width whatever is in them: a wider box for one of
         them would read as the more important half of one question asked
         twice. */
      .device-migration-tenants {
        display: grid;
        grid-template-columns: 1fr 1fr;
        gap: 24px;
        align-items: start;
      }
      /* Nothing here aligns the two labels: @c8y/style already does it for a
         label holding the platform's help button - label:has(.btn-help) is a
         flex row, and label .btn-help is 14px square. */
      @media (max-width: 767px) {
        .device-migration-tenants {
          grid-template-columns: 1fr;
        }
      }
    `
  ]
})
export class MigrateComponent implements OnInit {
  readonly childTypes = CHILD_TYPES;
  readonly dataTypes = DATA_TYPES;

  private readonly flow: MigrationFlow;

  readonly view = signal<View | undefined>(undefined);
  readonly search = signal('');



  /**
   * Settles when the drawer is done: the new job's id, or a rejection if the
   * operator backed out. The convention the platform's own drawers use, and what
   * the list awaits to know whether to reload.
   */
  readonly result: Promise<string>;
  private started_!: (jobId: string) => void;
  private cancelled_!: () => void;

  constructor(
    private readonly migration: MigrationService,
    private readonly drawer: BottomDrawerRef<MigrateComponent>,
    private readonly translator: C8yTranslatePipe,
    private readonly modals: BsModalService
  ) {
    this.flow = new MigrationFlow(migration.api);
    this.result = new Promise<string>((resolve, reject) => {
      this.started_ = resolve;
      this.cancelled_ = reject;
    });
  }

  /**
   * Whether Start should refuse to be pressed.
   *
   * A method rather than an expression in the template, because the footer reads
   * it before the first snapshot exists and a half-written condition there is
   * not type-checkable.
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
    await this.settle();
  }

  // --- choosing --------------------------------------------------------------

  async chooseSource(tenant: string): Promise<void> {
    this.flow.chooseSource(tenant);
    this.refresh();
  }

  async chooseTarget(tenant: string): Promise<void> {
    await this.flow.chooseTarget(tenant);
    await this.settle();
  }

  /**
   * The value the target dropdown carries for "a tenant not listed here".
   *
   * A sentinel in the same list rather than a control beside it: it answers the
   * same question - which tenant - so it belongs among the answers. As a
   * checkbox it would read as a mode the whole dialogue is in.
   */
  readonly REMOTE = '__remote__';

  /** Where the documentation lives, for the line at the top. */
  readonly documentation = 'https://github.com/Cumulocity-IoT/c8y-devicemigration';

  /**
   * Choosing a target, which may be choosing to connect one.
   *
   * The sentinel opens the modal rather than selecting anything. Nothing is
   * changed until that modal says a connection was proven, so cancelling it
   * leaves the previous target exactly as it was.
   */
  /** The select hands over the whole item; what the flow keeps is its value. */
  async chooseSourceOption(item: SelectableItem | undefined): Promise<void> {
    await this.chooseSource(item?.value ?? '');
  }

  async chooseTargetOption(item: SelectableItem | undefined): Promise<void> {
    const value = item?.value;
    if (value === this.REMOTE) {
      this.connectRemoteTarget();
      return;
    }
    await this.chooseTarget(value ?? '');
  }

  /** Opens the modal that establishes a certificate connection. */
  private connectRemoteTarget(): void {
    const modal = this.modals.show(RemoteTargetComponent, {
      class: 'modal-md',
      ignoreBackdropClick: true,
      initialState: { flow: this.flow }
    });
    const connected = (modal.content as RemoteTargetComponent | undefined)?.connected;
    connected?.subscribe(() => {
      void this.settle();
    });
    // Whatever happens to the modal, the dialogue behind it re-reads the flow:
    // a cancelled connection may still have established one, and the dropdown
    // has to stop showing the sentinel either way.
    modal.onHidden?.subscribe(() => this.refresh());
  }

  /** What the asset selector chose, or nothing once it was cleared. */
  async chooseRoot(root: { id: string; name: string } | undefined): Promise<void> {
    this.flow.chooseRoot(root);
    await this.settle();
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
    await this.settle();
  }

  /**
   * Whether the relationship checkboxes are on screen.
   *
   * The summarising sentence stays either way, so folding them back never hides
   * what somebody just chose - it goes on saying it, in words.
   */
  readonly hierarchyOpen = signal(false);

  /** A catalogue string, for the one place a template cannot reach. */
  private say(text: string): string {
    return this.translator.transform(text);
  }

  toggleHierarchy(): void {
    this.hierarchyOpen.update(open => !open);
  }

  /**
   * Everything the service still has to be asked after a choice, then a render.
   *
   * One call rather than two at every call site, because the two have to happen
   * together: the estimate says how much would move and the credential check
   * says whether the chosen way of authenticating is available, and a screen
   * that refreshed after one but not the other would offer a Start that the
   * service refuses.
   */
  private async settle(): Promise<void> {
    await this.flow.ensureEstimate();
    await this.flow.ensureCredentialCheck();
    this.refresh();
  }

  /**
   * Answering the credentials question. No estimate round trip: the count it
   * needs is already in the estimate the dialogue has, whatever the answer -
   * this changes what is created alongside the migration, not what moves.
   *
   * It can still cost a round trip of its own. Provisioning a device user per
   * device is the one answer whose availability depends on what the target
   * already has, so choosing it asks.
   */
  async chooseCredentials(choice: CredentialChoice): Promise<void> {
    this.flow.chooseCredentials(choice);
    await this.settle();
  }


  // --- starting --------------------------------------------------------------

  /**
   * Starts the migration, and closes only if the service accepted it.
   *
   * Closing is this component's decision, not the drawer's: the drawer has no
   * "close only if it worked" hook, and nothing closes it but these two methods.
   * So a refusal - a full queue, an unsubscribed tenant - leaves the drawer open
   * with the service's own words on it, instead of vanishing and taking the
   * reason with it.
   *
   * Still an arrow property: the template binds it directly.
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

    this.started_(job.id);
    void this.drawer.close();
  };

  /** Backing out. Nothing has been asked of either tenant, so nothing to undo. */
  cancel(): void {
    this.cancelled_();
    void this.drawer.close();
  }

  /** Takes a fresh snapshot for the template. */
  private refresh(): void {
    const sourceOptions = this.flow.sourceOptions;
    // The remote case is the last option rather than a control beside the list:
    // it answers the same question, so it belongs among the answers.
    const targetOptions = [
      ...this.flow.targetOptions,
      { label: this.say(gettext('Connect a different tenant…')), value: this.REMOTE }
    ];
    const chosenTarget = this.flow.choices.remoteTargetId
      ? this.REMOTE
      : this.flow.choices.targetTenant;

    this.view.set({
      tenants: { source: sourceOptions, target: targetOptions },
      selectedSource: sourceOptions.find(item => item.value === this.flow.choices.sourceTenant),
      selectedTarget: targetOptions.find(item => item.value === chosenTarget),
      choices: { ...this.flow.choices },
      ownerTenant: this.flow.ownerTenant,
      preflightPassed: this.flow.preflightPassed,
      estimate: this.flow.estimate,
      readiness: this.flow.readiness,
      failure: this.flow.failure,
      windowError: this.flow.windowError,
      remoteTarget: this.flow.remoteTarget,
      canIssueCredentials: this.flow.canIssueCredentials,
      // The conjunction is translated here because hierarchyNote is a pure
      // function and this is where the translator is.
      hierarchyNote: hierarchyNote(this.flow.choices, this.say(gettext('and'))),
      credentialConflicts: this.flow.credentialConflicts,
      busy: this.flow.busy
    });
  }
}
