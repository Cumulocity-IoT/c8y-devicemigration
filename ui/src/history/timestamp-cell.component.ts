import { Component } from '@angular/core';
import { CellRendererContext } from '@c8y/ngx-components';

import type { GridRow } from './rows';

/**
 * The timestamp column: the platform's formatting, the raw value's ordering.
 *
 * This exists because the grid sorts and searches on a column's `path` and
 * nothing else - `doClientSideSorting` reads `get(item, path)` and ignores
 * `sortingConfig`, which only ever reaches a server-side query. Pointed at the
 * formatted date, that sorts a *localised string* lexicographically: with
 * `01/09/26` and `28/08/26` in the same list, September lands before August and
 * "newest first" is not what the reader gets.
 *
 * So the column's path is the raw ISO timestamp the service recorded, which
 * sorts chronologically because it is UTC and fixed-width, and this renderer
 * shows the locale-formatted text instead. The one cost is that the search box
 * matches a timestamp in its ISO form (`2026-08-28`) rather than the reader's,
 * which is a fair trade for a list whose whole point is being in order.
 */
@Component({
  selector: 'c8y-device-migration-timestamp-cell',
  standalone: true,
  template: `{{ formatted() }}`
})
export class TimestampCellComponent {
  constructor(private readonly context: CellRendererContext) {}

  formatted(): string {
    return (this.context.item as GridRow | undefined)?.started ?? '';
  }
}
