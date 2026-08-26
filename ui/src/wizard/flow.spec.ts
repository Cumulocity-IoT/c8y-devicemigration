import { ApiError, type EstimateResponse, type Job, type MigrationApi } from '../api/client';
import { WizardFlow } from './flow';

/**
 * The rules that decide what the wizard does next, tested without a browser.
 *
 * The ones that matter most are about invalidation: an estimate that describes a
 * different migration from the one about to run is worse than no estimate at
 * all, and a stale pre-flight would let somebody past a check that never
 * happened.
 */

function stub(overrides: Partial<MigrationApi> = {}): MigrationApi {
  const nothing = async () => {
    throw new Error('not stubbed');
  };
  return {
    tenants: async () => ({ ownerTenant: 't-owner', tenants: ['t-owner', 't-a', 't-b'] }),
    preflight: async () => ({}),
    estimate: async () => ({ withinLimits: true, objects: 9 }) as EstimateResponse,
    search: async () => ({ objects: [{ id: '1', name: 'Plant Berlin' }] }),
    object: async () => ({ id: '1', name: 'Plant Berlin' }),
    children: async () => ({ objects: [] }),
    migrations: async () => [],
    migration: nothing as never,
    startMigration: async () => ({ id: 'job-1', kind: 'MIGRATION', status: 'QUEUED' }) as Job,
    cancelMigration: async () => undefined,
    reverts: async () => [],
    revert: nothing as never,
    startRevert: nothing as never,
    ...overrides
  };
}

/** A flow that has got as far as a proven target. */
async function readyFlow(api: MigrationApi = stub()): Promise<WizardFlow> {
  const flow = new WizardFlow(api);
  await flow.loadTenants();
  flow.chooseSource('t-a');
  flow.chooseRoot({ id: '1', name: 'Plant Berlin' });
  flow.chooseTarget('t-b');
  await flow.preflight();
  return flow;
}

describe('offering tenants', () => {
  it('lists the owner tenant as a source but does not let it be chosen', async () => {
    const flow = new WizardFlow(stub());
    await flow.loadTenants();

    const owner = flow.sourceTenants.find(t => t.id === 't-owner');
    expect(owner?.unavailable).toContain('where migrations are recorded');
    expect(flow.sourceTenants.find(t => t.id === 't-a')?.unavailable).toBe('');
  });

  it('will not offer the source as its own target', async () => {
    const flow = new WizardFlow(stub());
    await flow.loadTenants();
    flow.chooseSource('t-a');

    expect(flow.targetTenants.find(t => t.id === 't-a')?.unavailable).toBe('already the source');
  });
});

describe('invalidation', () => {
  it('drops the root when the source changes, because it belonged to the old one', async () => {
    const flow = await readyFlow();
    flow.chooseSource('t-b');

    expect(flow.choices.root).toBeUndefined();
    expect(flow.can('target').reachable).toBe(false);
  });

  it('drops a target that has become the source', async () => {
    const flow = await readyFlow();
    flow.chooseSource('t-b'); // which was the target

    expect(flow.choices.targetTenant).toBeUndefined();
    expect(flow.preflightPassed).toBe(false);
  });

  it('makes a new target prove itself again', async () => {
    const flow = await readyFlow();
    expect(flow.preflightPassed).toBe(true);

    flow.chooseTarget('t-owner');
    expect(flow.preflightPassed).toBe(false);
    expect(flow.startable.because).toContain('checked');
  });

  // The estimate is a promise about what will move. Anything that changes what
  // would move has to void it.
  it.each([
    ['the data types', { dataTypes: 'all' }],
    ['the window', { dateFrom: '-30d' }],
    ['the child types', { childTypes: 'asset' }],
    ['not following children at all', { noChildren: true }]
  ])('voids the estimate when %s changes', async (_what, change) => {
    const flow = await readyFlow();
    await flow.refreshEstimate();
    expect(flow.estimate).toBeDefined();

    flow.changeScope(change);
    expect(flow.estimate).toBeUndefined();
    expect(flow.startable.because).toContain('estimate');
  });
});

