import { CommonModule } from '@angular/common';
import { Component, signal } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { CoreModule } from '@c8y/ngx-components';

import { certificateProblem } from './certificate';
import { TrustService } from './trust.service';

/**
 * Trusting a migration certificate, on the target tenant.
 *
 * The remote-target flow shows a certificate on the source side; this is where
 * it is trusted, by whoever administers the target. Pasting text is the point:
 * the platform's own Trusted certificates screen wants a file, and what the
 * source shows is text.
 */
@Component({
  selector: 'c8y-device-migration-trust',
  standalone: true,
  imports: [CommonModule, CoreModule, FormsModule],
  templateUrl: './register-certificate.component.html'
})
export class RegisterCertificateComponent {
  readonly pem = signal('');
  readonly name = signal('');
  readonly busy = signal(false);

  /** The fingerprint of what was registered, once it has been. */
  readonly registered = signal<string | undefined>(undefined);

  /** The platform's own refusal, when it refuses. */
  readonly failure = signal<string | undefined>(undefined);

  constructor(private readonly trust: TrustService) {}

  /** What is wrong with what has been pasted, for the button and the hint. */
  problem(): string | undefined {
    return certificateProblem(this.pem());
  }

  async register(): Promise<void> {
    if (this.problem()) {
      return;
    }
    this.busy.set(true);
    this.failure.set(undefined);
    this.registered.set(undefined);
    try {
      this.registered.set(await this.trust.register(this.pem(), this.name()));
    } catch (error) {
      this.failure.set(error instanceof Error ? error.message : String(error));
    } finally {
      this.busy.set(false);
    }
  }

  /** Clears the form to trust another one. */
  again(): void {
    this.pem.set('');
    this.name.set('');
    this.registered.set(undefined);
    this.failure.set(undefined);
  }
}
