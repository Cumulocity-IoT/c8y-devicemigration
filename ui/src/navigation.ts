import { Injectable } from '@angular/core';
import { gettext, NavigatorNode, type NavigatorNodeFactory } from '@c8y/ngx-components';

import { AvailabilityService } from './api/availability.service';
import { DeviceMigrationNavigatorIconComponent } from './icon/navigator-icon.component';

/**
 * One entry, always called the same thing.
 *
 * Not two - a migration entry and a "trust a migration certificate" entry -
 * because that would put the plugin's internals in the navigator: the second
 * would exist only because a target tenant has no microservice, which is not
 * something a person browsing a menu should have to know. What they want is the
 * feature, and the feature is called Device migration wherever it appears.
 *
 * So the node is single and fixed, and *what it opens* is decided by the tenant.
 * See DeviceMigrationComponent.
 */
@Injectable({ providedIn: 'root' })
export class DeviceMigrationNavigationFactory implements NavigatorNodeFactory {
  constructor(private readonly availability: AvailabilityService) {}

  async get(): Promise<NavigatorNode[]> {
    // Which permission gates the entry depends on what it would open, and that
    // is the whole reason this is a factory rather than a literal node.
    //
    // Where the microservice answers, the entry leads to a screen every call of
    // which the service refuses without the migration role, so an entry without
    // it would only ever lead to a refusal. Where it does not, the entry leads to
    // trusting a certificate - done by the target tenant's administrator, who has
    // no reason to hold a migration role, and whose permission to do it the
    // platform enforces on the call itself.
    const reachable = await this.availability.reachable();

    return [
      new NavigatorNode({
        // The shell translates a navigator label; this is the marker for it.
        label: gettext('Device migration'),
        // The same icon wherever this feature appears - here, the list's
        // introduction and its empty state. One picture for one thing: two
        // icons for the same feature reads as two features.
        //
        // A component rather than `icon`, which takes a name from the platform's
        // own set and cannot render this one. It is named `c8y-navigator-icon`
        // on purpose: the navigator lays a node's icon out with a type selector,
        // so the host's tag name is what the spacing hangs on - see the
        // component.
        iconComponent: DeviceMigrationNavigatorIconComponent,
        path: '/device-migration',
        priority: 100,
        permissions: reachable ? ['ROLE_DEVICE_MIGRATION_ADMIN'] : []
      })
    ];
  }
}
