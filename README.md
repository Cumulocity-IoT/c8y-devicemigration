# c8y-devicemigration
**EXPERIMENTAL** go-c8y-cli extension for device migration

Exports a device hierarchy from one Cumulocity tenant and recreates it in another.

## Installation

The extension can be installed using the following command.

```sh
c8y extension install Cumulocity-IoT/c8y-devicemigration
```

## Usage examples

Export a device and everything below it into a directory:

```sh
c8y devicemigration export --rootId 12345 --output-directory ./my-export
```

Export into a single compressed archive instead:

```sh
c8y devicemigration export --rootId 12345 --output-compressed ./my-export.tgz
```

By default only the `asset` and `device` relationships are followed. Use
`--childTypes` to migrate additions as well, or to narrow the export further:

```sh
c8y devicemigration export --rootId 12345 --output-directory ./my-export \
    --childTypes asset,device,addition
```

Or export the root device on its own, following nothing:

```sh
c8y devicemigration export --rootId 12345 --output-directory ./my-export --no-children
```

Activate the target tenant, then recreate the hierarchy there. `import` always
uses the currently active session, so the session has to be switched before it
runs rather than with a `--session` flag on the command itself:

```sh
eval "$(c8y sessions set --sessionFilter target-tenant)"
c8y devicemigration import --input-directory ./my-export
```

Importing an archive works the same way, and the mapping file can be redirected:

```sh
c8y devicemigration import --input-compressed ./my-export.tgz --mapping ./ids.json
```

Each managed object is created with a POST, and go-c8y-cli asks for confirmation
on those for a production session. Pass `--force` to accept everything up front,
which is what you want for anything but a handful of devices:

```sh
c8y devicemigration import --input-directory ./my-export --force
```

`import` reports every object it touches, by name, with the IDs in brackets:

```
Imported Plant Berlin (1 -> 1001)
Imported Pump A (2 -> 1002)
Assigned Pump A (1002) as device child of Plant Berlin (1001)
```

The create lines show the old ID and the ID the target tenant assigned; the
assign lines show the new IDs, since that is what the relationship was built
from. Managed objects without a name are reported as `<unnamed>`.

## What gets migrated

`export` walks the child relationships from the given root and writes, per
device, the raw managed object as `exported_device_<id>.json` plus one
`<id>_child_<childType>.json` index per relationship type. A device referenced
by several parents is exported once, and cycles in the hierarchy terminate.

Which relationships are followed is controlled by `--childTypes`, a comma
separated list of `asset`, `device` and `addition`. It defaults to
`asset,device`, so additions are skipped unless they are asked for. This applies
to the whole traversal: a device that is only reachable through an addition is
not exported under the default.

`--no-children` turns the traversal off completely and exports just the root
managed object, with no relationship index files. It cannot be combined with
`--childTypes`.

`import` recreates every exported managed object in the currently active session
and then rebuilds the relationships using the IDs assigned by the target tenant.
It restores whatever the export contains and has no child type option of its
own, so the selection is made once, at export time.
Because those IDs are new, the old-to-new mapping is written to
`id_mapping.json` (override with `--mapping`) so the migration can be traced or
scripted against afterwards.

Server managed properties are dropped before a managed object is recreated:
`id`, `self`, `lastUpdated`, `creationTime`, `owner`, the parent and child
reference collections, and the computed `c8y_Availability`,
`c8y_ActiveAlarmsStatus` and `c8y_Connection` fragments. Everything else,
including custom fragments, is sent as exported.

## Limitations

Only managed objects and their parent/child relationships are migrated. In
particular the following are **not** covered:

- external IDs / identity bindings
- measurements, events, alarms and operations
- binaries attached to devices
- device credentials, so migrated devices do not reconnect on their own
- applications, tenant options and user or role assignments

Existing objects in the target tenant are never matched or updated: every import
creates new managed objects, so importing the same export twice produces two
copies.

## Development

The test suite runs entirely against a fake `c8y` on `PATH`, so it never touches
a tenant and needs no session.

```sh
make check     # what CI runs: lint, parser freshness, tests
make test      # tests only (bootstraps bats into tests/vendor on first run)
make lint      # shellcheck, skipped with a warning if not installed
```

`tests/fixtures/hierarchy.json` describes the fake tenant. It is shaped
deliberately: three parents have two children of the same type, because a parent
with a single child cannot expose the class of bug where `c8y` consumes the
stdin pipe driving a `while read` loop and only the first child is processed.
It also contains a cycle, an object with two parents, an addition-only branch,
an unnamed object, and names carrying quotes, brackets, tabs and non-ASCII.

Both commands parse arguments with a generated docopt parser held between the
`# docopt parser below/above` markers. After editing a `DOC` string, regenerate
it or every invocation will exit 70:

```sh
docopt.sh commands/export      # pipx install docopt-sh==2.0.3
```

### What the tests cannot cover

The fake encodes assumptions about real `c8y` behaviour, listed at the top of
`tests/fake/c8y`. Two are not verified against a live tenant: that
`inventory create --select id` emits `{"id":"..."}`, and that a plain-JSON file
passed to `--template` is used as the request body verbatim. Before trusting a
real migration, smoke it manually:

1. `c8y devicemigration export --rootId <id> --output-directory ./smoke`
2. Check one `exported_device_*.json` looks like the managed object, and that
   each `*_child_*.json` is a JSON array holding every child.
3. Switch to a throwaway tenant and import with `--force`.
4. Confirm the reported link count matches the number of references in the
   export, and spot-check the hierarchy in the UI.

## Licensing

This project is licensed under the Apache 2.0 license - see <https://www.apache.org/licenses/LICENSE-2.0>

______________________

These tools are provided as-is and without warranty or support. They do not constitute part of the Cumulocity product suite. Users are free to use, fork and modify them, subject to the license agreement. While Cumulocity GmbH welcomes contributions, we cannot guarantee to include every contribution in the master project.

______________________

You can find additional information in the [Cumulocity Community](https://community.cumulocity.com/).
