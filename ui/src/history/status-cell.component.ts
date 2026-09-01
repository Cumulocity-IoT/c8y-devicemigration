import { Component } from '@angular/core';
import { CellRendererContext } from '@c8y/ngx-components';

import type { GridRow } from './rows';

/**
 * The status column: the platform's own tag, in the tone of the outcome.
 *
 * `.tag` is Cumulocity's tag/label component — the stylesheet marks the older
 * `.label` deprecated in its favour — so success is the light green on dark
 * green the platform means by success, and this plugin looks like the screens
 * around it rather than like itself.
 *
 * One tag, holding one status. Anything that *qualifies* the status — how far a
 * running job has got, what cannot be believed about a finished one — is the
 * tooltip, so the column stays a word wide and scannable. The value the grid
 * sorts and searches on is still that word.
 *
 * Which tone belongs to which outcome is decided by `outcome` and `statusTag`
 * in [rows.ts](rows.ts), with tests. This is wiring.
 */
@Component({
  selector: 'c8y-device-migration-status-cell',
  standalone: true,
  template: `
    @if (row(); as r) {
      <span [class]="r.statusClass" [title]="tooltip(r)">{{ r.status }}</span>
    }
  `
})
export class StatusCellComponent {
  constructor(private readonly context: CellRendererContext) {}

  row(): GridRow | undefined {
    return this.context.item as GridRow | undefined;
  }

  tooltip(row: GridRow): string {
    return row.detail ? `${row.status} — ${row.detail}` : row.status;
  }
}
