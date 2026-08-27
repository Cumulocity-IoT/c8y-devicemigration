/**
 * Marking what a person reads.
 *
 * Every string this plugin shows goes through `| translate` in a template, and
 * the English source text is its own key - the gettext arrangement the platform
 * uses. The catalogues live in `src/locales/*.po`, and a key with no entry falls
 * back to the source, which is why a missing German translation shows English
 * rather than nothing.
 *
 * `gettext` here is a marker and nothing else: it returns what it is given, so
 * that a string produced in TypeScript is findable by the same extraction tools
 * that read the templates. It is deliberately not the `gettext` from
 * @c8y/ngx-components, which would pull Angular into modules that are tested
 * without it - the name is the same so that tooling looking for `gettext(` finds
 * these too.
 */
export function gettext(text: string): string {
  return text;
}

/**
 * A string to show, with the values that belong in it.
 *
 * Used where a sentence has numbers or names in it. The whole sentence is the
 * key, so a translator can move the parameters to wherever the language wants
 * them - which is the reason not to assemble sentences from fragments.
 */
export interface Message {
  text: string;
  params?: Record<string, string | number>;
}

/** A message, marked for translation. */
export function message(text: string, params?: Record<string, string | number>): Message {
  return params ? { text: gettext(text), params } : { text: gettext(text) };
}
