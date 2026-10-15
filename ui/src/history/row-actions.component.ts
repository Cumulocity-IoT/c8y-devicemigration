import { CommonModule } from '@angular/common';
import { Component } from '@angular/core';
import { CellRendererContext, CoreModule } from '@c8y/ngx-components';
import { BsDropdownModule } from 'ngx-bootstrap/dropdown';

import type { GridRow } from './rows';

/**
 * The actions column: one menu, on every row, whatever the row is.
 *
 * The platform's own row actions change shape with how many of them apply: two
 * are rendered as icons in the row, three as one icon plus a `⋮` menu holding
 * the rest. That threshold is in the grid's template and there is no input for
 * it - so with them a row's affordance would depend on what had happened to the
 * migration. A run nobody had undone would wear a menu, the reverted one under
 * it two buttons, and the same action would live in a different place on
 * adjacent rows.
 *
 * So this column stands in for them, and renders the menu the grid renders for
 * three or more - the same markup, the same classes, the same placement - for
 * every row, with whatever that row offers inside it. Consistency is the whole point:
 * *Remove migration record* is in the same place on the first row and the last,
 * and finding an action never depends on counting what else is available.
 *
 * Which entries a row has is [`actionsFor`](rows.ts), with tests. This is
 * wiring, and `perform` is the row's own callback into the component that built
 * it - the dialogues live there.
 */
@Component({
  selector: 'c8y-device-migration-row-actions',
  standalone: true,
  imports: [CommonModule, CoreModule, BsDropdownModule],
  template: `
    @if (row(); as r) {
      @if (r.actions.length) {
        <div class="dropdown" placement="bottom right" container="body" dropdown>
          <button
            class="dropdown-toggle c8y-dropdown"
            type="button"
            [title]="'Actions' | translate"
            [attr.aria-label]="'Actions' | translate"
            aria-haspopup="true"
            dropdownToggle
          >
            <i c8yIcon="ellipsis-v"></i>
          </button>
          <ul class="dropdown-menu dropdown-menu-right" *dropdownMenu>
            @for (action of r.actions; track action.kind) {
              <li>
                <button type="button" [title]="action.text" (click)="r.perform(action.kind)">
                  <i c8yIcon="{{ action.icon }}" [ngClass]="action.iconClasses"></i>
                  {{ action.text }}
                </button>
              </li>
            }
          </ul>
        </div>
      }
    }
  `,
  styles: [
    `
      /* The grid right-aligns its own actions column by way of a class it only
         adds when it owns one. This column is ours, so the alignment is too. */
      :host {
        display: block;
        text-align: right;
      }
    `
  ]
})
export class RowActionsComponent {
  constructor(private readonly context: CellRendererContext) {}

  row(): GridRow | undefined {
    return this.context.item as GridRow | undefined;
  }
}
