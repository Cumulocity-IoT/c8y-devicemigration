import { FetchClient, type IFetchOptions, type IFetchResponse } from '@c8y/client';

import { BASE } from './client';

/**
 * A Cumulocity client that reads another tenant's inventory.
 *
 * The platform's own components - the asset selector above all - read through
 * `InventoryService`, which talks to whatever tenant the browser is signed in
 * to. The hierarchy being migrated is in a different tenant, and the browser has
 * no session there; only the microservice holds credentials for both.
 *
 * So this sends every request through the microservice's inventory proxy
 * instead. Given this client, `InventoryService` builds the same paths it always
 * builds and they land in the tenant this is pointed at, which is what lets the
 * platform's components work here unchanged - searching, paging and expanding a
 * group included.
 *
 * Requests are delegated to the session's own client rather than re-authenticated
 * here. Whatever the session uses - a cookie and an XSRF header, basic
 * credentials in a development shell - is what this uses, because it is the same
 * client doing the sending.
 */
// Deliberately not decorated with @Injectable: it is provided with a factory,
// which keeps this file free of Angular and therefore testable without it - the
// same rule the rest of this plugin follows.
export class TenantPlatformClient extends FetchClient {
  // Not called `tenant`: FetchClient already has a field of that name, meaning
  // the tenant of the session, and shadowing it would be a trap for the next
  // reader.
  private chosen?: string;

  constructor(private readonly session: FetchClient) {
    // An absolute base so the inherited constructor never has to consult
    // window.location. Nothing reads it: every path is resolved by the session
    // client, which knows where the platform is.
    super('http://proxied.invalid');
  }

  /** Points this at a tenant. Nothing can be read before this is called. */
  for(tenant: string): void {
    this.chosen = tenant;
  }

  override fetch(url: string, init?: IFetchOptions): Promise<IFetchResponse> {
    return this.session.fetch(this.proxied(url), init);
  }

  override getUrl(url = '', options?: IFetchOptions): string {
    return this.session.getUrl(this.proxied(url), options);
  }

  override getFetchOptions(options?: IFetchOptions): IFetchOptions {
    return this.session.getFetchOptions(options);
  }

  /**
   * The proxy path for a platform path.
   *
   * A URL that is already proxied is left alone: the microservice rewrites the
   * links in what it returns to point back at itself, so a client that follows
   * `self` or `next` arrives here with a path that must not be prefixed twice.
   *
   * Refusing to guess when no tenant has been set is deliberate. The failure it
   * prevents is the one that matters: a request that quietly goes to the tenant
   * the browser is signed in to and confidently lists the wrong devices.
   */
  private proxied(url: string): string {
    if (!this.chosen) {
      throw new Error('no tenant chosen, so there is no inventory to read');
    }

    const prefix = `${BASE}/tenants/${encodeURIComponent(this.chosen)}/platform`;
    if (url.startsWith(prefix) || url.includes(`${prefix}/`)) {
      return url;
    }
    return `${prefix}/${url.replace(/^\/+/, '')}`;
  }
}
