import type { components } from './types';

/**
 * The migration microservice, as the browser sees it.
 *
 * Deliberately a plain function over a fetch-like callable rather than an
 * Angular service: everything worth testing about talking to this API - which
 * status means what, how a refusal reads, what a 429 is called - is testable
 * without a browser or a dependency injector. The Angular wrapper around this is
 * three lines and holds no logic.
 *
 * Every type here comes from internal/service/openapi.yaml through
 * openapi-typescript, so a field renamed in Go fails this build rather than
 * arriving as undefined at runtime.
 */

export type TenantsResponse = components['schemas']['TenantsResponse'];
export type PreflightRequest = components['schemas']['PreflightRequest'];
export type PreflightResponse = components['schemas']['PreflightResponse'];
export type MigrationRequest = components['schemas']['MigrationRequest'];
export type RevertRequest = components['schemas']['RevertRequest'];
export type Job = components['schemas']['Job'];
export type InventoryObject = components['schemas']['InventoryObject'];
export type InventoryResponse = components['schemas']['InventoryResponse'];
export type EstimateRequest = components['schemas']['EstimateRequest'];
export type EstimateResponse = components['schemas']['EstimateResponse'];
export type EstimateBreach = components['schemas']['EstimateBreach'];
export type RemoteTargetRequest = components['schemas']['RemoteTargetRequest'];
export type RemoteTargetView = components['schemas']['RemoteTargetView'];
export type RemoteTargetEstablished = components['schemas']['RemoteTargetEstablished'];
export type TrustedCertificateView = components['schemas']['TrustedCertificateView'];
export type TrustedCertificatesResponse = components['schemas']['TrustedCertificatesResponse'];
export type TrustAnchorCutoverRequest = components['schemas']['TrustAnchorCutoverRequest'];
export type TrustAnchorCutoverResponse = components['schemas']['TrustAnchorCutoverResponse'];

/** The path the platform exposes this microservice at. */
export const BASE = '/service/devicemigration';

/**
 * Why a call failed, in terms a screen can act on rather than a status number.
 *
 * The distinctions are the ones that change what a user should do: ask for the
 * role, subscribe a tenant, wait, or read the message. Anything else is
 * `failed`, whose message comes from the service and is shown verbatim - a
 * paraphrased platform error is harder to search for than the original.
 */
export type ApiFailureKind =
  | 'unauthenticated'
  | 'forbidden'
  | 'notFound'
  | 'busy'
  | 'refused'
  | 'gone'
  | 'failed';

export class ApiError extends Error {
  constructor(
    readonly kind: ApiFailureKind,
    readonly status: number,
    message: string,
    /** The service's own error identifier, such as devicemigration/badRequest. */
    readonly code?: string
  ) {
    super(message);
    this.name = 'ApiError';
  }

  /** Whether trying the same thing again could plausibly work. */
  get retryable(): boolean {
    return this.kind === 'busy' || this.status >= 500;
  }
}

/** The subset of fetch this client uses, so a test can stand in for it. */
export type FetchLike = (url: string, init?: RequestInit) => Promise<Response>;

export interface MigrationApi {
  tenants(): Promise<TenantsResponse>;
  preflight(request: PreflightRequest): Promise<PreflightResponse>;
  estimate(request: EstimateRequest): Promise<EstimateResponse>;

  search(tenant: string, text: string, onlyRoots?: boolean): Promise<InventoryResponse>;
  object(tenant: string, id: string): Promise<InventoryObject>;
  children(tenant: string, id: string, childTypes?: string): Promise<InventoryResponse>;

  migrations(): Promise<Job[]>;
  migration(id: string): Promise<Job>;
  startMigration(request: MigrationRequest): Promise<Job>;
  cancelMigration(id: string): Promise<void>;

  /**
   * Collects the device credentials a migration issued - once.
   *
   * Returns the CSV body, which the caller saves. Throws an ApiError of kind
   * `gone` if there is nothing to collect: already taken, lost to a restart of
   * the service, or never issued. There is no way to ask twice, so a caller that
   * drops the result has lost it.
   */
  migrationCredentials(id: string): Promise<string>;

  reverts(): Promise<Job[]>;
  revert(id: string): Promise<Job>;
  startRevert(request: RevertRequest): Promise<Job>;

  establishRemoteTarget(request: RemoteTargetRequest): Promise<RemoteTargetEstablished>;
  remoteTargets(): Promise<RemoteTargetView[]>;
  discardRemoteTarget(id: string): Promise<void>;

