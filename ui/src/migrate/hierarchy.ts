import { gettext } from '../i18n';

/**
 * What the dialogue says about the hierarchy when it is not asking about it.
 *
 * The relationship checkboxes answer a question most people do not have: the
 * default follows assets and devices, which is what almost every migration wants.
 * So the choice is folded away behind a sentence describing what would happen,
 * and the sentence has to stay true - a note that still said "assets and devices"
 * after somebody narrowed it would be worse than the checkboxes it replaced.
 *
 * Built here rather than in the component so the wording can be tested without a
 * browser, and so every sentence it can produce is a literal a catalogue
 * extractor can see.
 */

/**
 * The kinds as they read inside a sentence.
 *
 * Separate from `kindLabel`, which names a checkbox and is singular and
 * capitalised. These are lower case in English because they sit mid-sentence -
 * and a *translation* of them is free to capitalise, which German needs, since
 * its nouns are capitalised wherever they appear.
 */
const PLURALS: Record<string, string> = {
  asset: gettext('assets'),
  device: gettext('devices'),
  addition: gettext('additions')
};

/** One kind, plural, for use in a sentence. Unknown kinds come back as they are. */
export function pluralKind(kind: string): string {
  return PLURALS[kind] ?? kind;
}

/** A sentence and whatever it interpolates. */
export interface Note {
  text: string;
  params?: Record<string, string>;
}

/** The choices this note is about. */
export interface HierarchyChoices {
  childTypes: string;
  noChildren: boolean;
  dataTypes: string;
}

/**
 * What would be migrated, as a sentence.
 *
 * Four of them rather than one assembled from fragments: whether data comes too
 * changes the verb's subject, and "only this object" is not the same sentence as
 * "child assets" with an empty list. A translator needs each as a whole.
 *
 * `and` is passed in rather than reached for, so this stays a pure function - the
 * component has the translator and supplies the word.
 */
export function hierarchyNote(choices: HierarchyChoices, and: string): Note {
  const kinds = split(choices.childTypes).map(pluralKind);
  const withData = split(choices.dataTypes).length > 0;

  // Unticking every relationship means the same thing as asking for no children,
  // which is also how the request is built - see MigrationFlow.scopeOfRequest.
  if (choices.noChildren || kinds.length === 0) {
    return {
      text: withData
        ? gettext('Note: only this object and its data are migrated.')
        : gettext('Note: only this object is migrated, without any children.')
    };
  }

  return {
    text: withData
      ? gettext('Note: child {{ kinds }} and their data are migrated.')
      : gettext('Note: child {{ kinds }} are migrated.'),
    params: { kinds: joinWords(kinds, and) }
  };
}

/** `a`, `a and b`, `a, b and c`. */
function joinWords(words: string[], and: string): string {
  if (words.length <= 1) {
    return words.join('');
  }
  return `${words.slice(0, -1).join(', ')} ${and} ${words[words.length - 1]}`;
}

/** A comma separated list as its parts, ignoring the empty string. */
function split(value: string): string[] {
  return value
    .split(',')
    .map(part => part.trim())
    .filter(part => part !== '');
}
