import { Injectable } from '@angular/core';
import { FetchClient } from '@c8y/client';

import { serviceReachable } from './availability';

/**
 * Whether the microservice is reachable, asked once.
 *
 * Two things need the answer and must not disagree: the navigator decides which
 * entry to offer, and the route decides which screen to render. Asking twice
 * would also mean two calls on every page load for something that cannot change
 * while a page is open - a tenant does not gain a subscription mid-session.
 *
 * The promise is cached rather than the value, so callers arriving together share
 * one request instead of racing to start their own.
 */
@Injectable({ providedIn: 'root' })
export class AvailabilityService {
  private asked?: Promise<boolean>;

  constructor(private readonly fetchClient: FetchClient) {}

  reachable(): Promise<boolean> {
    this.asked ??= serviceReachable((url, init) => this.fetchClient.fetch(url, init));
    return this.asked;
  }
}
