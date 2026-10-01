import { serviceReachable } from './availability';
import { BASE, type FetchLike } from './client';

/**
 * The one question that decides which screen a tenant gets, and which permission
 * gates the navigator entry. Both read the same answer, so what it means has to
 * be exact.
 */

function answering(status: number): { fetchLike: FetchLike; calls: string[] } {
  const calls: string[] = [];
  const fetchLike: FetchLike = async url => {
    calls.push(String(url));
    return new Response(null, { status });
  };
  return { fetchLike, calls };
}

describe('whether the microservice is reachable', () => {
  it('asks the one route that answers without credentials', async () => {
    const { fetchLike, calls } = answering(200);

    await expect(serviceReachable(fetchLike)).resolves.toBe(true);
    expect(calls).toEqual([`${BASE}/health`]);
  });

  // A tenant that is not subscribed never reaches the service at all: the
  // platform's own gateway answers. That is the case the trust screen exists for.
  it('is false when the gateway does not find the service', async () => {
    const { fetchLike } = answering(404);
    await expect(serviceReachable(fetchLike)).resolves.toBe(false);
  });

  it('is false when the service is there but not answering', async () => {
    const { fetchLike } = answering(503);
    await expect(serviceReachable(fetchLike)).resolves.toBe(false);
  });

  // Health is deliberately the route asked: it answers without credentials, so a
  // true here cannot be confused with "this caller has a role" - and those two
  // need opposite screens.
  it('does not depend on who is asking', async () => {
    for (const status of [200, 404]) {
      const { fetchLike, calls } = answering(status);
      await serviceReachable(fetchLike);
      expect(calls[0]).not.toContain('migrations');
      expect(calls[0]).toContain('/health');
    }
  });

  it('treats a network failure as unreachable', async () => {
    const failing: FetchLike = async () => {
      throw new Error('offline');
    };
    await expect(serviceReachable(failing)).resolves.toBe(false);
  });
});
