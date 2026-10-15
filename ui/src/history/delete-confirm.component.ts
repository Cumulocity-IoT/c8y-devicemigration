import { CommonModule } from '@angular/common';
import { Component, EventEmitter, Input, Output, signal } from '@angular/core';
import { CoreModule } from '@c8y/ngx-components';
import { BsModalRef } from 'ngx-bootstrap/modal';

import { MigrationService } from '../api/migration.service';
import { gettext } from '../i18n';
import { explain, type Failure, type GridRow } from './rows';

/**
 * Asking before forgetting a run, and being exact about what "forget" means.
 *
 * Two destructive things sit next to each other in the same row menu, and they
 * destroy different things: *Revert* deletes what the migration created in the
 * target tenant, and this deletes the record that says a migration ever
 * happened. Confusing them would be easy and expensive in both directions, so
 * the first sentence here is the difference rather than a "this cannot be
 * undone" that would be true of either.
 *
 * The undos are named rather than mentioned in passing. Deleting a migration
 * takes them with it - the service does that, because a revert record names its
 * migration by run id and nothing else - and somebody who came to remove one row
 * should not learn afterwards that two went.
 */
@Component({
  selector: 'c8y-device-migration-delete-confirm',
  standalone: true,
  imports: [CommonModule, CoreModule],
  template: `
    <c8y-modal
      [title]="title | translate"
      [labels]="{ ok: confirmation | translate, cancel: 'Cancel' | translate }"
      [close]="submit"
    >
      <div class="p-24">
        @if (failure(); as problem) {
          <div class="alert alert-danger">
            <strong>{{ problem.title | translate }}</strong>
            <p class="m-0">{{ problem.detail | translate }}</p>
          </div>
        }

        <p>
          {{
            'This removes the record of what happened. It deletes nothing in either tenant: whatever was migrated stays exactly where it is.'
              | translate
          }}
        </p>

        <!--
          Two branches rather than a ternary label, because an undo and a
          migration are described by different things: "Root object: undo of
          20260101T…" reads as a mistake, and the run id an undo would repeat in
          a row of its own is the only thing identifying it anyway.
        -->
        <dl class="delete-summary">
          @if (row.isRevert) {
            <dt>{{ 'Undo of run' | translate }}</dt>
            <dd><code>{{ row.runId }}</code></dd>
          } @else {
            <dt>{{ 'Root object' | translate }}</dt>
            <dd>{{ row.what }}</dd>
          }

          @if (row.target) {
            <dt>{{ 'Target tenant' | translate }}</dt>
            <dd>{{ row.target }}</dd>
          }

          <dt>{{ 'Start time' | translate }}</dt>
          <dd>{{ row.started }}</dd>

          @if (!row.isRevert && row.runId) {
            <dt>{{ 'Run id' | translate }}</dt>
            <dd><code>{{ row.runId }}</code></dd>
          }
        </dl>

        @if (row.undosDeleted) {
          <p class="text-muted small">{{ row.undosDeleted }}</p>
        }

        <!--
          The alerts carry their own padding: the class draws its icon as an
          absolutely positioned ::before in a 44px gutter, and a padding utility
          on top of it puts the words underneath the triangle.
        -->
        @for (warning of row.deleteWarnings; track warning) {
          <div class="alert alert-warning m-b-8">{{ warning }}</div>
        }
      </div>
    </c8y-modal>
  `,
  styles: [
    `
      .delete-summary {
        display: grid;
        grid-template-columns: max-content minmax(0, 1fr);
        gap: 6px 20px;
        align-items: baseline;
        margin: 16px 0;
        font-size: 13px;
      }
      .delete-summary dt {
        font-weight: 400;
        color: var(--c8y-text-muted, #78849b);
        white-space: nowrap;
      }
      .delete-summary dd {
        margin: 0;
        min-width: 0;
        overflow-wrap: anywhere;
      }
    `
  ]
})
export class DeleteConfirmComponent {
  /** Set by whoever opens this. */
  @Input({ required: true }) row!: GridRow;

  /** Fired once the service has removed the records, so the list can reload. */
  @Output() readonly removed = new EventEmitter<void>();

  readonly failure = signal<Failure | undefined>(undefined);

  /**
   * The title, which depends on which kind of row this is.
   *
   * Chosen here rather than by a ternary inside `| translate`: the extraction
   * that keeps the catalogues honest looks for a literal immediately before the
   * pipe, so `(a ? 'x' : 'y') | translate` is a string it cannot see - and a
   * string it cannot see is one that quietly stays English in German. `gettext`
   * is the marker it does read.
   */
  get title(): string {
    return this.row.isRevert
      ? gettext('Remove this undo from the list?')
      : gettext('Remove this migration from the list?');
  }

  /** The button that does it, worded the same as the menu entry that got here. */
  get confirmation(): string {
    return this.row.isRevert
      ? gettext('Remove undo record')
      : gettext('Remove migration record');
  }

  constructor(
    private readonly migration: MigrationService,
    private readonly modal: BsModalRef
  ) {}

  /**
   * Deletes the records, and closes only if the service agreed.
   *
   * Bound to c8y-modal's `close` input rather than its `onClose` output, like
   * every other dialogue here: closing stays our decision, so a refusal - a job
   * that started running between the menu opening and the button being pressed,
   * which the service answers with a 409 - leaves this open carrying the reason.
   */
  readonly submit = async (): Promise<void> => {
    this.failure.set(undefined);
    const id = this.row.job.id ?? '';

    try {
      // A revert listed on its own deletes only itself; anything else is a
      // migration, and the service takes its undos along.
      await (this.row.isRevert
        ? this.migration.api.deleteRevert(id)
        : this.migration.api.deleteMigration(id));
    } catch (error) {
      this.failure.set(explain(error));
      return;
    }

    this.removed.emit();
    this.modal.hide();
  };
}
