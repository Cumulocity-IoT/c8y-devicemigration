import { FetchClient } from '@c8y/client';

import { TenantPlatformClient } from './platform-client';

/** The session's client, recording where it was asked to go. */
function session(): { client: FetchClient; urls: string[] } {
  const urls: string[] = [];
  const client = {
    fetch: (url: string) => {
      urls.push(url);
      return Promise.resolve({ status: 200 } as Response);
    },
    getUrl: (url: string) => `https://tenant.example.com${url}`,
    getFetchOptions: (options: unknown) => options
  } as unknown as FetchClient;
  return { client, urls };
}

describe('reading another tenant through the microservice', () => {
  it('sends a platform path to that tenant s proxy', async () => {
    const { client, urls } = session();
    const proxy = new TenantPlatformClient(client);
    proxy.for('t87654321');

    await proxy.fetch('/inventory/managedObjects?pageSize=10');

    expect(urls).toEqual([
      '/service/devicemigration/tenants/t87654321/platform/inventory/managedObjects?pageSize=10'
    ]);
  });

  it('reads whichever tenant it was last pointed at', async () => {
    const { client, urls } = session();
    const proxy = new TenantPlatformClient(client);

    proxy.for('t1');
    await proxy.fetch('inventory/managedObjects');
    proxy.for('t2');
    await proxy.fetch('inventory/managedObjects');

    expect(urls[0]).toContain('/tenants/t1/platform/');
    expect(urls[1]).toContain('/tenants/t2/platform/');
  });

  // The failure this prevents is the quiet one: a request that goes to the
  // tenant the browser is signed in to and lists the wrong devices.
  it('refuses to read anything before a tenant is chosen', async () => {
    const { client, urls } = session();
    const proxy = new TenantPlatformClient(client);

    expect(() => proxy.fetch('/inventory/managedObjects')).toThrow(/no tenant chosen/);
    expect(urls).toEqual([]);
  });

  // The service rewrites the links in what it returns to point back at itself,
  // so a client following self or next arrives with a path that is already ours.
  it('does not prefix a path that is already proxied', async () => {
    const { client, urls } = session();
    const proxy = new TenantPlatformClient(client);
    proxy.for('t1');

    await proxy.fetch('/service/devicemigration/tenants/t1/platform/inventory/managedObjects/9');

    expect(urls).toEqual([
      '/service/devicemigration/tenants/t1/platform/inventory/managedObjects/9'
    ]);
  });

  it('resolves a URL through the session, so the host is the session s host', () => {
    const { client } = session();
    const proxy = new TenantPlatformClient(client);
    proxy.for('t1');

    expect(proxy.getUrl('/inventory/managedObjects')).toBe(
      'https://tenant.example.com/service/devicemigration/tenants/t1/platform/inventory/managedObjects'
    );
  });

  it('escapes a tenant into the path', async () => {
    const { client, urls } = session();
    const proxy = new TenantPlatformClient(client);
    proxy.for('t1/../t2');

    await proxy.fetch('/inventory/managedObjects');

    expect(urls[0]).toContain('/tenants/t1%2F..%2Ft2/platform/');
  });
});
