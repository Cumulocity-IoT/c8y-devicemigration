import { NgModule } from '@angular/core';
import { CoreModule, hookNavigator, hookRoute } from '@c8y/ngx-components';

import { DeviceMigrationComponent } from './device-migration.component';
import { DeviceMigrationNavigationFactory } from './navigation';

/**
 * The plugin, as an application loads it.
 *
 * One module, exported from package.json, so it can be added to Administration -
 * where migrating tenants belongs, and where the only people the microservice
 * accepts are already working - or to any other application without changes.
 *
 * **One route and one navigator entry**, both called Device migration. The
 * plugin has two screens, but which of them a tenant gets is a property of that
 * tenant rather than a choice a reader should be asked to make: where the
 * microservice is subscribed this is the migration tool, and where it is not it
 * is the one thing such a tenant can usefully do - trust a migration
 * certificate, which is a plain platform call. Two separate navigator entries
 * would put that distinction in front of people who have no way to know what it
 * means.
 */
@NgModule({
  imports: [CoreModule, DeviceMigrationComponent],
  providers: [
    hookRoute([{ path: 'device-migration', component: DeviceMigrationComponent }]),
    hookNavigator(DeviceMigrationNavigationFactory)
  ]
})
export class DeviceMigrationModule {}
