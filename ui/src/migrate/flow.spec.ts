import { ApiError, type EstimateResponse, type Job, type MigrationApi } from '../api/client';
import { MigrationFlow } from './flow';

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
    tenants: async () => ({
      ownerTenant: 't-owner',
      tenants: [
        { id: 't-owner', name: 'Operations' },
        { id: 't-a', name: 'Werk Berlin', domain: 't-a.example.com' },
        { id: 't-b', domain: 't-b.example.com' }
      ]
    }),
    preflight: async () => ({}),
    estimate: async () => ({ withinLimits: true, objects: 9 }) as EstimateResponse,
    search: async () => ({ objects: [{ id: '1', name: 'Plant Berlin' }] }),
    object: async () => ({ id: '1', name: 'Plant Berlin' }),
    children: async () => ({ objects: [] }),
    migrations: async () => [],
    migration: nothing as never,
    startMigration: async () => ({ id: 'job-1', kind: 'MIGRATION', status: 'QUEUED' }) as Job,
    cancelMigration: async () => undefined,
    migrationCredentials: nothing as never,
    deleteMigration: nothing as never,
    reverts: async () => [],
    revert: nothing as never,
    startRevert: nothing as never,
    deleteRevert: nothing as never,
    establishRemoteTarget: async () => ({ id: 'rt-1', host: 'h', tenant: 't-remote' }),
    remoteTargets: async () => [],
    discardRemoteTarget: async () => undefined,
    trustedCertificates: nothing as never,
    cutOverCertificate: nothing as never,
    countWithFragments: nothing as never,
    ...overrides
  };
}

/** A flow that has got as far as a proven target. */
async function readyFlow(api: MigrationApi = stub()): Promise<MigrationFlow> {
  const flow = new MigrationFlow(api);
  await flow.loadTenants();
  flow.chooseSource('t-a');
  flow.chooseRoot({ id: '1', name: 'Plant Berlin' });
  await flow.chooseTarget('t-b');
  return flow;
}

describe('offering tenants', () => {
  it('lists the owner tenant as a source but does not let it be chosen', async () => {
    const flow = new MigrationFlow(stub());
    await flow.loadTenants();

    const owner = flow.sourceTenants.find((t: { id: string }) => t.id === 't-owner');
    expect(owner?.unavailable).toContain('where migrations are recorded');
    expect(flow.sourceTenants.find((t: { id: string }) => t.id === 't-a')?.unavailable).toBe('');
  });

  it('will not offer the source as its own target', async () => {
    const flow = new MigrationFlow(stub());
    await flow.loadTenants();
    flow.chooseSource('t-a');

    expect(flow.targetTenants.find((t: { id: string }) => t.id === 't-a')?.unavailable).toBe('already the source');
  });
});

describe('invalidation', () => {
  it('drops the root when the source changes, because it belonged to the old one', async () => {
    const flow = await readyFlow();
    flow.chooseSource('t-b');

    expect(flow.choices.root).toBeUndefined();
    expect(flow.readiness.outstanding).toContain('Choose the device or group to migrate.');
  });

  it('drops a target that has become the source', async () => {
    const flow = await readyFlow();
    flow.chooseSource('t-b'); // which was the target

    expect(flow.choices.targetTenant).toBeUndefined();
    expect(flow.preflightPassed).toBe(false);
  });

  // A pre-flight proves two particular tenants can be reached. Carrying that
  // verdict over to a different target would let somebody start a migration on
  // the strength of a check that never happened for it.
  it('proves a new target rather than trusting the old verdict', async () => {
    const checked: string[] = [];
    const flow = await readyFlow(
      stub({
        preflight: async request => {
          checked.push(request.targetTenant ?? '');
          return {};
        }
      })
    );
    expect(checked).toEqual(['t-b']);

    await flow.chooseTarget('t-owner');
    expect(checked).toEqual(['t-b', 't-owner']);
    expect(flow.preflightPassed).toBe(true);

    // And the estimate is gone, because it described the other target.
    expect(flow.estimate).toBeUndefined();
  });

  it('has nothing to say about a target that could not be proven', async () => {
    const flow = await readyFlow(
      stub({
        preflight: async () => {
          throw new ApiError('refused', 400, 'tenant t-owner has no service user');
        }
      })
    );

    expect(flow.preflightPassed).toBe(false);
    expect(flow.failure?.detail).toContain('no service user');
    expect(flow.readiness.outstanding.join(' ')).toContain('can be reached');
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
    // Cannot start, and the reason is that there is no estimate yet.
    expect(flow.readiness.ready).toBe(false);
    expect(flow.readiness.outstanding.join(' ')).toContain('how much would move');
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

    expect(flow.readiness.ready).toBe(false);
    expect(flow.readiness.outstanding.join(' ')).toContain('over the export limits');

    await flow.start();
    expect(flow.job).toBeUndefined();
  });

  it('holds the job it started, so a screen can follow it', async () => {
    const flow = await readyFlow();
    await flow.refreshEstimate();
    await flow.start();

    expect(flow.job?.id).toBe('job-1');
  });
});

