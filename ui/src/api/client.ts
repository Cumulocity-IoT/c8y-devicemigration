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

  reverts(): Promise<Job[]>;
  revert(id: string): Promise<Job>;
  startRevert(request: RevertRequest): Promise<Job>;

  establishRemoteTarget(request: RemoteTargetRequest): Promise<RemoteTargetEstablished>;
  remoteTargets(): Promise<RemoteTargetView[]>;
  discardRemoteTarget(id: string): Promise<void>;
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

    reverts: async () => (await read<{ reverts?: Job[] }>('/reverts')).reverts ?? [],
    revert: id => read<Job>(`/reverts/${encodeURIComponent(id)}`),
    startRevert: request_ => write<Job>('/reverts', request_),

    establishRemoteTarget: request_ =>
      write<RemoteTargetEstablished>('/remote-targets', request_),
    remoteTargets: async () =>
      (await read<{ remoteTargets?: RemoteTargetView[] }>('/remote-targets')).remoteTargets ?? [],
    discardRemoteTarget: async id => {
      await remove<unknown>(`/remote-targets/${encodeURIComponent(id)}`);
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
