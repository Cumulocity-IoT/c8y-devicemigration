import type { FetchLike } from '../api/client';
import { registerCertificate } from './register';

const PEM = '-----BEGIN CERTIFICATE-----\nMII==\n-----END CERTIFICATE-----';

function fetchLike(handler: (url: string, init?: RequestInit) => Partial<Response>): {
  fetch: FetchLike;
  urls: string[];
  bodies: string[];
} {
  const urls: string[] = [];
  const bodies: string[] = [];
  const fetch: FetchLike = (url, init) => {
    urls.push(url);
    if (init?.body) {
      bodies.push(init.body as string);
    }
    return Promise.resolve(handler(url, init) as Response);
  };
  return { fetch, urls, bodies };
}

describe('registering a certificate', () => {
  it('posts it to the given tenant and returns the fingerprint', async () => {
    const stub = fetchLike(() => ({ ok: true, json: () => Promise.resolve({ fingerprint: 'ab12' }) }));

    const fingerprint = await registerCertificate(stub.fetch, 't-target', PEM, 'Werk');

    expect(fingerprint).toBe('ab12');
    expect(stub.urls).toEqual(['/tenant/tenants/t-target/trusted-certificates']);
    expect(JSON.parse(stub.bodies[0])).toMatchObject({
      certInPemFormat: PEM,
      autoRegistrationEnabled: true,
      status: 'ENABLED',
      name: 'Werk'
    });
  });

  it('asks the platform which tenant it is when it was not told', async () => {
    const stub = fetchLike(url =>
      url.includes('currentTenant')
        ? { ok: true, json: () => Promise.resolve({ name: 't-found' }) }
        : { ok: true, json: () => Promise.resolve({ fingerprint: 'x' }) }
    );

    await registerCertificate(stub.fetch, '', PEM, '');

    expect(stub.urls).toContain('/tenant/currentTenant');
    expect(stub.urls).toContain('/tenant/tenants/t-found/trusted-certificates');
  });

  it('surfaces the platform’s own refusal', async () => {
    const stub = fetchLike(() => ({
      ok: false,
      status: 422,
      json: () => Promise.resolve({ message: 'Certificate already exists' })
    }));

    await expect(registerCertificate(stub.fetch, 't', PEM, 'x')).rejects.toThrow(/already exists/);
  });
});