describe('starting', () => {
  it('sends only what was chosen, so the service applies its own defaults', async () => {
    const sent: unknown[] = [];
    const flow = await readyFlow(
      stub({
        startMigration: async request => {
          sent.push(request);
          return { id: 'job-1', status: 'QUEUED' } as Job;
        }
      })
    );
    await flow.refreshEstimate();
    await flow.start();

    expect(sent[0]).toEqual({ sourceTenant: 't-a', targetTenant: 't-b', rootId: '1' });
  });

  it('names the child types only when they are not the default', async () => {
    const flow = await readyFlow();
    flow.changeScope({ childTypes: 'asset,device,addition' });

    expect(flow.request().childTypes).toBe('asset,device,addition');
  });

  it('asks for no children instead of naming types when that is the choice', async () => {
    const flow = await readyFlow();
    flow.changeScope({ noChildren: true, childTypes: 'asset' });

    expect(flow.request().noChildren).toBe(true);
    expect(flow.request().childTypes).toBeUndefined();
  });

  it('refuses to start while the estimate says it is over the limits', async () => {
    const flow = await readyFlow(
      stub({
        estimate: async () =>
          ({
            withinLimits: false,
            breaches: [{ kind: 'measurements', count: 2_000_000, allowed: 1_000_000 }]
          }) as EstimateResponse
      })
    );
    await flow.refreshEstimate();

    expect(flow.startable.reachable).toBe(false);
    expect(flow.startable.because).toContain('over the export limits');

    await flow.start();
    expect(flow.job).toBeUndefined();
  });

  it('moves to the progress step once a job exists', async () => {
    const flow = await readyFlow();
    await flow.refreshEstimate();
    await flow.start();

    expect(flow.job?.id).toBe('job-1');
    expect(flow.step).toBe('progress');
  });
});

describe('failures', () => {
  it('keeps the service\'s own words', async () => {
    const flow = new WizardFlow(
      stub({
        tenants: async () => {
          throw new ApiError(
            'forbidden',
            403,
            'admin does not hold ROLE_DEVICE_MIGRATION_ADMIN, which is required to migrate'
          );
        }
      })
    );
    await flow.loadTenants();

    expect(flow.failure?.title).toBe('Not allowed here');
    expect(flow.failure?.detail).toContain('ROLE_DEVICE_MIGRATION_ADMIN');
    expect(flow.failure?.retryable).toBe(false);
  });

  it('says a busy service is worth trying again', async () => {
    // Built against the busy stub from the start: copying state from another
    // flow would copy its API too, and the test would quietly exercise the
    // wrong one.
    const flow = await readyFlow(
      stub({
        startMigration: async () => {
          throw new ApiError('busy', 429, '10 migrations are already queued');
        }
      })
    );
    await flow.refreshEstimate();
    await flow.start();

    expect(flow.failure?.retryable).toBe(true);
    expect(flow.failure?.detail).toContain('already queued');
    // And no job was invented on the way.
    expect(flow.job).toBeUndefined();
  });

  it('clears a failure when the step changes, so it cannot haunt another screen', async () => {
    const flow = await readyFlow(
      stub({
        estimate: async () => {
          throw new ApiError('refused', 400, 'rootId is required');
        }
      })
    );

    // Arriving at the review is what estimates, and what fails here.
    await flow.open('review');
    expect(flow.step).toBe('review');
    expect(flow.failure?.detail).toBe('rootId is required');

    flow.back();
    expect(flow.step).toBe('target');
    expect(flow.failure).toBeUndefined();
  });

  it('never leaves the wizard busy after a refusal', async () => {
    const flow = new WizardFlow(
      stub({
        tenants: async () => {
          throw new ApiError('failed', 500, 'boom');
        }
      })
    );
    await flow.loadTenants();

    expect(flow.busy).toBe(false);
  });
});

describe('opening a step', () => {
  it('will not open one that is not reachable', async () => {
    const flow = new WizardFlow(stub());
    await flow.open('review');

    expect(flow.step).toBe('introduction');
  });

  it('estimates on arriving at the review, not on a button', async () => {
    let estimates = 0;
    const flow = await readyFlow(
      stub({
        estimate: async () => {
          estimates++;
          return { withinLimits: true } as EstimateResponse;
        }
      })
    );

    await flow.open('review');
    expect(estimates).toBe(1);

    // And not again on the way back and forth, because nothing changed.
    flow.back();
    await flow.open('review');
    expect(estimates).toBe(1);
  });
});
