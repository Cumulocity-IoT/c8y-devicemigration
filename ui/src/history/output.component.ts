import { CommonModule } from '@angular/common';
import { Component, Input } from '@angular/core';
import { CoreModule } from '@c8y/ngx-components';
import { BsModalRef } from 'ngx-bootstrap/modal';

import type { Job } from '../api/client';

/**
 * What the engine printed, for one job.
 *
 * A dialogue rather than a row that unfolds inside the table: the log is dozens
 * of lines and a table is a poor place to put them, and the grid the list is
 * drawn with has no room for a row of its own anyway.
 */
@Component({
  selector: 'c8y-device-migration-output',
  standalone: true,
  imports: [CommonModule, CoreModule],
  template: `
    <c8y-modal [title]="'Output' | translate" [labels]="{ ok: '', cancel: 'Close' | translate }">
      <div class="p-24">
        @if (job.error) {
          <!-- The service's own words, which is what somebody would search for. -->
          <div class="alert alert-danger">{{ job.error }}</div>
        }

        <p class="text-muted small">
          {{
            'The last {{ lines }} lines the engine printed. The whole output is in the microservice log.'
              | translate: { lines: job.progress?.length || 0 }
          }}
        </p>
        <pre class="m-0" style="max-height: 60vh; overflow: auto">{{
          job.progress?.join('\n')
        }}</pre>
      </div>
    </c8y-modal>
  `
})
export class OutputComponent {
  /** Set by whoever opens this. */
  @Input({ required: true }) job!: Job;

  constructor(readonly modal: BsModalRef) {}
}
