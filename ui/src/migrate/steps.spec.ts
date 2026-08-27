import { readiness, type MigrationState } from './steps';

/**
 * A disabled Start is only acceptable if it says why. These tests are as much
 * about the sentences as about the boolean.
 */

const complete: MigrationState = {
  sourceTenant: 't1',
  rootId: '9',
  targetTenant: 't2',
  preflightPassed: true,
  estimated: true,
  withinLimits: true
};

describe('what is still missing', () => {
  it('is nothing when everything has been chosen and checked', () => {
    expect(readiness(complete)).toEqual({ ready: true, outstanding: [] });
  });

  it('asks for the choices in the order they have to be made', () => {
    expect(readiness({}).outstanding[0]).toContain('tenant to migrate from');
    expect(readiness({ sourceTenant: 't1' }).outstanding[0]).toContain('device or group');
    expect(readiness({ sourceTenant: 't1', rootId: '9' }).outstanding[0]).toContain(
      'tenant to migrate to'
    );
  });

  // Everything at once, because on one screen there is no reason to reveal the
  // next problem only after the last one is fixed.
  it('lists everything outstanding, not just the first thing', () => {
    expect(readiness({}).outstanding.length).toBeGreaterThan(1);
  });

  it('refuses one tenant on both sides, in those words', () => {
    const both = { sourceTenant: 't1', rootId: '9', targetTenant: 't1' };
    expect(readiness(both).outstanding).toContain('The source and target cannot be the same tenant.');
    expect(readiness(both).ready).toBe(false);
  });

  it('says it is checking the tenants rather than that something is wrong', () => {
    const unchecked = { sourceTenant: 't1', rootId: '9', targetTenant: 't2' };
    expect(readiness(unchecked).outstanding.some(o => o.includes('can be reached'))).toBe(true);
  });

  it('does not mention an estimate before there is anything to estimate', () => {
    const nothing = readiness({});
    expect(nothing.outstanding.some(o => o.includes('how much would move'))).toBe(false);
  });

  it('waits for the estimate once the three choices are made', () => {
    const waiting = { sourceTenant: 't1', rootId: '9', targetTenant: 't2', preflightPassed: true };
    expect(readiness(waiting).outstanding).toContain('Working out how much would move.');
  });

  // The refusal that matters most: over the limits, and the sentence says what
  // to change.
  it('refuses a migration over the limits and says what to narrow', () => {
    const over = readiness({ ...complete, withinLimits: false });

    expect(over.ready).toBe(false);
    expect(over.outstanding.join(' ')).toContain('over the export limits');
    expect(over.outstanding.join(' ')).toContain('data window');
  });
});