describe('failures', () => {
  it('keeps the service\'s own words', async () => {
    const flow = new MigrationFlow(
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

  // On one screen a stale failure sits next to the thing that now works, so the
  // next attempt has to clear it.
  it('clears a failure when the next thing succeeds', async () => {
    let failing = true;
    const flow = await readyFlow(
      stub({
        estimate: async () => {
          if (failing) {
            throw new ApiError('refused', 400, 'rootId is required');
          }
          return { withinLimits: true } as EstimateResponse;
        }
      })
    );

    await flow.refreshEstimate();
    expect(flow.failure?.detail).toBe('rootId is required');

    failing = false;
    await flow.refreshEstimate();
    expect(flow.failure).toBeUndefined();
    expect(flow.readiness.ready).toBe(true);
  });

  it('never leaves the screen busy after a refusal', async () => {
    const flow = new MigrationFlow(
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

describe('keeping the estimate current', () => {
  it('estimates once the three choices are in place', async () => {
    let estimates = 0;
    const flow = await readyFlow(
      stub({
        estimate: async () => {
          estimates++;
          return { withinLimits: true } as EstimateResponse;
        }
      })
    );

    await flow.ensureEstimate();
    expect(estimates).toBe(1);
    expect(flow.readiness.ready).toBe(true);
  });

  // Every choice on the screen calls this, so it has to be cheap when nothing
  // has changed.
  it('does not estimate again when nothing has changed', async () => {
    let estimates = 0;
    const flow = await readyFlow(
      stub({
        estimate: async () => {
          estimates++;
          return { withinLimits: true } as EstimateResponse;
        }
      })
    );

    await flow.ensureEstimate();
    await flow.ensureEstimate();
    await flow.ensureEstimate();
    expect(estimates).toBe(1);
  });

  it('estimates again after something that changes what would move', async () => {
    let estimates = 0;
    const flow = await readyFlow(
      stub({
        estimate: async () => {
          estimates++;
          return { withinLimits: true } as EstimateResponse;
        }
      })
    );

    await flow.ensureEstimate();
    flow.changeScope({ dataTypes: 'all' });
    await flow.ensureEstimate();

    expect(estimates).toBe(2);
  });

  it('does not estimate while a choice is still missing', async () => {
    let estimates = 0;
    const flow = new MigrationFlow(
      stub({
        estimate: async () => {
          estimates++;
          return { withinLimits: true } as EstimateResponse;
        }
      })
    );
    await flow.loadTenants();
    flow.chooseSource('t-a');

    await flow.ensureEstimate();
    expect(estimates).toBe(0);
  });
});

describe('choosing what to include', () => {
  it('ticks and unticks a relationship kind', async () => {
    const flow = await readyFlow();
    expect(flow.selected('childTypes', 'asset')).toBe(true);
    expect(flow.selected('childTypes', 'addition')).toBe(false);

    flow.toggle('childTypes', 'addition');
    expect(flow.selected('childTypes', 'addition')).toBe(true);
    expect(flow.choices.childTypes).toBe('asset,device,addition');

    flow.toggle('childTypes', 'asset');
    expect(flow.choices.childTypes).toBe('device,addition');
  });

  // The list has to read back the way the API documents it, whatever order the
  // boxes were ticked in: a request that does not match what was chosen is hard
  // to trust when something goes wrong.
  it("keeps the API's order however the boxes were ticked", async () => {
    const flow = await readyFlow();
    flow.changeScope({ dataTypes: '' });

    flow.toggle('dataTypes', 'alarms');
    flow.toggle('dataTypes', 'measurements');
    flow.toggle('dataTypes', 'events');

    expect(flow.choices.dataTypes).toBe('measurements,events,alarms');
  });

  it('sends nothing when every data kind is unticked', async () => {
    const flow = await readyFlow();
    flow.changeScope({ dataTypes: 'measurements' });
    flow.toggle('dataTypes', 'measurements');

    expect(flow.choices.dataTypes).toBe('');
    expect(flow.request().dataTypes).toBeUndefined();
  });

  // Every tick changes what would move, so the estimate cannot survive one.
  it('voids the estimate on every tick', async () => {
    const flow = await readyFlow();
    await flow.ensureEstimate();
    expect(flow.estimate).toBeDefined();

    flow.toggle('dataTypes', 'events');
    expect(flow.estimate).toBeUndefined();
  });

  it('refuses to leave a migration following nothing at all', async () => {
    const flow = await readyFlow();
    flow.toggle('childTypes', 'asset');
    flow.toggle('childTypes', 'device');

    // Nothing ticked is the same request as "only this object", and the API
    // refuses an empty childTypes - so it is sent as noChildren instead.
    expect(flow.choices.childTypes).toBe('');
    expect(flow.request().noChildren).toBe(true);
    expect(flow.request().childTypes).toBeUndefined();
  });
});

describe('the start date', () => {
  it('is refused where it was typed, not at the top of the dialogue', async () => {
    const flow = await readyFlow();

    flow.changeScope({ dateFrom: 'last tuesday' });

    expect(flow.windowError?.text).toMatch(/relative offset like -14d/);
    expect(flow.failure).toBeUndefined();
  });

  it('stops a migration until it can be read', async () => {
    const flow = await readyFlow();
    await flow.ensureEstimate();
    expect(flow.readiness.ready).toBe(true);

    flow.changeScope({ dateFrom: '-14x' });

    expect(flow.readiness.ready).toBe(false);
    expect(flow.readiness.outstanding).toContain('The start date cannot be read.');
  });

  // No point asking: the service would refuse the same value, and a round trip
  // per keystroke is how a form comes to feel slow.
  it('is not sent to the service while it cannot be read', async () => {
    let asked = 0;
    const flow = await readyFlow(
      stub({
        estimate: async () => {
          asked++;
          return { withinLimits: true } as EstimateResponse;
        }
      })
    );

    flow.changeScope({ dateFrom: 'nonsense' });
    await flow.ensureEstimate();

    expect(asked).toBe(0);
  });

  it('is asked about again once it makes sense', async () => {
    const flow = await readyFlow();

    flow.changeScope({ dateFrom: 'nonsense' });
    await flow.ensureEstimate();
    expect(flow.estimate).toBeUndefined();

    flow.changeScope({ dateFrom: '-7d' });
    await flow.ensureEstimate();

    expect(flow.windowError).toBeUndefined();
    expect(flow.estimate).toBeDefined();
  });

  // The service is the authority on this syntax. When it disagrees with the
  // check above, its words still belong under the field.
  it('shows the service s own refusal on the field', async () => {
    const flow = await readyFlow(
      stub({
        estimate: async () => {
          throw new ApiError(
            'refused',
            400,
            "Unsupported date '-99y', expected a relative offset like -30d"
          );
        }
      })
    );

    flow.changeScope({ dateFrom: '-99d' });
    await flow.ensureEstimate();

    expect(flow.windowError?.text).toMatch(/Unsupported date/);
    expect(flow.failure).toBeUndefined();
  });

  it('leaves a refusal that is not about the date where every other one goes', async () => {
    const flow = await readyFlow(
      stub({
        estimate: async () => {
          throw new ApiError('refused', 400, 'rootId is required');
        }
      })
    );

    flow.changeScope({ dateFrom: '-7d' });
    await flow.ensureEstimate();

    expect(flow.windowError).toBeUndefined();
    expect(flow.failure?.detail).toBe('rootId is required');
  });
});

describe('a remote target', () => {
  it('establishes a certificate connection and exposes the CA to register', async () => {
    const flow = new MigrationFlow(
      stub({
        establishRemoteTarget: async () => ({
          id: 'rt-9',
          host: 't9.example.com',
          tenant: 't9',
          caCertificate: '-----BEGIN CERTIFICATE-----\nMII...\n-----END CERTIFICATE-----'
        })
      })
    );
    await flow.loadTenants();
    flow.chooseSource('t-a');
    flow.chooseRoot({ id: '1', name: 'Plant Berlin' });

    await flow.establishRemoteTarget('t9.example.com', 't9');

    expect(flow.choices.remoteTargetId).toBe('rt-9');
    expect(flow.choices.targetTenant).toBe('t9');
    expect(flow.remoteTarget?.caCertificate).toContain('BEGIN CERTIFICATE');
    // Not proven until the CA is registered and the connection checked.
    expect(flow.preflightPassed).toBe(false);
  });

  it('preflights a remote target by its id, not its tenant name', async () => {
    let sawRemoteId = '';
    const flow = new MigrationFlow(
      stub({
        establishRemoteTarget: async () => ({ id: 'rt-9', host: 'h', tenant: 't9' }),
        preflight: async req => {
          sawRemoteId = req.remoteTargetId ?? '';
          return {};
        }
      })
    );
    await flow.loadTenants();
    flow.chooseSource('t-a');
    flow.chooseRoot({ id: '1', name: 'Plant Berlin' });
    await flow.establishRemoteTarget('h', 't9');

    await flow.checkRemoteTarget();

    expect(sawRemoteId).toBe('rt-9');
    expect(flow.preflightPassed).toBe(true);
  });

  it('asks the migration for by remote id, and omits the tenant name', async () => {
    const flow = new MigrationFlow(
      stub({ establishRemoteTarget: async () => ({ id: 'rt-9', host: 'h', tenant: 't9' }) })
    );
    await flow.loadTenants();
    flow.chooseSource('t-a');
    flow.chooseRoot({ id: '1', name: 'Plant Berlin' });
    await flow.establishRemoteTarget('h', 't9');

    const request = flow.request();
    expect(request.remoteTargetId).toBe('rt-9');
    expect(request.targetTenant).toBeUndefined();
  });

  it('forgets the remote target when a subscribed tenant is chosen instead', async () => {
    const flow = new MigrationFlow(
      stub({ establishRemoteTarget: async () => ({ id: 'rt-9', host: 'h', tenant: 't9' }) })
    );
    await flow.loadTenants();
    flow.chooseSource('t-a');
    flow.chooseRoot({ id: '1', name: 'Plant Berlin' });
    await flow.establishRemoteTarget('h', 't9');

    await flow.chooseTarget('t-b');

    expect(flow.choices.remoteTargetId).toBeUndefined();
    expect(flow.remoteTarget).toBeUndefined();
    expect(flow.request().remoteTargetId).toBeUndefined();
    expect(flow.request().targetTenant).toBe('t-b');
  });
});

describe('how the migrated devices will authenticate', () => {
  // One question with one answer, so the request carries at most one flag. The
  // service refuses any pair of them, and a screen that could produce one would
  // be offering something that is rejected on submit.
  it('puts exactly one flag on the request, or none', async () => {
    const expected: Record<string, string | undefined> = {
      none: undefined,
      file: 'provisionCredentials',
      self: 'deviceSelfCredentials',
      certificate: 'certificateFleet'
    };

    for (const [choice, flag] of Object.entries(expected)) {
      const flow = await readyFlow();
      flow.chooseCredentials(choice as 'none' | 'file' | 'self' | 'certificate');

      const request = flow.request() as Record<string, unknown>;
      const set = Object.keys(expected)
        .map(key => expected[key])
        .filter((name): name is string => !!name)
        .filter(name => request[name] === true);

      expect(set).toEqual(flag ? [flag] : []);
    }
  });

  it('asks for nothing by default', async () => {
    const flow = await readyFlow();

    expect(flow.choices.credentials).toBe('none');
    expect(flow.request().provisionCredentials).toBeUndefined();
    expect(flow.request().deviceSelfCredentials).toBeUndefined();
    expect(flow.request().certificateFleet).toBeUndefined();
  });

  // Keeping the estimate is the point of not routing this through changeScope:
  // the count it needs is already in hand whatever the answer, because this
  // changes what is created alongside the migration rather than what moves.
  it('does not discard the estimate', async () => {
    const flow = await readyFlow();
    await flow.refreshEstimate();
    expect(flow.estimate).toBeDefined();

    flow.chooseCredentials('self');

    expect(flow.estimate).toBeDefined();
  });

  // A remote target is reached with a device access token, which cannot register
  // a device user. Both issuing answers are impossible there; the certificate
  // one is not, because it registers nothing.
  it('withdraws both issuing answers for a remote target, and keeps the certificate one', async () => {
    const remote = stub({
      establishRemoteTarget: async () => ({ id: 'rt-9', host: 'h', tenant: 't9' })
    });

    for (const [choice, expected] of [
      ['file', 'none'],
      ['self', 'none'],
      ['certificate', 'certificate']
    ] as const) {
      const flow = new MigrationFlow(remote);
      await flow.loadTenants();
      flow.chooseSource('t-a');
      flow.chooseRoot({ id: '1', name: 'Plant Berlin' });
      flow.chooseCredentials(choice);

      await flow.establishRemoteTarget('h', 't9');

      expect(flow.canIssueCredentials).toBe(false);
      expect(flow.choices.credentials).toBe(expected);
    }
  });

  it('offers everything again for a subscribed target', async () => {
    const flow = await readyFlow();

    expect(flow.canIssueCredentials).toBe(true);
  });
});

/**
 * Provisioning a device user per device is the one answer whose availability
 * depends on what the target already holds: registering an external identity a
 * tenant already has replaces the credential of the device already using it.
 * The service checks it, and the wizard has to stop before Start rather than
 * learn it from a failed job.
 */
describe('the credential check against the target', () => {
  const conflict = {
    sourceId: '4',
    name: 'Pump West',
    externalId: 'PUMP-WEST-001',
    externalType: 'c8y_Serial',
    targetId: '8801'
  };

  it('is not asked for until the answer needs it', async () => {
    const asked: unknown[] = [];
    const flow = await readyFlow(
      stub({
        preflight: async request => {
          asked.push(request);
          return {};
        }
      })
    );
    await flow.ensureCredentialCheck();

    expect(asked).toHaveLength(1);
    expect(asked[0]).not.toHaveProperty('provisionCredentials');
  });

  it('is asked for, with the hierarchy, once that answer is chosen', async () => {
    const asked: Record<string, unknown>[] = [];
    const flow = await readyFlow(
      stub({
        preflight: async request => {
          asked.push(request as Record<string, unknown>);
          return {};
        }
      })
    );
    flow.chooseCredentials('file');
    await flow.ensureCredentialCheck();

    expect(asked.at(-1)).toMatchObject({ rootId: '1', provisionCredentials: true });
  });

  it('blocks the migration when the target already holds an identity', async () => {
    const flow = await readyFlow(stub({ preflight: async () => ({ conflicts: [conflict] }) }));
    flow.chooseCredentials('file');
    await flow.ensureEstimate();
    await flow.ensureCredentialCheck();

    expect(flow.credentialsRefused).toBe(true);
    expect(flow.readiness.ready).toBe(false);
    expect(flow.credentialConflicts).toEqual([conflict]);
  });

  // The devices are named on screen, so the list has to survive to the view
  // rather than being collapsed to a count: the way out is to go and look at
  // them in both tenants.
  it('keeps every device the service named', async () => {
    const second = { ...conflict, sourceId: '5', externalId: 'LINE-A-SN', name: 'Line A' };
    const flow = await readyFlow(
      stub({ preflight: async () => ({ conflicts: [conflict, second] }) })
    );
    flow.chooseCredentials('file');
    await flow.ensureCredentialCheck();

    expect(flow.credentialConflicts.map(c => c.externalId)).toEqual([
      'PUMP-WEST-001',
      'LINE-A-SN'
    ]);
  });

  // The refusal is about one answer, not about the migration: dropping that
  // answer has to clear it, or the screen blocks for a reason nothing explains.
  it('stops blocking when another answer is chosen', async () => {
    const flow = await readyFlow(stub({ preflight: async () => ({ conflicts: [conflict] }) }));
    flow.chooseCredentials('file');
    await flow.ensureCredentialCheck();
    expect(flow.readiness.ready).toBe(false);

    flow.chooseCredentials('self');
    await flow.ensureCredentialCheck();

    expect(flow.credentialsRefused).toBe(false);
    expect(flow.credentialConflicts).toEqual([]);
  });

  // Which devices would be registered depends on the hierarchy and on which
  // relationships are followed, so narrowing the child types is as much a reason
  // to ask again as changing the target is.
  it('asks again when the scope changes', async () => {
    let asks = 0;
    const flow = await readyFlow(
      stub({
        preflight: async () => {
          asks += 1;
          return {};
        }
      })
    );
    flow.chooseCredentials('file');
    await flow.ensureCredentialCheck();
    const afterFirst = asks;

    await flow.ensureCredentialCheck();
    expect(asks).toBe(afterFirst);

    flow.changeScope({ childTypes: 'asset' });
    await flow.ensureCredentialCheck();
    expect(asks).toBe(afterFirst + 1);
  });

  // A stale answer that blocks is recoverable; a stale answer that allows would
  // let a migration start that the service then refuses.
  it('keeps blocking while it is being asked again', async () => {
    const flow = await readyFlow(stub({ preflight: async () => ({ conflicts: [conflict] }) }));
    flow.chooseCredentials('file');
    await flow.ensureCredentialCheck();

    flow.changeScope({ childTypes: 'asset' });

    expect(flow.credentialsRefused).toBe(true);
  });

  // A remote target cannot register a device user at all, so there is nothing to
  // check and nothing to report about a control that is not even offered.
  it('does not ask for a remote target', async () => {
    const asked: Record<string, unknown>[] = [];
    const flow = new MigrationFlow(
      stub({
        preflight: async request => {
          asked.push(request as Record<string, unknown>);
          return {};
        }
      })
    );
    await flow.loadTenants();
    flow.chooseSource('t-a');
    flow.chooseRoot({ id: '1', name: 'Plant Berlin' });
    await flow.establishRemoteTarget('h', 't-remote');
    await flow.checkRemoteTarget();
    await flow.ensureCredentialCheck();

    for (const request of asked) {
      expect(request).not.toHaveProperty('provisionCredentials');
    }
  });
});

// Establishing a remote target changes the credential answer for the operator,
// because such a target cannot register a device user at all. Nothing between
// there and Start would ask again, so the refusal has to go with the answer.
describe('a refusal and a remote target', () => {
  it('is forgotten when the answer is changed for the operator', async () => {
    const flow = await readyFlow(
      stub({
        preflight: async () => ({
          conflicts: [
            {
              sourceId: '4',
              name: 'Pump West',
              externalId: 'PUMP-WEST-001',
              externalType: 'c8y_Serial',
              targetId: '8801'
            }
          ]
        })
      })
    );
    flow.chooseCredentials('file');
    await flow.ensureCredentialCheck();
    expect(flow.credentialsRefused).toBe(true);

    await flow.establishRemoteTarget('h', 't-remote');

    expect(flow.choices.credentials).toBe('none');
    expect(flow.credentialsRefused).toBe(false);
  });
});

// Which devices would be registered depends on which relationships are
// followed, so the check has to walk the same ones the migration will. A
// pre-flight over a different set answers about different devices.
describe('the credential check and the migration agree on scope', () => {
  async function scopeOf(change: Partial<import('./flow').Choices>) {
    const asked: Record<string, unknown>[] = [];
    const flow = await readyFlow(
      stub({
        preflight: async request => {
          asked.push(request as Record<string, unknown>);
          return {};
        }
      })
    );
    flow.changeScope(change);
    flow.chooseCredentials('file');
    await flow.ensureCredentialCheck();
    const request = flow.request();
    return {
      preflight: asked.at(-1)!,
      migration: request as unknown as Record<string, unknown>
    };
  }

  it('on a narrowed set of relationships', async () => {
    const { preflight, migration } = await scopeOf({ childTypes: 'asset' });
    expect(preflight.childTypes).toBe(migration.childTypes);
    expect(preflight.noChildren).toBe(migration.noChildren);
  });

  it('on no relationships at all', async () => {
    const { preflight, migration } = await scopeOf({ noChildren: true });
    expect(preflight.noChildren).toBe(true);
    expect(preflight.noChildren).toBe(migration.noChildren);
  });

  it('on the default, which both leave for the service to apply', async () => {
    const { preflight, migration } = await scopeOf({});
    expect(preflight.childTypes).toBeUndefined();
    expect(migration.childTypes).toBeUndefined();
    expect(preflight.noChildren).toBeUndefined();
  });
});

/**
 * What the platform's select is handed.
 *
 * `c8y-select` has no per-item disabled state, so a tenant that cannot be chosen
 * is left out rather than shown greyed. The rule is still the one above - these
 * are the same lists, filtered - and what matters is that the two agree.
 */
describe('the tenant options a select is given', () => {
  it('leaves out the tenant migrations are recorded in', async () => {
    const flow = new MigrationFlow(stub());
    await flow.loadTenants();

    expect(flow.sourceTenants.map(t => t.id)).toContain('t-owner');
    expect(flow.sourceOptions.map(o => o.value)).not.toContain('t-owner');
  });

  it('leaves out the source tenant once one is chosen', async () => {
    const flow = new MigrationFlow(stub());
    await flow.loadTenants();
    expect(flow.targetOptions.map(o => o.value)).toContain('t-a');

    flow.chooseSource('t-a');

    expect(flow.targetOptions.map(o => o.value)).not.toContain('t-a');
    expect(flow.targetOptions.map(o => o.value)).toContain('t-b');
  });

  // Whatever is offered has to be choosable: an option carrying a reason it
  // cannot be used is exactly what this list exists to avoid.
  it('offers nothing that carries a reason it cannot be chosen', async () => {
    const flow = new MigrationFlow(stub());
    await flow.loadTenants();
    flow.chooseSource('t-a');

    for (const [options, tenants] of [
      [flow.sourceOptions, flow.sourceTenants],
      [flow.targetOptions, flow.targetTenants]
    ] as const) {
      for (const option of options) {
        expect(tenants.find(t => t.id === option.value)?.unavailable).toBe('');
      }
    }
  });

  it('labels an option the way the list does', async () => {
    const flow = new MigrationFlow(stub());
    await flow.loadTenants();

    const option = flow.sourceOptions.find(o => o.value === 't-a');
    expect(option?.label).toBe(flow.sourceTenants.find(t => t.id === 't-a')?.label);
  });
});
