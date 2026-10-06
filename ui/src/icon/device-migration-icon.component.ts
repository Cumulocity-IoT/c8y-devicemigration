import { Component } from '@angular/core';

/**
 * The feature's icon: the artwork, and nothing about where it sits.
 *
 * Used directly by the list's introduction and its empty state, and through
 * [DeviceMigrationNavigatorIconComponent](navigator-icon.component.ts) in the
 * navigator - which exists only to give the host the tag name the navigator's
 * own layout rules select on.
 *
 * Inlined rather than referenced by name, because the platform's icon classes
 * resolve against `@c8y/style`'s own set and this icon is not in it. A
 * `dlt-c8y-icon-*` class renders its SVG as a **CSS mask** - `::before` from
 * `dlt/<name>.svg`, `::after` from `dlt-filled/<name>.svg`, each coloured from a
 * theme variable - and a mask keeps only the alpha. A two-colour icon cannot
 * travel as one file through that, which is why the artwork comes as two layers
 * and why they are drawn here as real paths.
 *
 * The microservice manifest cannot do any of this: its `icon.class` is a CSS
 * class applied verbatim, so it names the global `c8y-icon-device-migration` and
 * waits on that being registered platform-side, while the places drawn here are
 * already right.
 *
 * Once that class ships in `@c8y/style` these could go through the platform
 * instead - `c8yIcon="c8y-device-migration"` resolves to exactly that class,
 * since IconDirective rewrites a leading `c8y-` to `c8y-icon-`. Worth doing then:
 * it would pick up the theming and the dark-mode handling that come with a real
 * icon. Not now, because the class does not exist in the installed styles and an
 * icon that renders nothing is worse than one drawn by hand.
 *
 * One component and one copy of the artwork, with the differences done in CSS:
 * the template has to be a literal for the compiler to accept it (NG1010), so
 * building it from shared path constants is not an option.
 */
@Component({
  selector: 'c8y-device-migration-icon',
  standalone: true,
  template: `
    <svg
      viewBox="0 0 32 32"
      fill="none"
      focusable="false"
      aria-hidden="true"
      xmlns="http://www.w3.org/2000/svg"
    >
      <!--
        The shape: the two tenants, and the arrow from one to the other. Four
        paths, unsimplified, because this is drawn at 16px as often as at 96px.
      -->
      <g class="shape">
        <path
          d="M22.8584 22.6396L22.1221 23.375L22.6045 23.8574L20.1689 26.293L19.3076 25.4316L17.7109 27.0254L17 26.3145L18.5957 24.7188L17.7334 23.8564L20.1689 21.4209L20.6514 21.9033L21.3857 21.167L22.8584 22.6396Z"
        />
        <path
          d="M27 17.6904L25.3721 19.3174L26.2383 20.1836L23.791 22.6309L21.3438 20.1836L23.791 17.7373L24.6572 18.6035L26.2852 16.9746L27 17.6904Z"
        />
        <path
          d="M18 12H12V18H2V2H18V12ZM8.65137 9.90332L8.16895 9.4209L5.7334 11.8564L6.5957 12.7188L5 14.3145L5.71094 15.0254L7.30762 13.4307L8.16895 14.293L10.6045 11.8574L10.1221 11.375L10.8584 10.6396L9.38574 9.16699L8.65137 9.90332ZM12.6572 6.60352L11.791 5.7373L9.34375 8.18359L11.791 10.6309L14.2383 8.18359L13.3721 7.31738L15 5.69043L14.2852 4.97461L12.6572 6.60352Z"
        />
        <path
          d="M26.8271 3.67969C27.2959 4.04427 27.7321 4.45443 28.1357 4.91016L30.2061 2.83984V8.66016H24.3857L26.9639 6.08203C26.3389 5.32682 25.5837 4.73438 24.6982 4.30469C23.8128 3.875 22.8753 3.66016 21.8857 3.66016H20.2061V2L21.8857 2C22.4847 2 23.0706 2.0651 23.6436 2.19531C24.2295 2.32552 24.7829 2.52083 25.3037 2.78125C25.8376 3.02865 26.3454 3.32813 26.8271 3.67969Z"
        />
      </g>

      <!--
        The accent: the target tenant. Drawn last, so it sits over the shape
        where the two squares overlap - the same stacking the platform gets from
        painting ::after above ::before.
      -->
      <path
        class="accent"
        fill-rule="evenodd"
        clip-rule="evenodd"
        d="M30 30H14V14H30V30ZM16 28H28V16H16V28Z"
      />
    </svg>
  `,
  styles: [
    `
      :host {
        display: inline-block;
        line-height: 1;
      }
      /* 1.1em of whatever the surrounding rule set, which is how every masked
         c8y icon sizes itself - so in the navigator this is the same box the
         entries above and below have. */
      svg {
        width: 1.1em;
        height: 1.1em;
        display: block;
      }

      /* In a page, the shape follows the text. That is what keeps the
         platform's own modifiers meaningful: text-muted on the host mutes it
         exactly as it mutes a real duocolor icon, and the empty state's muted
         colour reaches it without being asked for. */
      .shape {
        fill: currentColor;
      }
      .accent {
        fill: var(--device-migration-icon-accent, #a5daa5);
      }

      /* In the navigator, one colour. Every other entry there is single-colour
         and follows its link through hover and active, so both layers take the
         text colour and the accent drops out - the duocolor version is for the
         page, where it is the only icon of its kind. */
      :host-context(.navigator) .accent {
        fill: currentColor;
      }
    `
  ]
})
export class DeviceMigrationIconComponent {}
