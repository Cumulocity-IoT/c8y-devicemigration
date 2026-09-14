import { Component } from '@angular/core';

import { DeviceMigrationIconComponent } from './device-migration-icon.component';

/**
 * The feature's icon, wearing the element the navigator styles.
 *
 * This exists for its **tag name** rather than for anything it draws. A node's
 * icon is laid out by a type selector, not a class:
 *
 *     c8y-navigator-icon,
 *     device-status {
 *       padding: 0 0 0 16px;
 *       margin-right: 16px;
 *       flex-grow: 0;
 *     }
 *
 * A component supplied through `NavigatorNode.iconComponent` replaces the
 * platform's own `c8y-navigator-icon`, and Angular names the host element after
 * that component's selector - so a differently named host matches none of those
 * rules. The entry then sits flush against the rail with its label crowding it,
 * which is exactly what it did. `.icon` inside carries the size, the same way
 * the platform's own component does (`<i class="icon" [c8yIcon]="icon">`).
 *
 * Sharing the selector with the platform's component is safe because this one is
 * only ever created dynamically, by the navigator, through `createComponent` -
 * which names the type outright and does no selector matching. It is never a
 * template dependency of anything, so the two never meet in one scope.
 *
 * The artwork is [DeviceMigrationIconComponent](device-migration-icon.component.ts),
 * embedded rather than repeated: a template has to be a literal for the compiler
 * to accept it, so one copy of the paths means one component drawing them.
 */
@Component({
  selector: 'c8y-navigator-icon',
  standalone: true,
  imports: [DeviceMigrationIconComponent],
  template: `<c8y-device-migration-icon class="icon"></c8y-device-migration-icon>`
})
export class DeviceMigrationNavigatorIconComponent {}
