import { Component, Input, type TemplateRef } from '@angular/core';
import { PopoverModule } from 'ngx-bootstrap/popover';

/**
 * The contextual help beside a field: the platform's own `.btn-help`.
 *
 * Markup copied from how Cumulocity's own screens do it - a bare button carrying
 * `btn-help`, an aria-label, and a popover triggered on focus - so this looks and
 * behaves like every other help button in the product rather than like one drawn
 * here.
 *
 * Two things that class already does, and which this therefore must not:
 *
 *   - **It draws the question mark itself**, as a CSS mask on `::before`, in the
 *     info palette colour. There is no icon element, and an icon child would be
 *     hidden anyway - `.btn-help > [class^='dlt-c8y-icon-'] { display: none }`.
 *   - **It sizes and aligns itself inside a label.** `@c8y/style` carries
 *     `label .btn-help { height: 14px; width: 14px }` and
 *     `label:has(.btn-help) { display: flex; align-items: center }`, so neither
 *     this nor the dialogue's own styles align it by hand.
 *
 * Not the platform's `c8y-help` *component*, which is a different thing: it takes
 * a documentation URL and opens the help drawer from the action bar. What belongs
 * beside a field is a sentence about that field.
 */
@Component({
  selector: 'c8y-device-migration-help',
  standalone: true,
  imports: [PopoverModule],
  template: `
    <button
      class="btn-help btn-help--sm"
      type="button"
      [attr.aria-label]="label"
      [popover]="content || text"
      [triggers]="'focus'"
      [placement]="placement"
      container="body"
    ></button>
  `
})
export class HelpComponent {
  /** The sentence to show. Already translated by whoever passes it. */
  @Input() text = '';

  /**
   * Structured content instead, for help that is a list rather than a sentence.
   *
   * ngx-bootstrap's popover takes a TemplateRef as readily as a string, and a
   * template is what several short entries need: four options run together into
   * one paragraph is a wall nobody reads, and it also hands a translator one
   * enormous string where four small ones would do.
   *
   * The template is rendered into the popover's own container, which is on the
   * body - so its styling lives in the global stylesheet rather than in a
   * component, where it could not reach.
   */
  @Input() content?: TemplateRef<unknown>;

  /**
   * What the button is about, for a screen reader. Already translated, for the
   * same reason - and not shown as a heading on the popover, which carries the
   * sentence and nothing else.
   */
  @Input({ required: true }) label!: string;

  /**
   * Which side of the button the popover opens on. Above by default, as the
   * platform's own do; below where the field sits high in a dialogue and a
   * long sentence above it would cover the button and lose its pointer.
   */
  @Input() placement: 'top' | 'bottom' | 'left' | 'right' = 'top';
}
