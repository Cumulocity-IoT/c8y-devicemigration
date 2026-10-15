import { chosenRoot } from './selection';

describe('what the asset selector hands back', () => {
  // What this pins: onSelected emits an envelope, and reading it as the
  // selected object would silently ignore every click.
  it('reads the selection out of the envelope, not the envelope', () => {
    expect(
      chosenRoot({
        items: { id: '1399229', name: 'Plant Berlin' },
        change: { item: { id: '1399229', name: 'Plant Berlin' }, isSelected: true }
      })
    ).toEqual({ id: '1399229', name: 'Plant Berlin' });
  });

  it('takes the first of a multi-selection, since a migration has one root', () => {
    expect(
      chosenRoot({
        items: [
          { id: '1', name: 'first' },
          { id: '2', name: 'second' }
        ]
      })
    ).toEqual({ id: '1', name: 'first' });
  });

  it('accepts a numeric id, which is what the platform sometimes sends', () => {
    expect(chosenRoot({ items: { id: 1399229, name: 'Plant Berlin' } })?.id).toBe('1399229');
  });

  it('survives an asset with no name rather than refusing it', () => {
    expect(chosenRoot({ items: { id: '9' } })).toEqual({ id: '9', name: '' });
  });

  // Unticking has to clear the root, or the dialogue would migrate something
  // nobody has had selected since.
  it('treats a deselection as no root', () => {
    expect(
      chosenRoot({ items: undefined, change: { item: { id: '1' }, isSelected: false } })
    ).toBeUndefined();
  });

  it('is not fooled by an empty or idless selection', () => {
    expect(chosenRoot(undefined)).toBeUndefined();
    expect(chosenRoot({})).toBeUndefined();
    expect(chosenRoot({ items: [] })).toBeUndefined();
    expect(chosenRoot({ items: null })).toBeUndefined();
    expect(chosenRoot({ items: { name: 'no id' } })).toBeUndefined();
  });
});
