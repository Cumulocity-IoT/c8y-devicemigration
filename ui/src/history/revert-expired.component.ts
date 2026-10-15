import { CommonModule } from '@angular/common';
import { Component, Input } from '@angular/core';
import { CoreModule } from '@c8y/ngx-components';

import type { GridRow } from './rows';

/**
 * Why a remote-target migration can no longer be undone from here.
 *
 * This is the other half of the muted *Undo no longer possible* action. The
 * action alone says that it cannot be done; somebody who wanted to do it needs
 * to know why, and whether anything else would work — an explanation nobody can
 * get from an action that has simply disappeared.
 *
 * The reason is a deadline, not a rule about the migration: a device token
 * deletes only what it owns, the objects are owned by the identity that created
 * them, and that identity is the certificate. So the undo is possible for
 * exactly as long as the certificate is, and no longer.
 */
@Component({
  selector: 'c8y-device-migration-revert-expired',
  standalone: true,
  imports: [CommonModule, CoreModule],
  template: `
    <c8y-modal
      [title]="'Undo no longer possible' | translate"
      [labels]="{ ok: '', cancel: 'Close' | translate }"
    >
      <div class="p-24">
        <p>
          {{
            'This migration went to {{ target }}, a tenant this service is not subscribed to. It reached it with a certificate it generated for that one migration, and undoing it means presenting the same certificate again — a migration deletes only what its own identity created.'
              | translate: { target: row.target }
          }}
        </p>
        <p>
          {{
            'That certificate is gone. It is valid for six hours and is never written down, so it also does not survive a restart of the service. There is no way to mint another that the target tenant would recognise as the same identity.'
              | translate
          }}
        </p>

        <p class="text-medium m-b-4">{{ 'What still works' | translate }}</p>
        <ul>
          <li>
            {{
              'Undo it from the command line, with a session for the target tenant: c8y-devicemigration revert --runId {{ runId }} --session target.json'
                | translate: { runId: row.scope.runId }
            }}
          </li>
          <li>
            {{
              'Or, in the target tenant itself, delete the objects carrying the run marker below.'
                | translate
            }}
          </li>
        </ul>

        <p class="text-muted small m-b-0">
          {{ 'Run marker' | translate }}:
          <code>{{ row.scope.marker }}</code>
        </p>
      </div>
    </c8y-modal>
  `
})
export class RevertExpiredComponent {
  /** Set by whoever opens this. */
  @Input({ required: true }) row!: GridRow;
}
