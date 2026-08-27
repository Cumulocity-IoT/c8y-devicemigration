import { message, type Message } from '../i18n';

/**
 * Whether a start date is one the engine will accept.
 *
 * A mirror of ParseDate in internal/export/window.go, deliberately no wider:
 *
 *   - nothing, meaning the configured default
 *   - a relative offset, `-14d`, in seconds, minutes, hours, days or weeks
 *   - an absolute time in one of the shapes Cumulocity itself emits:
 *     2026-01-01, 2026-01-01T00:00:00, or either with a zone
 *
 * It exists so a typo is a message under the field rather than a refusal from
 * the service at the top of the dialogue. The service remains the authority - it
 * is the one that will parse this for real - so a refusal from it is shown on the
 * field too. This only makes the common case immediate.
 *
 * Being no wider matters as much as being no narrower: JavaScript's own
 * Date.parse accepts things Go will not ("March 5 2020"), and accepting them
 * here would move the refusal back to where it came from.
 */

/** A relative offset: minus, a count, and a unit the engine knows. */
const RELATIVE = /^-\d+[smhdw]$/;

/** A date on its own. */
const DATE = /^(\d{4})-(\d{2})-(\d{2})$/;

/**
 * A date and time, with an optional zone - and fractional seconds only with one.
 *
 * That last part is not fussiness: the engine's layouts are RFC 3339, which
 * requires a zone, and a plain `2006-01-02T15:04:05`, which has no fraction. So
 * `...T08:30:00.000` on its own is refused by the engine, and refusing it here
 * keeps the two answers the same.
 */
const DATE_TIME =
  /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:(?:\.\d+)?(Z|[+-]\d{2}:?\d{2})|)$/;

/** What is wrong with a start date, or nothing when it is fine. */
export function windowProblem(value: string): Message | undefined {
  const text = value.trim();
  if (text === '') {
    return undefined;
  }
  if (RELATIVE.test(text)) {
    return undefined;
  }

  const parts = DATE.exec(text) ?? DATE_TIME.exec(text);
  if (!parts) {
    return message('Use a relative offset like -14d, or a date like 2026-01-01.');
  }
  if (!isRealDate(Number(parts[1]), Number(parts[2]), Number(parts[3]))) {
    return message('There is no such date as {{ date }}.', {
      date: `${parts[1]}-${parts[2]}-${parts[3]}`
    });
  }
  if (parts.length > 4 && !isRealTime(Number(parts[4]), Number(parts[5]), Number(parts[6]))) {
    return message('That is not a time of day.');
  }
  return undefined;
}

/**
 * Whether those three numbers name a day that exists.
 *
 * Built and read back rather than range-checked, because February is the whole
 * problem: 2026-02-30 passes every range check there is.
 */
function isRealDate(year: number, month: number, day: number): boolean {
  const date = new Date(Date.UTC(year, month - 1, day));
  return (
    date.getUTCFullYear() === year &&
    date.getUTCMonth() === month - 1 &&
    date.getUTCDate() === day
  );
}

function isRealTime(hour: number, minute: number, second: number): boolean {
  return hour <= 23 && minute <= 59 && second <= 60;
}

/**
 * The moment a field value names, when it names one.
 *
 * Only an absolute value can be shown in a picker; `-14d` is a rule rather than
 * a moment, and a picker showing "today minus fourteen days" would be showing
 * something the field does not say. So that returns nothing, and the picker sits
 * empty next to a relative window.
 */
export function dateFromWindow(value: string): Date | null {
  // A relative value is refused by Date as well, but saying so here is the
  // point: the reason it is not shown is that it is not a moment.
  if (windowProblem(value) || value.trim() === '' || RELATIVE.test(value.trim())) {
    return null;
  }
  const date = new Date(value.trim());
  return Number.isNaN(date.getTime()) ? null : date;
}

/**
 * A date from the picker, in the field's own syntax.
 *
 * An absolute instant, so it says exactly what was picked no matter who reads it
 * later. The relative form is the one to prefer by hand - it keeps meaning the
 * same thing tomorrow - but a picked date is a moment, and writing it as one is
 * the only honest thing to do with it.
 */
export function windowFromDate(picked: Date | string | undefined | null): string {
  if (!picked) {
    return '';
  }
  const date = picked instanceof Date ? picked : new Date(picked);
  if (Number.isNaN(date.getTime())) {
    return '';
  }
  return date.toISOString();
}
