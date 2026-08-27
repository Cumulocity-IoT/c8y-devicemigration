import { dateFromWindow, windowFromDate, windowProblem } from './window';

// The cases are the engine's own: internal/export/window.go accepts a relative
// offset or one of five absolute layouts, and refuses everything else. Where this
// disagrees with that file, this file is wrong.
describe('the start date a migration can be given', () => {
  it('accepts nothing at all, which means the configured default', () => {
    expect(windowProblem('')).toBeUndefined();
    expect(windowProblem('   ')).toBeUndefined();
  });

  it('accepts every relative unit the engine understands', () => {
    for (const value of ['-30s', '-15m', '-6h', '-14d', '-2w', '-0d', '-365d']) {
      expect(windowProblem(value)).toBeUndefined();
    }
  });

  it('accepts the absolute shapes Cumulocity itself emits', () => {
    for (const value of [
      '2026-01-01',
      '2026-01-01T00:00:00',
      '2026-01-01T00:00:00Z',
      '2026-08-27T08:30:00.000Z',
      '2026-08-27T08:30:00.123456789Z',
      '2026-08-27T08:30:00+02:00',
      '2026-08-27T08:30:00+0200'
    ]) {
      expect(windowProblem(value)).toBeUndefined();
    }
  });

  it('refuses what the engine refuses, with something to do about it', () => {
    for (const value of ['14d', '-14', '-14x', 'yesterday', '2026/01/01', '-1.5d', '- 14d']) {
      expect(windowProblem(value)?.text).toMatch(/relative offset like -14d/);
    }
  });

  // The reason this is not a range check: 2026-02-30 passes every range check.
  it('refuses a day that does not exist, and names it', () => {
    expect(windowProblem('2026-02-30')).toEqual({
      text: 'There is no such date as {{ date }}.',
      params: { date: '2026-02-30' }
    });
    expect(windowProblem('2026-13-01')?.text).toMatch(/no such date/);
    expect(windowProblem('2025-02-29')?.text).toMatch(/no such date/);
    expect(windowProblem('2024-02-29')).toBeUndefined(); // a leap year
  });

  // RFC 3339 requires a zone with fractional seconds, and the engine's only
  // zoneless layout has no fraction. Neither accepts this.
  it('refuses fractional seconds without a zone, as the engine does', () => {
    expect(windowProblem('2026-08-27T08:30:00.000')).toBeDefined();
  });

  it('refuses a time of day that does not exist', () => {
    expect(windowProblem('2026-01-01T24:00:00')?.text).toMatch(/not a time of day/);
    expect(windowProblem('2026-01-01T00:60:00')?.text).toMatch(/not a time of day/);
  });

  // JavaScript would take these. Go would not, and Go is what runs.
  it('does not accept dates only JavaScript understands', () => {
    for (const value of ['March 5 2020', '2020', '5/3/2020', 'Thu, 01 Jan 2026 00:00:00 GMT']) {
      expect(windowProblem(value)).toBeDefined();
    }
  });
});

describe('what the picker shows', () => {
  it('shows the moment an absolute value names', () => {
    expect(dateFromWindow('2026-08-27T08:30:00.000Z')?.toISOString()).toBe(
      '2026-08-27T08:30:00.000Z'
    );
    expect(dateFromWindow('2026-08-27')?.getUTCFullYear()).toBe(2026);
  });

  // -14d is a rule, not a moment. A picker showing today-minus-fourteen-days
  // would be showing something the field does not say.
  it('shows nothing for a relative window', () => {
    expect(dateFromWindow('-14d')).toBeNull();
  });

  it('shows nothing for an empty or unreadable value', () => {
    expect(dateFromWindow('')).toBeNull();
    expect(dateFromWindow('last tuesday')).toBeNull();
    expect(dateFromWindow('2026-02-30')).toBeNull();
  });
});

describe('a date chosen from the picker', () => {
  it('becomes an absolute instant the engine accepts', () => {
    const written = windowFromDate(new Date(Date.UTC(2026, 7, 27, 8, 30)));

    expect(written).toBe('2026-08-27T08:30:00.000Z');
    expect(windowProblem(written)).toBeUndefined();
  });

  it('takes what a picker hands over as a string', () => {
    expect(windowFromDate('2026-08-27T08:30:00.000Z')).toBe('2026-08-27T08:30:00.000Z');
  });

  it('is nothing when nothing was picked', () => {
    expect(windowFromDate(undefined)).toBe('');
    expect(windowFromDate(null)).toBe('');
    expect(windowFromDate('not a date')).toBe('');
  });
});
