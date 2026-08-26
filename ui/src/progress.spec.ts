import { deviceProgress, isFinished, isRunning, present } from './progress';

describe('the progress bar', () => {
  it('reads how far through the devices a run is', () => {
    expect(
      deviceProgress(['Migrating data for 9 devices, one at a time', 'Device 3 of 9'])
    ).toEqual({ current: 3, total: 9, percent: 33 });
  });

  it('takes the last statement, not the first', () => {
    expect(deviceProgress(['Device 1 of 4', 'Device 2 of 4', 'Device 3 of 4'])?.current).toBe(3);
  });

  // The bar is inferred from wording the API does not promise. If that wording
  // ever changes there must be no bar, rather than a bar showing a number
  // nobody can justify.
  it('shows nothing when no line says how far along it is', () => {
    expect(deviceProgress(['Exporting the hierarchy from the source tenant'])).toBeUndefined();
    expect(deviceProgress([])).toBeUndefined();
    expect(deviceProgress(undefined)).toBeUndefined();
  });

  it('ignores a line that looks like progress but cannot be', () => {
    expect(deviceProgress(['Device 12 of 9'])).toBeUndefined();
    expect(deviceProgress(['Device 3 of 0'])).toBeUndefined();
  });
});

describe('what a status says', () => {
  it('reads a finished run plainly', () => {
    expect(present({ status: 'SUCCEEDED', countsVerified: true }).label).toBe('Succeeded');
    expect(present({ status: 'SUCCEEDED', countsVerified: true }).caveat).toBeUndefined();
  });

  // The one field that must never be dropped on the way to a screen: a run that
  // was cut short cannot vouch for what it did.
  it('warns that a cancelled run cannot vouch for itself', () => {
    const shown = present({ status: 'CANCELLED', countsVerified: false, runId: 'r1' });

    expect(shown.tone).toBe('warning');
    expect(shown.caveat).toContain('not known');
    expect(shown.caveat).toContain('run id');
  });

  it('warns the same way about an interrupted run', () => {
    expect(present({ status: 'INTERRUPTED', countsVerified: false }).caveat).toContain('not known');
  });

  // A failure the engine reported itself did stop where it says it stopped, so
  // its numbers stand - but objects may exist in the target.
  it('points a failed run at its run id', () => {
    const shown = present({ status: 'FAILED', countsVerified: true, runId: 'r1' });

    expect(shown.tone).toBe('danger');
    expect(shown.caveat).toContain('may already exist');
  });

  it('says nothing misleading about a failure that created nothing', () => {
    expect(present({ status: 'FAILED', countsVerified: true }).caveat).toBeUndefined();
  });

  it('knows which jobs are still going', () => {
    expect(isRunning({ status: 'QUEUED' })).toBe(true);
    expect(isRunning({ status: 'RUNNING' })).toBe(true);
    expect(isRunning({ status: 'SUCCEEDED' })).toBe(false);

    expect(isFinished({ status: 'INTERRUPTED' })).toBe(true);
    expect(isFinished({ status: 'RUNNING' })).toBe(false);
  });
});
