import { Injectable } from '@angular/core';
import { FetchClient } from '@c8y/client';

import { createApi, type MigrationApi } from './client';

/**
 * The API, for Angular.
 *
 * Everything worth testing lives in createApi; this exists to hand it the
 * platform's own FetchClient, which is what makes a browser session work: it
 * sends the authorization cookie and the X-XSRF-TOKEN header the service
 * requires on anything other than a read. A hand-rolled fetch would have to do
 * both itself, and would get the second one wrong.
 */
@Injectable({ providedIn: 'root' })
export class MigrationService {
  readonly api: MigrationApi;

  constructor(private readonly fetchClient: FetchClient) {
    this.api = createApi((url, init) => this.fetchClient.fetch(url, init));
  }
}
