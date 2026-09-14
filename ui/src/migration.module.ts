import { NgModule } from '@angular/core';
import { CoreModule, gettext, hookNavigator, hookRoute, NavigatorNode } from '@c8y/ngx-components';

import { HistoryComponent } from './history/history.component';
import { DeviceMigrationNavigatorIconComponent } from './icon/navigator-icon.component';
import { RegisterCertificateComponent } from './trust/register-certificate.component';

/**
 * The plugin, as an application loads it.
 *
 * One module, exported from package.json, so it can be added to Administration -
 * where migrating tenants belongs, and where the only people the microservice
 * accepts are already working - or to any other application without changes.
 */
@NgModule({
  imports: [CoreModule, HistoryComponent, RegisterCertificateComponent],
  providers: [
    hookRoute([
      { path: 'device-migration', component: HistoryComponent },
      // Target-side: trusting a migration certificate. Its own route so it can be
      // reached in a tenant that has the plugin but not the microservice, by an
      // admin who is not an owner-tenant admin - which is exactly who registers it.
      { path: 'device-migration/trust-certificate', component: RegisterCertificateComponent }
    ]),
    hookNavigator([
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
        // Hidden from anybody who could not use it anyway: the microservice
        // refuses every call without this role, and a menu entry that only ever
        // leads to a refusal is worse than no entry.
        permissions: ['ROLE_DEVICE_MIGRATION_ADMIN']
      }),
      new NavigatorNode({
        // Not gated on the migration role: the person trusting a certificate is
        // the target tenant's admin, who does not hold it. The platform enforces
        // the certificate-management permission on the call itself.
        label: gettext('Trust a migration certificate'),
        icon: 'certificate',
        path: '/device-migration/trust-certificate',
        priority: 90
      })
    ])
  ]
})
export class DeviceMigrationModule {}
