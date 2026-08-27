import { readdirSync, readFileSync, statSync } from 'fs';
import { join } from 'path';

/**
 * Every string this plugin can show, read out of its own source.
 *
 * The same two markers the platform's own tooling looks for: `| translate` in a
 * template, and `gettext(...)` or `message(...)` in TypeScript. Reading the
 * source rather than keeping a list beside it is the point - a list would drift,
 * and the drift is exactly what the tests using this are for.
 *
 * Not part of the running plugin: it reads files, so it only ever runs in a test.
 */
export function translatable(root = join(__dirname, '..')): string[] {
  const found = new Set<string>();

  for (const file of sources(root)) {
    const text = readFileSync(file, 'utf8');

    // '...' | translate, in a template file or in a template inside a .ts file -
    // a component with an inline template is still a template, and reading only
    // .html files quietly lost every string in one.
    for (const match of text.matchAll(/(['"])((?:(?!\1).)*?)\1\s*\|\s*translate/gs)) {
      found.add(match[2]);
    }

    if (file.endsWith('.ts')) {
      for (const match of text.matchAll(/(?:gettext|message)\(\s*(['"])((?:(?!\1).)*?)\1/gs)) {
        found.add(match[2]);
      }
    }
  }
  return [...found].sort();
}

/** The files worth reading: the plugin's own templates and code, not its tests. */
function sources(directory: string): string[] {
  const files: string[] = [];

  for (const entry of readdirSync(directory)) {
    const path = join(directory, entry);
    if (statSync(path).isDirectory()) {
      files.push(...sources(path));
      continue;
    }
    if (entry.endsWith('.spec.ts') || entry.endsWith('.d.ts')) {
      continue;
    }
    // i18n.ts holds the marker itself, whose own argument is a parameter name.
    if (entry === 'i18n.ts' || entry === 'extract.ts') {
      continue;
    }
    if (entry.endsWith('.html') || entry.endsWith('.ts')) {
      files.push(path);
    }
  }
  return files;
}
