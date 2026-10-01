import { DEFAULT_CHOICES } from './flow';
import { hierarchyNote, pluralKind } from './hierarchy';

/**
 * The sentence that stands in for the relationship checkboxes.
 *
 * What matters is that it keeps describing what would actually happen: a note
 * still reading "assets and devices" after somebody narrowed the hierarchy would
 * be worse than the controls it replaced.
 */

/** The sentence with its parameters filled in, as a reader would see it. */
function rendered(choices: Parameters<typeof hierarchyNote>[0]): string {
  const note = hierarchyNote(choices, 'and');
  return Object.entries(note.params ?? {}).reduce(
    (text, [key, value]) => text.replace(`{{ ${key} }}`, value),
    note.text
  );
}

describe('the hierarchy note', () => {
  it('describes the default a migration starts with', () => {
    expect(rendered(DEFAULT_CHOICES)).toBe('Note: child assets and devices are migrated.');
  });

  it('follows the relationships that are actually ticked', () => {
    expect(rendered({ ...DEFAULT_CHOICES, childTypes: 'asset' })).toBe(
      'Note: child assets are migrated.'
    );
    expect(rendered({ ...DEFAULT_CHOICES, childTypes: 'device' })).toBe(
      'Note: child devices are migrated.'
    );
    expect(rendered({ ...DEFAULT_CHOICES, childTypes: 'asset,device,addition' })).toBe(
      'Note: child assets, devices and additions are migrated.'
    );
  });

  // Data is a separate question, and whether it comes too changes what the
  // sentence is about rather than adding a clause to the end of it.
  it('says so when device data comes too', () => {
    expect(rendered({ ...DEFAULT_CHOICES, dataTypes: 'measurements' })).toBe(
      'Note: child assets and devices and their data are migrated.'
    );
    expect(rendered({ ...DEFAULT_CHOICES, noChildren: true, dataTypes: 'alarms' })).toBe(
      'Note: only this object and its data are migrated.'
    );
  });

  // Unticking every relationship means the same thing as asking for no children,
  // which is also how the request is built. The note has to agree with that.
  it('treats no relationships and no children as the same thing', () => {
    const noChildren = rendered({ ...DEFAULT_CHOICES, noChildren: true });
    const nothingTicked = rendered({ ...DEFAULT_CHOICES, childTypes: '' });

    expect(noChildren).toBe('Note: only this object is migrated, without any children.');
    expect(nothingTicked).toBe(noChildren);
  });

  // The sentence carries the grammar, so a translator gets whole sentences
  // rather than fragments to assemble.
  it('is a literal sentence with the kinds as the only variable', () => {
    for (const choices of [
      DEFAULT_CHOICES,
      { ...DEFAULT_CHOICES, dataTypes: 'events' },
      { ...DEFAULT_CHOICES, noChildren: true }
    ]) {
      const note = hierarchyNote(choices, 'and');
      expect(note.text).toMatch(/^Note: /);
      expect(Object.keys(note.params ?? {})).toEqual(
        note.text.includes('{{ kinds }}') ? ['kinds'] : []
      );
    }
  });

  // An unknown kind is shown rather than hidden: the API could grow one before
  // this does, and a note that silently dropped it would be untrue.
  it('shows a kind it does not know rather than dropping it', () => {
    expect(pluralKind('something_new')).toBe('something_new');
    expect(rendered({ ...DEFAULT_CHOICES, childTypes: 'asset,something_new' })).toBe(
      'Note: child assets and something_new are migrated.'
    );
  });
});