  /**
   * A tenant's trusted certificates, each with the service's assessment of
   * whether it can be moved and why not.
   *
   * Read live every time rather than cached: a cutover leaves no record anywhere,
   * so this listing is the only answer to "has that CA already been moved" - a
   * certificate that is gone from the source is one that has.
   */
  trustedCertificates(tenant: string): Promise<TrustedCertificatesResponse>;

  /**
   * Moves one trusted certificate from one tenant to another.
   *
   * Synchronous, and destructive in the middle: the certificate is deleted from
   * the source before it is created in the target, so between the two calls the
   * fleet chained to it reaches neither. The service restores the source copy if
   * the target refuses; a caller still has to show what came back, because a
   * target that accepted the certificate while dropping autoRegistrationEnabled
   * would refuse every arriving device in silence.
   */
  cutOverCertificate(request: TrustAnchorCutoverRequest): Promise<TrustAnchorCutoverResponse>;

  /**
   * How many objects in a tenant carry **all** of these fragments.
   *
   * Counted through the inventory proxy with pageSize=1, so the page count *is*
   * the object count and nothing but statistics crosses the wire. Used to ask how
   * far a device-self-service migration has got, which is the only signal that
   * tells a working one from an ignored one.
   *
   * More than one fragment because the interesting question is always an
   * intersection: objects of *this run* that have also collected. Counting the
   * issued fragment alone would count every migration the tenant has ever
   * received.
   */
  countWithFragments(tenant: string, fragments: string[]): Promise<number>;
}

export function createApi(fetchLike: FetchLike): MigrationApi {
  const read = async <T>(path: string): Promise<T> => request<T>(fetchLike, 'GET', path);
  const write = async <T>(path: string, body?: unknown): Promise<T> =>
    request<T>(fetchLike, 'POST', path, body);
  const remove = async <T>(path: string): Promise<T> => request<T>(fetchLike, 'DELETE', path);

  return {
    tenants: () => read<TenantsResponse>('/tenants'),
    preflight: request_ => write<PreflightResponse>('/preflight', request_),
    estimate: request_ => write<EstimateResponse>('/estimates', request_),

    search: (tenant, text, onlyRoots) => {
      const query = new URLSearchParams();
      if (text) {
        query.set('text', text);
      }
      if (onlyRoots) {
        query.set('onlyRoots', 'true');
      }
      return read<InventoryResponse>(
        `/tenants/${encodeURIComponent(tenant)}/inventory?${query.toString()}`
      );
    },
    object: (tenant, id) =>
      read<InventoryObject>(
        `/tenants/${encodeURIComponent(tenant)}/inventory/${encodeURIComponent(id)}`
      ),
    children: (tenant, id, childTypes) => {
      const query = childTypes ? `?childTypes=${encodeURIComponent(childTypes)}` : '';
      return read<InventoryResponse>(
        `/tenants/${encodeURIComponent(tenant)}/inventory/${encodeURIComponent(id)}/children${query}`
      );
    },

    migrations: async () =>
      (await read<{ migrations?: Job[] }>('/migrations')).migrations ?? [],
    migration: id => read<Job>(`/migrations/${encodeURIComponent(id)}`),
    startMigration: request_ => write<Job>('/migrations', request_),
    cancelMigration: async id => {
      await write<unknown>(`/migrations/${encodeURIComponent(id)}/cancel`);
    },
    migrationCredentials: id =>
      readText(fetchLike, `/migrations/${encodeURIComponent(id)}/credentials`),

    reverts: async () => (await read<{ reverts?: Job[] }>('/reverts')).reverts ?? [],
    revert: id => read<Job>(`/reverts/${encodeURIComponent(id)}`),
    startRevert: request_ => write<Job>('/reverts', request_),

    establishRemoteTarget: request_ =>
      write<RemoteTargetEstablished>('/remote-targets', request_),
    remoteTargets: async () =>
      (await read<{ remoteTargets?: RemoteTargetView[] }>('/remote-targets')).remoteTargets ?? [],
    discardRemoteTarget: async id => {
      await remove<unknown>(`/remote-targets/${encodeURIComponent(id)}`);
    },

    trustedCertificates: tenant =>
      read<TrustedCertificatesResponse>(
        `/tenants/${encodeURIComponent(tenant)}/trusted-certificates`
      ),
    cutOverCertificate: request_ =>
      write<TrustAnchorCutoverResponse>('/certificate-cutovers', request_),

    countWithFragments: async (tenant, fragments) => {
      // withTotalPages because the platform omits totalPages without it, and
      // pageSize=1 so that count is the number of objects rather than of pages.
      const has = fragments.map(fragment => `has(${fragment})`).join(' and ');
      const query = new URLSearchParams({
        query: `$filter=(${has})`,
        pageSize: '1',
        withTotalPages: 'true'
      });
      const answer = await read<{ statistics?: { totalPages?: number } }>(
        `/tenants/${encodeURIComponent(tenant)}/platform/inventory/managedObjects?${query}`
      );

      // A missing count is a failure, not a zero. `withTotalPages` makes the
      // platform send `totalPages: 0` for a query that matches nothing, so
      // absence means something else went wrong - and reporting that as zero
      // would read as "no device has collected yet" for ever, which is precisely
      // the answer this is here to distinguish from.
      const total = answer.statistics?.totalPages;
      if (typeof total !== 'number') {
        throw new Error('the inventory answered without a total, so nothing can be counted');
      }
      return total;
    }
  };
}

