/**
 * What the asset selector says when somebody picks something.
 *
 * This shape is the platform's, and it is not the obvious one: `onSelected`
 * emits an envelope - the whole selection under `items`, and what just changed
 * under `change` - rather than the selected object. Reading it as the object is
 * how a working picker looked broken: every click was ignored and the dialogue
 * went on asking for a device.
 *
 * From @c8y/ngx-components/assets-navigator, AssetSelectorBase.emitChange:
 *
 *     const changes = this.config.multi ? selected : selected[0];
 *     this.onSelected.emit({ items: changes, change: { item, isSelected } });
 *
 * So `items` is one object in single-select and an array in multi, and with
 * modelMode 'simple' each one is `{ id, name }`.
 */

/** One thing the selector can hand back. */
export interface SelectedAsset {
  id?: string | number;
  name?: string;
}

/** The envelope `onSelected` emits. */
export interface AssetSelection {
  items?: SelectedAsset | SelectedAsset[] | null;
  change?: { item?: SelectedAsset; isSelected?: boolean };
}

/** What was chosen, as a migration root. */
export interface ChosenRoot {
  id: string;
  name: string;
}

/**
 * The root a selection means, or nothing when it means "no root any more".
 *
 * Clearing the selection is a real answer, not a missing one: unticking the
 * chosen device has to clear the root, or the dialogue would go on to migrate
 * something nobody has had selected since. It needs no special case - the
 * selector reports it as a selection with nothing in it, because `items` is the
 * selection *after* the change, not the thing that changed. `change.isSelected`
 * is deliberately not consulted: in a multi-select it can be false while other
 * things are still selected.
 */
export function chosenRoot(selection: AssetSelection | undefined): ChosenRoot | undefined {
  if (!selection) {
    return undefined;
  }

  const items = selection.items;
  const first = Array.isArray(items) ? items[0] : items;
  if (!first?.id) {
    return undefined;
  }
  return { id: String(first.id), name: first.name ?? '' };
}
