import { gettext } from '../i18n';

/**
 * Registering a migration certificate on the target tenant.
 *
 * The remote-target flow mints a certificate on the source and shows its public
 * CA. Somebody has to make the target trust it. The platform's own screen wants
 * a file; this lets the target admin paste the text instead - which is the whole
 * point, since what the source shows is text.
 *
 * The request is exactly what the platform's own uploader sends: the full PEM,
 * armor and all (its uploader assigns `certInPemFormat` the raw file contents),
 * with auto-registration on so a device presenting a leaf signed by this CA is
 * bound to its migrated object without a prior device request.
 *
 * Pure on purpose - the Angular piece is a few lines of wiring; this is where the
 * rules are, so this is where the tests are.
 */

/** What the platform's trusted-certificate API is asked to create. */
export interface CertificateRegistration {
  name: string;
  certInPemFormat: string;
  autoRegistrationEnabled: true;
  status: 'ENABLED';
}

/** What is wrong with a pasted certificate, or nothing when it looks like one. */
export function certificateProblem(pem: string): string | undefined {
  const text = pem.trim();
  if (text === '') {
    return gettext('Paste the certificate first.');
  }
  if (!text.includes('-----BEGIN CERTIFICATE-----') || !text.includes('-----END CERTIFICATE-----')) {
    return gettext(
      'That does not look like a certificate. Paste the whole thing, including the BEGIN and END lines.'
    );
  }
  return undefined;
}

/**
 * The registration request for a pasted certificate.
 *
 * The name has to be unique in the tenant, so it is the caller's to choose; an
 * empty one falls back to something recognisable rather than being rejected by
 * the platform for being blank.
 */
export function certificateRegistration(pem: string, name: string): CertificateRegistration {
  const chosen = name.trim() || gettext('Device migration certificate');
  return {
    name: chosen,
    certInPemFormat: pem.trim(),
    autoRegistrationEnabled: true,
    status: 'ENABLED'
  };
}
