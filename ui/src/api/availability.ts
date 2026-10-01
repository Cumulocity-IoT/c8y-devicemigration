import { BASE, type FetchLike } from './client';

/**
 * Whether this tenant can reach the migration microservice.
 *
 * The plugin has two screens for two quite different people. In the tenant the
 * microservice is deployed on, it is a migration tool. In a tenant that has only
 * the plugin - a target the service is not subscribed to - the one useful thing
 * is trusting a migration certificate, which is a plain platform call needing no
 * microservice at all.
 *
 * Which of those a tenant gets is therefore decided by this one question, asked
 * of the platform rather than of a setting somebody has to keep in step.
 *
 * `/health` is what it asks, because it is the only route the service answers
 * without credentials: the answer then means "this tenant is subscribed and the
 * service is up" and nothing about who is asking. Any other route would conflate
 * an unsubscribed tenant with a caller lacking a role, and those need opposite
 * screens.
 */
export async function serviceReachable(fetchLike: FetchLike): Promise<boolean> {
  try {
    const response = await fetchLike(`${BASE}/health`, {
      method: 'GET',
      headers: { Accept: 'application/json' }
    });
    return response.ok;
  } catch {
    // A network failure is not an unsubscribed tenant, but it is not a working
    // microservice either, and the screen that needs no microservice is the one
    // that can still do something. Treated as unreachable for that reason.
    return false;
  }
}
