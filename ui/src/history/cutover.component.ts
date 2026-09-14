import { CommonModule } from '@angular/common';
import { Component, EventEmitter, Input, OnInit, Output, signal } from '@angular/core';
import { C8yTranslatePipe, CoreModule } from '@c8y/ngx-components';
import { BsModalRef } from 'ngx-bootstrap/modal';

import { type TrustedCertificateView } from '../api/client';
import { gettext } from '../i18n';
import { MigrationService } from '../api/migration.service';
import { explain, type Failure, type GridRow } from './rows';

/**
 * Moving a certificate fleet's trust anchor, once its objects have migrated.
 *
 * For such a fleet the credential is the chain and it never changes: migrating
 * the objects is only half the move, and this is the other half. The devices are
 * still connected to the source, which still trusts their CA; making the target
 * trust it instead is what brings them across, and within one instance they need
 * no change of their own to follow.
 *
 * Three things make this the most dangerous screen in the plugin, and each has a
 * counterpart below:
 *
 *   - **The unit is the whole CA.** Every device chained to it moves, including
 *     devices no migration covered, and the platform records nothing linking a
 *     device to its certificate - so the number cannot be shown, only admitted.
 *   - **There is an outage in the middle.** A fingerprint can exist in only one
 *     tenant per instance and disabling it does not free it, so the move is
 *     delete-then-create. Between the two calls the fleet reaches neither tenant.
 *   - **There is no flag to flip back.** Recovery is re-creating the certificate
 *     in the source. The service does that itself if the target refuses, which is
 *     why this waits for the answer rather than queueing a job.
 */
@Component({
  selector: 'c8y-device-migration-cutover',
  standalone: true,
  imports: [CommonModule, CoreModule],
  // A pipe has to be provided to be injectable, and the labels below are built
  // as text rather than rendered through the template.
  providers: [C8yTranslatePipe],
  template: `
    <c8y-modal
      [title]="'Move the trust anchor?' | translate"
      [labels]="labels()"
      [close]="submit"
      [disabled]="!done() && (!chosen() || busy())"
    >
      <div class="p-24">
        @if (failure(); as problem) {
          <div class="alert alert-danger">
            <strong>{{ problem.title | translate }}</strong>
            <p class="m-0">{{ problem.detail | translate }}</p>
          </div>
        }

        @if (done(); as outcome) {
          <!--
            Shown instead of the chooser once it has happened, because the
            certificate has gone from the source and the list behind this is now
            wrong. autoRegistrationEnabled is read back from the target rather
            than assumed: a target that took the certificate and dropped that flag
            would refuse every arriving device, silently.
          -->
          <div class="alert alert-success">
            <strong>{{ 'The certificate has moved.' | translate }}</strong>
            <p class="m-0">{{ outcome.message }}</p>
          </div>
          @if (!outcome.autoRegistrationEnabled) {
            <div class="alert alert-danger">
              {{
                'The target reports auto-registration as disabled for this certificate, so devices arriving with it will be refused. Enable it in the target tenant before the devices reconnect.'
                  | translate
              }}
            </div>
          }
        } @else {
          <p>
            {{
              'The devices of this migration authenticate with certificates, so they have nothing new to learn. They move when {{ target }} trusts the authority that issued them.'
                | translate: { target: row.target }
            }}
          </p>

          @if (loading()) {
            <p class="text-muted">{{ 'Reading the trusted certificates…' | translate }}</p>
          } @else if (!certificates().length) {
            <p class="text-muted">
              {{ 'This tenant has no trusted certificates to move.' | translate }}
            </p>
          } @else {
            <!--
              Unmovable ones are listed with their reason rather than hidden. A
              fleet enrolled against the tenant's own authority has to be
              re-enrolled, and that is the answer somebody needs here - not an
              empty list that looks like a bug.
            -->
            @for (certificate of certificates(); track certificate.fingerprint) {
              <label class="c8y-radio d-block" [class.text-muted]="!certificate.movable">
                <input
                  type="radio"
                  name="certificate"
                  [disabled]="!certificate.movable"
                  [checked]="chosen() === certificate.fingerprint"
                  (change)="choose(certificate)"
                />
                <span></span>
                <span>
                  <strong>{{ certificate.name || certificate.fingerprint }}</strong>
                  @if (certificate.subject) {
                    <span class="text-muted"> · {{ certificate.subject }}</span>
                  }
                  @if (certificate.reason) {
                    <span class="d-block text-muted small">{{ certificate.reason }}</span>
                  }
                </span>
              </label>
            }
          }

          @if (warning()) {
            <div class="alert alert-warning m-b-8">{{ warning() }}</div>
          }

          <div class="alert alert-warning m-b-8">
            {{
              'The certificate is removed from the source tenant before it is added to the target. Until the second step finishes, no device chained to it can reach either tenant. If the target refuses it, this service puts it back in the source.'
                | translate
            }}
          </div>
        }
      </div>
    </c8y-modal>
  `
})
export class CutoverComponent implements OnInit {
  /** The migration whose fleet is moving. Set by whoever opens this. */
  @Input({ required: true }) row!: GridRow;

