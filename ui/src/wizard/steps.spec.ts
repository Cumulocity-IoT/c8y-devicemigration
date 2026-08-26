import { availability, canStart, next, previous, type WizardState } from './steps';

/**
 * A disabled Next is only acceptable if it says why. These tests are as much
 * about the sentences as about the booleans.
 */

const complete: WizardState = {
  sourceTenant: 't1',
  rootId: '9',
  targetTenant: 't2',
  preflightPassed: true,
  estimated: true,
  withinLimits: true
};

describe('reaching a step', () => {
  it('lets anybody read the introduction and choose a source', () => {
    expect(availability('introduction', {}).reachable).toBe(true);
    expect(availability('source', {}).reachable).toBe(true);
  });

  it('needs a tenant and a root before a target can be chosen', () => {
    expect(availability('target', {}).because).toContain('tenant to migrate from');
    expect(availability('target', { sourceTenant: 't1' }).because).toContain(
      'device or group'
    );
    expect(availability('target', { sourceTenant: 't1', rootId: '9' }).reachable).toBe(true);
  });

  it('refuses one tenant on both sides, in those words', () => {
    const both = { sourceTenant: 't1', rootId: '9', targetTenant: 't1', preflightPassed: true };
    expect(availability('review', both).because).toContain('cannot be the same tenant');
  });

  it('waits for the pre-flight before the review', () => {
    const unchecked = { sourceTenant: 't1', rootId: '9', targetTenant: 't2' };
    expect(availability('review', unchecked).because).toContain('checked');
  });

  it('has no progress to show before a migration exists', () => {
    expect(availability('progress', complete).because).toContain('No migration has been started');
    expect(availability('progress', { ...complete, jobId: 'job-1' }).reachable).toBe(true);
  });
});

describe('starting', () => {
  it('allows a complete, estimated, within-limits migration', () => {
    expect(canStart(complete).reachable).toBe(true);
  });

  it('waits for the estimate', () => {
    expect(canStart({ ...complete, estimated: false }).because).toContain('estimate');
  });

  // The refusal that matters most: over the limits, Start is disabled and the
  // sentence says what to change.
  it('refuses a migration over the limits and says what to narrow', () => {
    const over = canStart({ ...complete, withinLimits: false });

    expect(over.reachable).toBe(false);
    expect(over.because).toContain('over the export limits');
    expect(over.because).toContain('data window');
  });

  it('reports the earliest thing missing, not the last', () => {
    // No target at all: the answer should be about the target, not the estimate.
    expect(canStart({ sourceTenant: 't1', rootId: '9' }).because).toContain(
      'tenant to migrate to'
    );
  });
});

describe('moving between steps', () => {
  it('walks forwards and backwards', () => {
    expect(next('introduction')).toBe('source');
    expect(next('progress')).toBeUndefined();
    expect(previous('source')).toBe('introduction');
    expect(previous('introduction')).toBeUndefined();
  });
});
