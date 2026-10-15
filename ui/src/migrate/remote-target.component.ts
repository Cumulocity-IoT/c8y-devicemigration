import { CommonModule } from '@angular/common';
import { Component, EventEmitter, Input, Output, signal } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { C8yTranslatePipe, CoreModule } from '@c8y/ngx-components';
import { BsModalRef } from 'ngx-bootstrap/modal';

import { gettext } from '../i18n';
import type { MigrationFlow } from './flow';

/**
 * Connecting a tenant this instance is not subscribed to.
 *
 * Its own modal rather than a panel inside the migration dialogue, and that is
 * the point: establishing the connection is a short errand with its own steps -
 * name the tenant, carry a certificate to it, come back - and it happens once,
 * for a target that is set up again rarely. Inline it would occupy a third of
 * the dialogue permanently to serve the uncommon case, and switching it on would
 * read as a mode the whole form is in rather than as an answer to "which tenant".
 *
 * The connection is a certificate: the service mints one, the operator registers
 * the public half on the target, and nobody's password is exposed. The private
 * half never leaves the service.
 */
@Component({
  selector: 'c8y-device-migration-remote-target',
  standalone: true,
  imports: [CommonModule, CoreModule, FormsModule],
  providers: [C8yTranslatePipe],
  template: `
    <c8y-modal
      [title]="'Connect a different tenant' | translate"
      [labels]="labels()"
      [close]="submit"
      [disabled]="busy()"
    >
      <div class="p-24">
        @if (flow.failure; as problem) {
          <div class="alert alert-danger">
            <strong>{{ problem.title | translate }}</strong>
            <p class="m-0">{{ problem.detail | translate }}</p>
          </div>
        }

        <p class="text-muted">
          {{
            'For a tenant under a different parent tenant or on another system. The connection is a certificate rather than a password: this service mints one, you register the public half on that tenant, and the private half never leaves here.'
              | translate
          }}
        </p>

        @if (!flow.remoteTarget) {
          <c8y-form-group>
            <label for="remoteHost">{{ 'Target host' | translate }}</label>
            <input
              id="remoteHost"
              class="form-control"
              placeholder="t22222.eu-latest.cumulocity.com"
              [ngModel]="host()"
              (ngModelChange)="host.set($event)"
            />
          </c8y-form-group>
          <c8y-form-group>
            <label for="remoteTenant">{{ 'Target tenant id' | translate }}</label>
            <input
              id="remoteTenant"
              class="form-control"
              placeholder="t22222"
              [ngModel]="tenant()"
              (ngModelChange)="tenant.set($event)"
            />
          </c8y-form-group>
        } @else {
          <p class="text-medium m-b-2">
            {{ 'Trust this certificate on the target tenant' | translate }}
          </p>
          <p class="text-muted small m-b-8">
            {{
              'In the target tenant, open Device migration and paste the certificate below. A tenant without the migration service opens straight onto that screen.'
                | translate
            }}
          </p>
          <textarea
            class="form-control m-b-8 device-migration-pem"
            rows="5"
            readonly
            >{{ flow.remoteTarget.caCertificate }}</textarea
          >
          <div class="d-flex gap-8 a-i-center">
            <button class="btn btn-default btn-sm" type="button" (click)="copy()">
              {{ copied() ? ('Copied' | translate) : ('Copy certificate' | translate) }}
            </button>
            <button class="btn btn-default btn-sm" type="button" (click)="download()">
              {{ 'Download certificate' | translate }}
            </button>
          </div>
          <p class="text-muted small m-t-8 m-b-0">
            {{
              'Without the plugin on that tenant, add the downloaded file under Administration → Device management → Trusted certificates, with auto-registration enabled.'
                | translate
            }}
          </p>
        }
      </div>
    </c8y-modal>
  `,
  styles: [
    `
      .device-migration-pem {
        font-family: monospace;
        font-size: 11px;
      }
    `
  ]
})
export class RemoteTargetComponent {
  /** The wizard this is establishing a target for. Set by whoever opens it. */
  @Input({ required: true }) flow!: MigrationFlow;

  /** Fired once the connection is proven, so the dialogue can render it. */
  @Output() readonly connected = new EventEmitter<void>();

  readonly host = signal('');
  readonly tenant = signal('');
  readonly busy = signal(false);
  readonly copied = signal(false);

  constructor(
    private readonly modal: BsModalRef,
    private readonly translator: C8yTranslatePipe
  ) {}

  private say(text: string): string {
    return this.translator.transform(text);
  }

  /**
   * Two steps, so the button says which one it is on.
   *
   * The certificate has to be registered on the other tenant between them, which
   * is not something this can wait for - so the first press establishes and shows
   * the certificate, and the second proves it.
   */
  labels(): { ok: string; cancel: string } {
    return {
      ok: this.flow.remoteTarget
        ? this.say(gettext('I have registered it — check connection'))
        : this.say(gettext('Establish connection')),
      cancel: this.say(gettext('Cancel'))
    };
  }

  /**
   * Bound to the modal's `close` input rather than its output, as the undo
   * confirmation is: supplying the input makes closing this component's decision,
   * so a refusal stays on screen carrying the service's own words instead of
   * vanishing and taking the reason with it.
   */
  readonly submit = async (): Promise<void> => {
    this.busy.set(true);
    try {
      if (!this.flow.remoteTarget) {
        await this.flow.establishRemoteTarget(this.host().trim(), this.tenant().trim());
        // Established but not proven: the operator has a certificate to carry to
        // the other tenant, so this stays open for the second step.
        return;
      }
      await this.flow.checkRemoteTarget();
      if (this.flow.failure) {
        return;
      }
      this.connected.emit();
      this.modal.hide();
    } finally {
      this.busy.set(false);
    }
  };

  async copy(): Promise<void> {
    const pem = this.flow.remoteTarget?.caCertificate;
    if (!pem) {
      return;
    }
    await navigator.clipboard.writeText(pem);
    this.copied.set(true);
    setTimeout(() => this.copied.set(false), 2000);
  }

  download(): void {
    const pem = this.flow.remoteTarget?.caCertificate;
    if (!pem) {
      return;
    }
    const url = URL.createObjectURL(new Blob([pem], { type: 'application/x-pem-file' }));
    try {
      const link = document.createElement('a');
      link.href = url;
      link.download = `migration-${this.tenant() || 'target'}.pem`;
      link.click();
    } finally {
      URL.revokeObjectURL(url);
    }
  }
}
