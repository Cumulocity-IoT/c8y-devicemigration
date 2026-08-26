import { NgModule } from '@angular/core';
import { CoreModule, hookNavigator, hookRoute, NavigatorNode } from '@c8y/ngx-components';

import { HistoryComponent } from './history/history.component';
import { WizardComponent } from './wizard/wizard.component';

/**
 * The plugin, as an application loads it.
 *
 * One module, exported from package.json, so it can be added to Administration -
 * where migrating tenants belongs, and where the only people the microservice
 * accepts are already working - or to any other application without changes.
 */
@NgModule({
  imports: [CoreModule, HistoryComponent, WizardComponent],
  providers: [
    hookRoute([
      { path: 'device-migration', redirectTo: 'device-migration/history', pathMatch: 'full' },
      { path: 'device-migration/history', component: HistoryComponent },
      { path: 'device-migration/new', component: WizardComponent }
    ]),
    hookNavigator(
      new NavigatorNode({
        label: 'Device migration',
        icon: 'exchange',
        path: '/device-migration',
        priority: 100,
        // Hidden from anybody who could not use it anyway: the microservice
        // refuses every call without this role, and a menu entry that only ever
        // leads to a refusal is worse than no entry.
        permissions: ['ROLE_DEVICE_MIGRATION_ADMIN']
      })
    )
  ]
})
export class DeviceMigrationModule {}
