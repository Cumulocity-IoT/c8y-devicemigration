import { CommonModule } from '@angular/common';
import { Component, EventEmitter, Input, OnInit, Output } from '@angular/core';
import { FetchClient, InventoryService } from '@c8y/client';
import { CoreModule, InventorySearchService } from '@c8y/ngx-components';
import {
  AssetNodeService,
  AssetSelectorComponent,
  DeviceGroupService,
  GroupNodeService
} from '@c8y/ngx-components/assets-navigator';

import { gettext } from '../i18n';
import { TenantPlatformClient } from '../api/platform-client';
import { chosenRoot, type AssetSelection, type ChosenRoot } from './selection';

/**
 * Choosing what to migrate, with the platform's own asset selector.
 *
 * The selector is the right control for this - it searches, pages, expands a
 * group and looks like every other picker in Cumulocity - and out of the box it
 * cannot work here: it reads through `InventoryService`, which talks to the
 * tenant the browser is signed in to, and the hierarchy being migrated is in
 * another one.
 *
 * So this component replaces that dependency, and everything the selector
 * resolves through it, with copies built on a client that reads through the
 * microservice's inventory proxy. Nothing about the component is patched: it
 * makes the same calls it always makes, and they land in the source tenant.
 *
 * The provider list is the whole trick, and its completeness is what makes it
 * safe. Every service in the chain that touches the inventory is listed here,
 * because a service left off the list resolves from the application's root
 * injector instead - and then quietly lists the wrong tenant's devices, which is
 * the one failure this feature must not have.
 */
@Component({
  selector: 'c8y-device-migration-root-picker',
  standalone: true,
  imports: [CommonModule, CoreModule, AssetSelectorComponent],
  providers: [
    {
      // The session's own client does the sending; this only rewrites where to.
      provide: TenantPlatformClient,
      useFactory: (session: FetchClient) => new TenantPlatformClient(session),
      deps: [FetchClient]
    },
    {
      provide: InventoryService,
      useFactory: (proxy: TenantPlatformClient) => new InventoryService(proxy),
      deps: [TenantPlatformClient]
    },
    // Each of these reads the inventory. Provided here so that their own
    // inject(InventoryService) resolves to the proxied one above rather than to
    // the root injector's.
    InventorySearchService,
    AssetNodeService,
    GroupNodeService,
    DeviceGroupService
  ],
  template: `
    @if (tenant) {
      <c8y-asset-selector
        [config]="config"
        (onSelected)="choose($event)"
        (onClearSelected)="choose(undefined)"
      ></c8y-asset-selector>
    }
  `
})
export class RootPickerComponent implements OnInit {
  /**
   * The tenant to browse.
   *
   * Set once. The parent destroys and rebuilds this component when the source
   * tenant changes, because the selector and its services cache what they have
   * fetched - and a cache filled from one tenant has no business being shown for
   * another.
   */
  @Input({ required: true }) tenant!: string;

  /** The object to migrate, or nothing once the selection is cleared. */
  @Output() readonly picked = new EventEmitter<ChosenRoot | undefined>();

  /**
   * Groups are selectable and devices are shown: a migration root is usually a
   * group, sometimes a single device, and there is no third case. Search is on
   * because a tenant with hundreds of devices cannot be browsed to.
   */
  readonly config = {
    // The selector translates config.label itself; this marks it for extraction.
    label: gettext('Choose what to migrate'),
    search: true,
    groupsSelectable: true,
    showChildDevices: true,
    showSelected: true,
    // Devices in no group are otherwise reachable only by searching for them,
    // and migrating a single device is a first-class reason to be here.
    showUnassignedDevices: true,
    modelMode: 'simple' as const,
    view: 'tree' as const
  };

  constructor(private readonly platform: TenantPlatformClient) {}

  ngOnInit(): void {
    // Before the selector's first fetch: it is a child in this template, so it
    // is created after this runs. Until this happens the client refuses to read
    // anything at all, which is what keeps a missed wiring loud.
    this.platform.for(this.tenant);
  }

  /**
   * What the selector emits, as what a migration needs.
   *
   * The reading of it is in selection.ts, where it is tested: the emitted shape
   * is an envelope rather than the selected object, and getting that wrong made
   * a working picker look broken.
   *
   * A cleared selection is passed on as nothing, so the dialogue forgets the
   * root instead of keeping one nobody can see any more.
   */
  choose(selection: AssetSelection | undefined): void {
    this.picked.emit(chosenRoot(selection));
  }
}
