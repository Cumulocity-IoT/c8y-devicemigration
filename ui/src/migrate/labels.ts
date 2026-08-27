import { gettext } from '../i18n';

/**
 * The API's values, as words on a screen.
 *
 * A table rather than capitalising the value, for two reasons. The words are
 * translatable and a capitalised `asset` is not - extraction tools cannot see a
 * string that is built at runtime, so a marked table is the only way these reach
 * a catalogue at all. And capitalisation is not translation: `device` is `Gerät`,
 * and no amount of it would have got there.
 */
const LABELS: Record<string, string> = {
  asset: gettext('Asset'),
  device: gettext('Device'),
  addition: gettext('Addition'),
  measurements: gettext('Measurements'),
  events: gettext('Events'),
  alarms: gettext('Alarms')
};

/**
 * What to call one of the API's kinds.
 *
 * An unknown value comes back as it arrived: the API could grow a kind before
 * this does, and showing it raw is better than hiding it.
 */
export function kindLabel(kind: string): string {
  return LABELS[kind] ?? kind;
}
