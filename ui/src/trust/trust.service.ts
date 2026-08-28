import { Injectable } from '@angular/core';
import { FetchClient } from '@c8y/client';

import { registerCertificate } from './register';

/**
 * Registering a trusted certificate in the tenant the browser is signed in to.
 *
 * The rules are in register.ts, tested without Angular; this hands them the
 * platform's own FetchClient, so the call carries the target admin's own session
 * and touches the microservice not at all - it is the same platform call the
 * Trusted certificates screen makes.
 */
@Injectable({ providedIn: 'root' })
export class TrustService {
  constructor(private readonly fetchClient: FetchClient) {}

  register(pem: string, name: string): Promise<string> {
    return registerCertificate(
      (url, init) => this.fetchClient.fetch(url, init),
      this.fetchClient.tenant,
      pem,
      name
    );
  }
}
