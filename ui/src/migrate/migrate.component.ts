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

import type { PreflightConflict } from '../api/client';
import { MigrationService } from '../api/migration.service';
import { gettext, type Message } from '../i18n';
import {
  MigrationFlow,
  type Choices,
  type CredentialChoice,
  type Failure,
  type Tenant
} from './flow';
import { hierarchyNote, type Note } from './hierarchy';
import { kindLabel } from './labels';
import { dateFromWindow, windowFromDate } from './window';
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
  tenants: { source: Tenant[]; target: Tenant[] };
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
      /* No sizing here any more. This used to carry the modal-dialog's width,
         its margins, a breakpoint, and a fixed height for the scrolling body -
         all of it arithmetic against the viewport, and all of it unencapsulated,
         because the element being sized was an ancestor this component could not
         otherwise reach. The bottom drawer is that size by definition, and
         card-header / inner-scroll / card-footer decide what scrolls, so every
         one of those rules is gone and so is ViewEncapsulation.None. */

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
    private readonly translator: C8yTranslatePipe
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

  /** Whether the operator is aiming at a tenant this instance is not subscribed to. */
  readonly remoteMode = signal(false);
  readonly remoteHost = signal('');
  readonly remoteTenant = signal('');

  toggleRemote(on: boolean): void {
    this.remoteMode.set(on);
    if (!on) {
      // Back to the dropdown: forget any remote choice so Start is not gated on
      // a connection nobody is using.
      void this.chooseTarget(this.view()?.choices.targetTenant ?? '');
    }
  }

  /** Establishes the connection and shows the CA to register. */
  async establishRemote(): Promise<void> {
    await this.flow.establishRemoteTarget(this.remoteHost().trim(), this.remoteTenant().trim());
    this.refresh();
  }

  /** Proves it once the CA has been registered on the target. */
  async checkRemote(): Promise<void> {
    await this.flow.checkRemoteTarget();
    await this.settle();
  }

  /**
   * Saves the CA as a file, for the manual path.
   *
   * The platform's own Trusted certificates screen takes a file, not text, which
   * is the friction this whole panel exists to avoid - but somebody on a target
   * without the plugin still needs the file, so it is offered under "another way".
   */
  /** Whether the certificate was just copied, for the button to say so. */
  readonly certificateCopied = signal(false);

  /** Copies the CA to the clipboard, the quickest way to feed the paste field. */
  async copyCertificate(): Promise<void> {
    const pem = this.view()?.remoteTarget?.caCertificate;
    if (!pem) {
      return;
    }
    await navigator.clipboard.writeText(pem);
    this.certificateCopied.set(true);
    setTimeout(() => this.certificateCopied.set(false), 2000);
  }

  downloadCertificate(): void {
    const pem = this.view()?.remoteTarget?.caCertificate;
    if (!pem) {
      return;
    }
    const url = URL.createObjectURL(new Blob([pem], { type: 'application/x-pem-file' }));
    const link = document.createElement('a');
    link.href = url;
    link.download = `migration-${this.view()?.choices.targetTenant ?? 'target'}.pem`;
    link.click();
    URL.revokeObjectURL(url);
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
    this.view.set({
      tenants: { source: this.flow.sourceTenants, target: this.flow.targetTenants },
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
