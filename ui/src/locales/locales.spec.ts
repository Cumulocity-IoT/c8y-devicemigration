import { readdirSync, readFileSync } from 'fs';
import { join } from 'path';

import { translatable } from './extract';

/**
 * The catalogues have to cover what the plugin says, and say nothing else.
 *
 * A missing translation is invisible - the platform falls back to the English
 * source, so a German user sees English and nobody notices for a year. A stale
 * one is worse than useless: it survives the string it belonged to and makes the
 * catalogue look complete. Both are cheap to check and impossible to spot by
 * reading.
 */

/** The msgid/msgstr pairs in a catalogue, as the build reads them. */
function catalogue(file: string): Map<string, string> {
  const text = readFileSync(join(__dirname, file), 'utf8');
  const entries = new Map<string, string>();

  // Enough of the format for files this repository writes: single-line msgid and
  // msgstr, in that order, with C-style escapes.
  const pattern = /^msgid\s+"((?:[^"\\]|\\.)*)"\s*\nmsgstr\s+"((?:[^"\\]|\\.)*)"/gm;
  for (const match of text.matchAll(pattern)) {
    const id = unescape(match[1]);
    if (id === '') {
      continue; // the header
    }
    entries.set(id, unescape(match[2]));
  }
  return entries;
}

function unescape(text: string): string {
  return text.replace(/\\"/g, '"').replace(/\\\\/g, '\\');
}

describe('the German catalogue', () => {
  const german = catalogue('de.po');

  it('translates every string the plugin can show', () => {
    const missing = translatable().filter(text => !german.has(text));

    expect(missing).toEqual([]);
  });

  it('has nothing in it that the plugin no longer says', () => {
    const known = new Set(translatable());
    const stale = [...german.keys()].filter(id => !known.has(id));

    expect(stale).toEqual([]);
  });

  it('leaves nothing untranslated', () => {
    const empty = [...german].filter(([, translation]) => translation.trim() === '');

    expect(empty.map(([id]) => id)).toEqual([]);
  });

  // A parameter dropped in translation is a sentence with a hole in it, and it
  // only shows up in the language nobody on the team reads.
  it('keeps every parameter of every string it translates', () => {
    const wrong: string[] = [];
    for (const [id, translation] of german) {
      const wanted = [...id.matchAll(/{{\s*(\w+)\s*}}/g)].map(m => m[1]).sort();
      const got = [...translation.matchAll(/{{\s*(\w+)\s*}}/g)].map(m => m[1]).sort();
      if (wanted.join(',') !== got.join(',')) {
        wrong.push(`${id} -> ${translation}`);
      }
    }

    expect(wrong).toEqual([]);
  });
});

// A string can be in every catalogue and still show up in English: the mistake
// is forgetting the pipe, and the German user is the only one who would notice.
// These two rules cover the ways it has actually happened here.
describe('the templates', () => {
  // Inline templates count: they are templates, and the pipe can go missing in
  // one just as easily.
  const templates = readdirSync(join(__dirname, '..'), { recursive: true, encoding: 'utf8' })
    .filter(name => name.endsWith('.html') || (name.endsWith('.ts') && !name.endsWith('.spec.ts')))
    .map(name => ({ name, text: readFileSync(join(__dirname, '..', name), 'utf8') }));

  it('has templates to check', () => {
    expect(templates.length).toBeGreaterThan(0);
  });

  // label() turns an API value into a word from the catalogue - which then has to
  // be translated, or the word arrives in English.
  it('translates every label it renders', () => {
    const unpiped: string[] = [];
    for (const { name, text } of templates) {
      for (const match of text.matchAll(/{{[^}]*?label\([^)]*\)[^{}]*?}}/gs)) {
        if (!match[0].includes('| translate')) {
          unpiped.push(`${name}: ${match[0]}`);
        }
      }
    }

    expect(unpiped).toEqual([]);
  });

  // A failure's own title comes from our catalogue as well.
  it('translates every failure title it renders', () => {
    const unpiped: string[] = [];
    for (const { name, text } of templates) {
      for (const match of text.matchAll(/{{[^}]*?\.title[^{}]*?}}/gs)) {
        if (!match[0].includes('| translate')) {
          unpiped.push(`${name}: ${match[0]}`);
        }
      }
    }

    expect(unpiped).toEqual([]);
  });
});

describe('the English catalogues', () => {
  it('exist for both spellings, which is what makes them selectable', () => {
    expect(catalogue('en.po').size).toBeGreaterThan(0);
    expect(catalogue('en-US.po').size).toBeGreaterThan(0);
  });

  // The source is British English, so these hold only what the source cannot
  // say for itself: the disambiguated key, and the American spellings.
  it('only differ from the source where they have to', () => {
    const known = new Set(translatable());
    for (const file of ['en.po', 'en-US.po']) {
      const stale = [...catalogue(file).keys()].filter(id => !known.has(id));
      expect(stale).toEqual([]);
    }
  });

  it('spells a cancelled job the American way for American English', () => {
    expect(catalogue('en-US.po').get('Cancelled')).toBe('Canceled');
    expect(catalogue('en.po').has('Cancelled')).toBe(false);
  });
});
