import type { FetchLike } from '../api/client';
import { certificateRegistration } from './certificate';

/**
 * Registering a trusted certificate in a tenant, over a fetch-like.
 *
 * Pure the way api/client is pure: it takes the send function rather than an
 * Angular client, so the URL it builds, the tenant it falls back to and the way
 * it reads a refusal are all testable without a browser. The Angular service is
 * three lines that hand it the platform's own FetchClient.
 */

/**
 * Registers the pasted certificate and returns its fingerprint.
 *
 * tenant is the session's tenant when the caller knows it; empty asks the
 * platform, whose currentTenant `name` is the tenant id the endpoint takes.
 */
export async function registerCertificate(
  fetchLike: FetchLike,
  tenant: string,
  pem: string,
  name: string
): Promise<string> {
  const inTenant = tenant || (await currentTenant(fetchLike));
  const response = await fetchLike(
    `/tenant/tenants/${encodeURIComponent(inTenant)}/trusted-certificates`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      body: JSON.stringify(certificateRegistration(pem, name))
    }
  );

  if (!response.ok) {
    throw new Error(await reason(response));
  }
  const body = (await response.json()) as { fingerprint?: string };
  return body.fingerprint ?? '';
}

async function currentTenant(fetchLike: FetchLike): Promise<string> {
  const response = await fetchLike('/tenant/currentTenant', {
    headers: { Accept: 'application/json' }
  });
  const body = (await response.json()) as { name?: string };
  return body.name ?? '';
}

/** The platform's own words for a refusal, which is what to search for. */
async function reason(response: Response): Promise<string> {
  try {
    const body = (await response.json()) as { message?: string };
    if (body.message) {
      return body.message;
    }
  } catch {
    // no JSON body; fall through to the status
  }
  return `The platform refused the certificate (${response.status}).`;
}
