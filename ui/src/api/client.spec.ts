import { ApiError, BASE, createApi, type FetchLike } from './client';

/**
 * What a screen has to know when a call fails is not the status number but what
 * to do about it: ask for the role, subscribe a tenant, wait, or read the
 * message. These tests pin those translations, because they are what the error
 * screens are built on.
 */

/** answers builds a fetch stub that records what it was asked. */
function answers(...responses: Array<{ status: number; body?: unknown }>) {
  const calls: Array<{ url: string; method: string; body?: string }> = [];
  let at = 0;

  const fetchLike: FetchLike = async (url, init) => {
    calls.push({
      url,
      method: init?.method ?? 'GET',
      body: typeof init?.body === 'string' ? init.body : undefined
    });

    const answer = responses[Math.min(at++, responses.length - 1)];
    return new Response(answer.body === undefined ? null : JSON.stringify(answer.body), {
      status: answer.status,
      headers: { 'Content-Type': 'application/json' }
    });
  };

  return { fetchLike, calls };
}

describe('reading', () => {
  it('asks the platform for the microservice, not for a bare path', async () => {
    const { fetchLike, calls } = answers({ status: 200, body: { ownerTenant: 't1', tenants: [] } });

    await createApi(fetchLike).tenants();

    expect(calls[0].url).toBe(`${BASE}/tenants`);
    expect(calls[0].method).toBe('GET');
  });

  it('unwraps a listing to the array a table wants', async () => {
    const { fetchLike } = answers({
      status: 200,
      body: { migrations: [{ id: 'a', kind: 'MIGRATION' }] }
    });

    await expect(createApi(fetchLike).migrations()).resolves.toEqual([
      { id: 'a', kind: 'MIGRATION' }
    ]);
  });

  it('treats a listing with no entries as an empty list, not as undefined', async () => {
    const { fetchLike } = answers({ status: 200, body: {} });
    await expect(createApi(fetchLike).reverts()).resolves.toEqual([]);
  });

  it('escapes a tenant and an id into the path', async () => {
    const { fetchLike, calls } = answers({ status: 200, body: { id: '1' } });

    await createApi(fetchLike).object('t/evil', 'id with space');

    expect(calls[0].url).toBe(`${BASE}/tenants/t%2Fevil/inventory/id%20with%20space`);
  });

  it('leaves an empty search term out rather than searching for nothing', async () => {
    const { fetchLike, calls } = answers({ status: 200, body: { objects: [] } });

    await createApi(fetchLike).search('t1', '');

    expect(calls[0].url).toBe(`${BASE}/tenants/t1/inventory?`);
  });
});

describe('writing', () => {
  it('sends the request as JSON', async () => {
    const { fetchLike, calls } = answers({ status: 202, body: { id: 'job-1', kind: 'MIGRATION' } });

    await createApi(fetchLike).startMigration({
      sourceTenant: 't1',
      targetTenant: 't2',
      rootId: '9'
    });

    expect(calls[0].method).toBe('POST');
    expect(JSON.parse(calls[0].body!)).toEqual({
      sourceTenant: 't1',
      targetTenant: 't2',
      rootId: '9'
    });
  });

  // A cancel answers 204. Asking for JSON that is not there would throw where
  // nothing went wrong.
  it('accepts a response with no body', async () => {
    const { fetchLike } = answers({ status: 204 });
    await expect(createApi(fetchLike).cancelMigration('job-1')).resolves.toBeUndefined();
  });
});

describe('failures', () => {
  const failing = (status: number, body?: unknown) => createApi(answers({ status, body }).fetchLike);

  it('reads the service\'s own message rather than paraphrasing it', async () => {
    const message =
      'h.meyer does not hold ROLE_DEVICE_MIGRATION_ADMIN, which is required to migrate';
    const error = await failing(403, { error: 'security/Forbidden', message })
      .tenants()
      .catch((e: ApiError) => e);

    expect(error).toBeInstanceOf(ApiError);
    expect((error as ApiError).kind).toBe('forbidden');
    expect((error as ApiError).message).toBe(message);
    expect((error as ApiError).code).toBe('security/Forbidden');
  });

  it.each([
    [401, 'unauthenticated'],
    [403, 'forbidden'],
    [404, 'notFound'],
    [429, 'busy'],
    [400, 'refused'],
    [500, 'failed']
  ])('turns %i into %s', async (status, kind) => {
    const error = (await failing(status, { message: 'x' })
      .tenants()
      .catch((e: ApiError) => e)) as ApiError;

    expect(error.kind).toBe(kind);
  });

  // A full queue is worth trying again; a missing role is not.
  it('says which failures are worth retrying', async () => {
    const busy = (await failing(429, { message: 'busy' })
      .tenants()
      .catch((e: ApiError) => e)) as ApiError;
    const forbidden = (await failing(403, { message: 'no' })
      .tenants()
      .catch((e: ApiError) => e)) as ApiError;

    expect(busy.retryable).toBe(true);
    expect(forbidden.retryable).toBe(false);
  });

  // An unsubscribed tenant is a 400 with a particular code, and it is a
  // configuration problem rather than a bad request from the screen.
  it('recognises an unsubscribed tenant', async () => {
    const error = (await failing(400, {
      error: 'devicemigration/tenantNotSubscribed',
      message: 'tenant t22222 has no service user for this microservice'
    })
      .estimate({ sourceTenant: 't2', rootId: '1' })
      .catch((e: ApiError) => e)) as ApiError;

    expect(error.kind).toBe('refused');
    expect(error.code).toBe('devicemigration/tenantNotSubscribed');
  });

  it('explains a refusal that carried no message', async () => {
    const error = (await failing(403)
      .tenants()
      .catch((e: ApiError) => e)) as ApiError;

    expect(error.message).toContain('ROLE_DEVICE_MIGRATION_ADMIN');
  });

  // A network failure is not the service refusing anything, and saying so stops
  // somebody hunting for a permission problem that is not there.
  it('distinguishes not reaching the service from being refused by it', async () => {
    const api = createApi(async () => {
      throw new TypeError('Failed to fetch');
    });

    const error = (await api.tenants().catch((e: ApiError) => e)) as ApiError;
    expect(error.kind).toBe('failed');
    expect(error.status).toBe(0);
    expect(error.message).toContain('Could not reach the migration service');
  });
});
