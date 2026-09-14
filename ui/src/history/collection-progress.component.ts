import { CommonModule } from '@angular/common';
import { Component, Input, OnInit, signal } from '@angular/core';
import { CoreModule } from '@c8y/ngx-components';

import { type Job } from '../api/client';
import { CREDENTIALS_ISSUED_FRAGMENT, markerFragment } from '../api/fragments';
import { MigrationService } from '../api/migration.service';

/**
 * How far a device-self-service migration has actually got.
 *
 * Option B hands nothing over at migration time: each device asks for its own
 * credential afterwards, on its own schedule. So a finished job says nothing
 * about whether the fleet has moved, and without this the screen cannot tell a
 * migration that is working from one whose devices have never managed to reach
 * the endpoint. A count stuck at zero a day later is the whole signal.
 *
 * Counted rather than recorded: the service stores nothing per device beyond a
 * fragment on the object it issued for, and that is deliberate - the passwords
 * are not kept, so neither is a list of who has one. Two inventory reads through
 * the proxy answer it without the service growing any state.
 *
 * A component rather than a method on the list because the expansion template is
 * only rendered while a row is open. That makes `ngOnInit` fire exactly when
 * somebody looks, which is the lazy loading the list itself must not do: it
 * re-reads every few seconds, and two reads per self-service row riding along
 * with that would be a query storm for a number nobody is watching.
 */
@Component({
  selector: 'c8y-device-migration-collection-progress',
  standalone: true,
  imports: [CommonModule, CoreModule],
  template: `
    @if (failed()) {
      <span class="text-muted">
        {{ 'Could not read how many devices have collected.' | translate }}
        <button class="btn btn-link btn-sm p-0" type="button" (click)="load()">
          {{ 'Try again' | translate }}
        </button>
      </span>
    } @else if (counted()) {
      <span>
        {{
          '{{ collected }} of {{ total }} devices have collected their credentials'
            | translate: { collected: collected(), total: total() }
        }}
      </span>
      @if (collected() === 0) {
        <span class="text-muted">
          ·
          {{
            'None yet. The devices ask on their own schedule, so this stays at zero until the first one reaches the endpoint.'
              | translate
          }}
        </span>
      }
    } @else {
      <span class="text-muted">{{ 'Counting…' | translate }}</span>
    }
  `
})
export class CollectionProgressComponent implements OnInit {
  /** The migration whose devices are collecting. */
  @Input({ required: true }) job!: Job;

  readonly collected = signal(0);
  readonly total = signal(0);
  readonly counted = signal(false);
  readonly failed = signal(false);

  constructor(private readonly migration: MigrationService) {}

  ngOnInit(): void {
    void this.load();
  }

  /**
   * Counts what the migration created and how much of it has a credential.
   *
   * Both reads are of the **target** tenant, through the inventory proxy, and
   * both count objects rather than fetching them - the answer is two numbers and
   * nothing else crosses the wire.
   */
  async load(): Promise<void> {
    const tenant = this.job.request?.targetTenant;
    const runId = this.job.runId;
    if (!tenant || !runId) {
      this.failed.set(true);
      return;
    }

    this.failed.set(false);
    this.counted.set(false);
    try {
      // The second count is the intersection, not every object in the tenant
      // that has ever been issued a credential: a target that has received more
      // than one migration would otherwise report more collected than migrated.
      const marker = markerFragment(runId);
      const [total, collected] = await Promise.all([
        this.migration.api.countWithFragments(tenant, [marker]),
        this.migration.api.countWithFragments(tenant, [marker, CREDENTIALS_ISSUED_FRAGMENT])
      ]);
      this.total.set(total);
      this.collected.set(collected);
      this.counted.set(true);
    } catch {
      // No detail shown on purpose. This is a number beside a line of text, not
      // an operation somebody asked for, and a platform error rendered here
      // would read as though the migration itself had failed.
      this.failed.set(true);
    }
  }
}