  /** Fired once the certificate has moved, so the list can reload. */
  @Output() readonly movedOver = new EventEmitter<void>();

  readonly certificates = signal<TrustedCertificateView[]>([]);
  readonly warning = signal('');
  readonly chosen = signal('');
  readonly loading = signal(true);
  readonly busy = signal(false);
  readonly failure = signal<Failure | undefined>(undefined);
  readonly done = signal<
    { message?: string; autoRegistrationEnabled?: boolean } | undefined
  >(undefined);

  constructor(
    private readonly migration: MigrationService,
    private readonly modal: BsModalRef,
    private readonly translator: C8yTranslatePipe
  ) {}

  /** A catalogue string, for the one place a template cannot reach. */
  private say(text: string): string {
    return this.translator.transform(text);
  }

  ngOnInit(): void {
    void this.load();
  }

  /**
   * Reads the source tenant's truststore.
   *
   * Every time this opens, never cached. A cutover leaves no record - it creates
   * no job, being two calls rather than a run - so this listing is the only
   * answer to "has that CA already been moved": one that has is simply no longer
   * here.
   */
  async load(): Promise<void> {
    this.loading.set(true);
    this.failure.set(undefined);
    try {
      const answer = await this.migration.api.trustedCertificates(this.sourceTenant());
      this.certificates.set(answer.certificates ?? []);
      this.warning.set(answer.warning ?? '');
    } catch (error) {
      this.failure.set(explain(error));
    } finally {
      this.loading.set(false);
    }
  }

  choose(certificate: TrustedCertificateView): void {
    this.chosen.set(certificate.fingerprint ?? '');
  }

  /**
   * What the buttons say, which changes once the move has happened: there is
   * nothing left to confirm, only the read-back to have seen.
   */
  labels(): { ok: string; cancel?: string } {
    // Through gettext rather than as bare strings: the extractor the catalogue
    // tests use looks for that marker, and a label built any other way reaches a
    // German reader in English with nothing failing.
    return this.done()
      ? { ok: this.say(gettext('Close')) }
      : { ok: this.say(gettext('Move certificate')), cancel: this.say(gettext('Cancel')) };
  }

  /**
   * Moves the chosen certificate, and closes only if the service accepted it.
   *
   * Bound to c8y-modal's `close` input rather than its `onClose` output, as the
   * undo confirmation is: supplying the input makes closing our decision, so a
   * refusal leaves this open with the service's own words rather than vanishing
   * and taking the reason with it. That matters more here than anywhere - a
   * failed cutover may have left the source certificate restored, and the
   * message is the only place that says so.
   */
  readonly submit = async (): Promise<void> => {
    // Already moved: this press is the acknowledgement of the read-back, not a
    // second cutover. Doing it again would delete a certificate the source no
    // longer has.
    if (this.done()) {
      this.modal.hide();
      return;
    }
    if (!this.chosen()) {
      return;
    }

    this.failure.set(undefined);
    this.busy.set(true);
    try {
      const outcome = await this.migration.api.cutOverCertificate({
        sourceTenant: this.sourceTenant(),
        targetTenant: this.targetTenant(),
        fingerprint: this.chosen()
      });
      this.done.set(outcome);
      this.movedOver.emit();
    } catch (error) {
      this.failure.set(explain(error));
    } finally {
      this.busy.set(false);
    }
  };

  private sourceTenant(): string {
    return this.row.job.request?.sourceTenant ?? '';
  }

  private targetTenant(): string {
    return this.row.job.request?.targetTenant ?? '';
  }
}
