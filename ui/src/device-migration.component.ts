import { CommonModule } from '@angular/common';
import { Component, OnInit, signal } from '@angular/core';
import { CoreModule } from '@c8y/ngx-components';

import { AvailabilityService } from './api/availability.service';
import { HistoryComponent } from './history/history.component';
import { RegisterCertificateComponent } from './trust/register-certificate.component';

/**
 * What `/device-migration` opens, which depends on the tenant.
 *
 * One route and one navigator entry, because the feature is one thing. What a
 * tenant can actually do with it is not: where the microservice is subscribed
 * this is the migration tool, and where it is not - a target tenant carrying only
 * the plugin - the one useful thing is trusting a migration certificate, which
 * needs no microservice at all.
 *
 * Deciding here rather than in the navigator means the two cannot disagree, and
 * means a link or a bookmark to this path lands on whatever that tenant has.
 */
@Component({
  // Not `c8y-device-migration`: the drawer that starts a migration already has
  // that, and two components cannot share a selector.
  selector: 'c8y-device-migration-page',
  standalone: true,
  imports: [CommonModule, CoreModule, HistoryComponent, RegisterCertificateComponent],
  template: `
    @if (reachable() === undefined) {
      <!--
        Neither screen until the answer is in. Rendering one and swapping it is
        worse than a moment of nothing: the migration list starts loading jobs
        from a service this tenant may not have, and the reader sees a screen
        appear and then be replaced by a different one.
      -->
      <c8y-loading />
    } @else if (reachable()) {
      <c8y-device-migration-history />
    } @else {
      <c8y-device-migration-trust />
    }
  `
})
export class DeviceMigrationComponent implements OnInit {
  /** Undefined until the platform has answered. */
  readonly reachable = signal<boolean | undefined>(undefined);

  constructor(private readonly availability: AvailabilityService) {}

  async ngOnInit(): Promise<void> {
    this.reachable.set(await this.availability.reachable());
  }
}
