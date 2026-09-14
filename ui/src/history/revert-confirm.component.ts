import { CommonModule } from '@angular/common';
import { Component, EventEmitter, Input, Output, signal } from '@angular/core';
import { CoreModule } from '@c8y/ngx-components';
import { BsModalRef } from 'ngx-bootstrap/modal';

import { MigrationService } from '../api/migration.service';
import { explain, type Failure, type GridRow } from './rows';

/**
 * Asking before undoing a migration, and saying what that would mean.
 *
 * A revert deletes things in somebody's tenant and the old flow did it on one
 * click, from a menu item two pixels from *Migration log*. It is the one
 * destructive thing this screen can do, and it is the only one that was never
 * confirmed.
 *
 * The summary cannot promise a number. The service keeps no plan to ask for -
 * there is no dry run, and the record carries no count of what the migration
 * created - so inventing "12 objects" would be a guess presented as a fact. What
 * it states instead is exactly what the undo does: everything carrying this
 * run's marker, in this tenant. The marker is shown, because that is what makes
 * the promise checkable, and it is what somebody would paste into an inventory
 * query to see the objects for themselves before agreeing.
 */
@Component({
  selector: 'c8y-device-migration-revert-confirm',
  standalone: true,
  imports: [CommonModule, CoreModule],
  template: `
    <c8y-modal
      [title]="'Undo this migration?' | translate"
      [labels]="{ ok: 'Undo migration' | translate, cancel: 'Cancel' | translate }"
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
            'This deletes from {{ target }} everything the migration created. The devices in {{ source }} are not touched.'
              | translate: { target: row.target, source: row.source }
          }}
        </p>

        <dl class="revert-summary">
          <dt>{{ 'Migration root' | translate }}</dt>
          <dd>{{ row.what }}</dd>

          <dt>{{ 'Target tenant' | translate }}</dt>
          <dd>{{ row.target }}</dd>

          <dt>{{ 'Migrated' | translate }}</dt>
          <dd>{{ row.askedFor }}</dd>

          <dt>{{ 'Relationships' | translate }}</dt>
          <dd>{{ row.scope.relationships }}</dd>

          <dt>{{ 'Device data' | translate }}</dt>
          <dd>
            {{ row.scope.dataTypes }}
            @if (row.scope.window) {
              <span class="text-muted">· {{ row.scope.window }}</span>
            }
          </dd>

          <dt>{{ 'Run marker' | translate }}</dt>
          <dd><code>{{ row.scope.marker }}</code></dd>
        </dl>

        <p class="text-muted small">
          {{
            'Everything carrying that marker is deleted. Nothing else is, and objects already gone are skipped.'
              | translate
          }}
        </p>

        <!--
          No padding utility on these. The alert class draws its icon as an
          absolutely positioned ::before in a 44px gutter, and the padding it
          ships with is what keeps the text out of it - overriding that with p-8
          put the words underneath the warning triangle.
        -->
        @for (warning of row.revertWarnings; track warning) {
          <div class="alert alert-warning m-b-8">{{ warning }}</div>
        }
      </div>
    </c8y-modal>
  `,
  styles: [
    `
      .revert-summary {
        display: grid;
        grid-template-columns: max-content minmax(0, 1fr);
        gap: 6px 20px;
        align-items: baseline;
        margin: 16px 0;
        font-size: 13px;
      }
      .revert-summary dt {
        font-weight: 400;
        color: var(--c8y-text-muted, #78849b);
        white-space: nowrap;
      }
      .revert-summary dd {
        margin: 0;
        min-width: 0;
        overflow-wrap: anywhere;
      }
    `
  ]
})
export class RevertConfirmComponent {
  /** Set by whoever opens this. */
  @Input({ required: true }) row!: GridRow;

  /** Fired once the service has accepted the undo, so the list can reload. */
  @Output() readonly reverted = new EventEmitter<void>();

  readonly failure = signal<Failure | undefined>(undefined);

  constructor(
    private readonly migration: MigrationService,
    private readonly modal: BsModalRef
  ) {}

  /**
   * Starts the undo, and closes only if the service accepted it.
   *
   * Bound to c8y-modal's `close` input rather than its `onClose` output, the
   * same way the migration dialogue does it: supplying the input makes closing
   * our decision, so a refusal leaves this open with the service's own words on
   * it instead of vanishing and taking the reason away.
   */
  readonly submit = async (): Promise<void> => {
    this.failure.set(undefined);

    try {
      await this.migration.api.startRevert({ jobId: this.row.job.id ?? '' });
    } catch (error) {
      this.failure.set(explain(error));
      return;
    }

    this.reverted.emit();
    this.modal.hide();
  };
}
