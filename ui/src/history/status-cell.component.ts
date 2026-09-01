import { Component } from '@angular/core';
import { CellRendererContext, CoreModule } from '@c8y/ngx-components';

import type { GridRow } from './rows';

/**
 * The status column: an icon in the tone of the outcome, with the words in its
 * tooltip.
 *
 * A column of five coloured words is read as five words; a column of glyphs is
 * scanned in one look, which is what a list of past migrations is for. The
 * wording is not thrown away though - it is the tooltip, it is what the grid
 * still sorts and searches on, and it is what a screen reader gets, because a
 * status that only exists as a colour is not a status for everybody.
 *
 * Which glyph and which colour is decided by `statusBadge` in
 * [rows.ts](rows.ts) and arrives on the row already computed. This component is
 * wiring, deliberately: the decision is a pure function with tests, and this has
 * neither.
 */
@Component({
  selector: 'c8y-device-migration-status-cell',
  standalone: true,
  imports: [CoreModule],
  template: `
    @if (row(); as r) {
      <span [title]="r.status">
        <i [c8yIcon]="r.statusIcon" [class]="r.statusClass"></i>
        <span class="sr-only">{{ r.status }}</span>
      </span>
    }
  `
})
export class StatusCellComponent {
  constructor(private readonly context: CellRendererContext) {}

  row(): GridRow | undefined {
    return this.context.item as GridRow | undefined;
  }
}
