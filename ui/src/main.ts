/**
 * A plugin is loaded by its host application, which provides the page it lives
 * in. This exists so the build has an entry point to compile the exported module
 * from; nothing here bootstraps an application of its own.
 */
// The translation catalogues. Imported for their side effect only: the build
// turns every .po file in the module graph into a <language>.json asset, and the
// platform fetches those from this plugin's own path. See src/locales.
import './locales/de.po';
import './locales/en.po';
import './locales/en-US.po';

export { DeviceMigrationModule } from './migration.module';