async function request<T>(
  fetchLike: FetchLike,
  method: 'GET' | 'POST' | 'DELETE',
  path: string,
  body?: unknown
): Promise<T> {
  const init: RequestInit = { method, headers: { Accept: 'application/json' } };
  if (body !== undefined) {
    init.headers = { ...init.headers, 'Content-Type': 'application/json' };
    init.body = JSON.stringify(body);
  }

  let response: Response;
  try {
    response = await fetchLike(BASE + path, init);
  } catch (cause) {
    // A network failure is not the service refusing anything, and saying so
    // stops somebody hunting for a permission problem that is not there.
    throw new ApiError('failed', 0, `Could not reach the migration service: ${cause}`);
  }

  if (!response.ok) {
    throw await failureFrom(response);
  }

  // 204 on a cancel and on a log-level change; asking for JSON that is not there
  // would throw where nothing is wrong.
  if (response.status === 204) {
    return undefined as T;
  }
  return (await response.json()) as T;
}

/**
 * A GET whose body is a file rather than JSON.
 *
 * Separate from request() because that one asks for and parses JSON, and the
 * credentials endpoint answers text/csv. The refusal path is shared, so a 410
 * still arrives as an ApiError with the service's own explanation.
 */
async function readText(fetchLike: FetchLike, path: string): Promise<string> {
  let response: Response;
  try {
    response = await fetchLike(BASE + path, {
      method: 'GET',
      headers: { Accept: 'text/csv' }
    });
  } catch (cause) {
    throw new ApiError('failed', 0, `Could not reach the migration service: ${cause}`);
  }
  if (!response.ok) {
    throw await failureFrom(response);
  }
  return response.text();
}

async function failureFrom(response: Response): Promise<ApiError> {
  let message = '';
  let code: string | undefined;

  try {
    const body = (await response.json()) as { message?: string; error?: string };
    message = body.message ?? '';
    code = body.error;
  } catch {
    // A refusal without a JSON body is still a refusal.
  }

  return new ApiError(kindOf(response.status, code), response.status, message || fallback(response.status), code);
}

function kindOf(status: number, code?: string): ApiFailureKind {
  switch (status) {
    case 401:
      return 'unauthenticated';
    case 403:
      return 'forbidden';
    case 404:
      return 'notFound';
    case 410:
      // Its own kind, not notFound: the credentials existed and were handed over
      // once. A screen has to say "already collected" rather than "no such
      // migration", because those need different things from the reader.
      return 'gone';
    case 429:
      return 'busy';
    case 400:
      return 'refused';
    default:
      return code === 'devicemigration/tenantNotSubscribed' ? 'refused' : 'failed';
  }
}

/** What to say when the service refused without explaining itself. */
function fallback(status: number): string {
  switch (status) {
    case 401:
      return 'The platform did not accept this session. Sign in again.';
    case 403:
      return 'This account may not migrate. It needs ROLE_DEVICE_MIGRATION_ADMIN in the tenant the migration service is deployed on.';
    case 404:
      return 'Not found.';
    case 429:
      return 'The migration service is busy. Try again when the running migration finishes.';
    default:
      return `The migration service answered ${status}.`;
  }
}
