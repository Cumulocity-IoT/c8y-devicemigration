import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';

/**
 * Directives a template uses have to be imported by the component using them.
 *
 * Angular enforces that for *elements* - an unknown tag is a build error - but
 * not for *attributes*: `c8yIcon` on an `<i>` without CoreModule imported is
 * simply inert. The template compiles, the type check passes, the template check
 * passes, and the button renders an empty box. That is exactly how a help button
 * shipped with no icon in it.
 *
 * So this walks every component and checks the pairing itself. It is a crude
 * check - text in a file, not a parse - and crude is the point: it costs nothing
 * and it catches the one failure that is otherwise silent all the way to a
 * browser.
 */

/** Attributes that only do something when their module is imported. */
const DIRECTIVES: { attribute: string; module: string }[] = [
  { attribute: 'c8yIcon', module: 'CoreModule' },
  { attribute: 'c8y-form-group', module: 'CoreModule' },
  { attribute: 'ngModel', module: 'FormsModule' },
  { attribute: 'dropdownToggle', module: 'BsDropdownModule' },
  { attribute: 'popover', module: 'PopoverModule' }
];

function sourceFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) {
      return sourceFiles(path);
    }
    return entry.name.endsWith('.component.ts') ? [path] : [];
  });
}

/**
 * What the component's decorator declares in `imports`.
 *
 * The decorator's array, not the file: an `import { CoreModule } from …` line
 * survives removing the module from the array, so a check over the whole file
 * passes on exactly the bug this is for. Found by breaking it on purpose.
 */
function declaredImports(source: string): string {
  const declared = /imports:\s*\[([\s\S]*?)\]/.exec(source);
  return declared ? declared[1] : '';
}

/** A component's template, inline or from templateUrl. */
function templateOf(path: string, source: string): string {
  const external = /templateUrl:\s*'([^']+)'/.exec(source);
  if (external) {
    return readFileSync(join(dirname(path), external[1]), 'utf8');
  }
  const inline = /template:\s*`([\s\S]*?)`\s*,?\n/.exec(source);
  return inline ? inline[1] : '';
}

/**
 * A bare `<select>` renders as the browser's own control.
 *
 * `@c8y/style` suppresses the native appearance and draws the platform's caret
 * through `.c8y-select-wrapper` - the select itself carries no rule that would
 * do it. So a select without the wrapper looks like nothing else on the screen,
 * and nothing in the build says so: it is valid markup that simply inherits
 * different styling.
 *
 * The second convention of this kind to be missed quietly, after the help
 * button. Both are cheap to check and invisible until a browser is open.
 */
describe('selects carry the platform wrapper', () => {
  it('wraps every select in c8y-select-wrapper', () => {
    const unwrapped: string[] = [];

    for (const path of sourceFiles(join(__dirname))) {
      const template = templateOf(path, readFileSync(path, 'utf8'));
      for (const match of template.matchAll(/<select\b/g)) {
        // The wrapper is the select's parent, so it opens shortly before it -
        // far enough back for a comment between the two, not so far that the
        // previous field's wrapper would count.
        const before = template.slice(Math.max(0, match.index - 600), match.index);
        if (!before.includes('c8y-select-wrapper')) {
          unwrapped.push(path);
        }
      }
    }

    expect(unwrapped).toEqual([]);
  });
});

describe('every component imports what its template uses', () => {
  const components = sourceFiles(join(__dirname));

  it('finds the components to check', () => {
    expect(components.length).toBeGreaterThan(5);
  });

  for (const { attribute, module } of DIRECTIVES) {
    it(`imports ${module} wherever ${attribute} is used`, () => {
      const missing = components.filter(path => {
        const source = readFileSync(path, 'utf8');
        return (
          templateOf(path, source).includes(attribute) &&
          !declaredImports(source).includes(module)
        );
      });

      expect(missing).toEqual([]);
    });
  }
});
