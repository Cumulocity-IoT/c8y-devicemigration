# Internals

Why the tooling is shaped the way it is, and how to work on it. Nothing here is
needed to *use* the tool — start from [the main README](../README.md) for that.

The companion document is
[behaviour-contract.md](behaviour-contract.md), which records
what the commands do, down to the trailing newline, and which of the library's
helpful defaults had to be undone to get there.

## Why it talks to the API directly

A migration tool could reach Cumulocity by shelling out to `c8y` for every
operation, and that subprocess boundary is where such a tool goes wrong:

- `c8y` draining the stdin pipe that drives a `while read` loop, so only the
  first child of each parent is assigned
- ambient `C8Y_*` variables quietly winning over `--session`
- `sessions get` ignoring `--session` altogether
- records failing to lex when inlined as jsonnet
- `jq`'s `@tsv` doubling backslashes and corrupting records in transit

None of those are logic errors. They are all consequences of talking to a CLI
instead of an API. The [command line tool](cli.md) uses the **go-c8y v2** library
instead: a typed client with two explicit tenants removes that whole class by
construction — no subprocess, no ambient environment, no shell quoting, no pipes.

## Why sessions are read from disk

The identity comes from reading the session files, not from asking c8y.
`c8y sessions get --session X` **does not report session X** — it reports
whichever session is active and ignores `--session`, so with a source session
loaded it answers "source" for both sides and the same-tenant check refuses a
good migration. `host` and `tenant` are plaintext in a session file even when
`password` and `token` are encrypted, so reading them needs no passphrase, no
network and no active session.

For the same reason the ambient `C8Y_*` variables are ignored whenever
`--session` is given. Activating a session exports `C8Y_HOST` and `C8Y_TENANT`,
and those would otherwise decide which tenant is talked to whatever `--session`
says. `C8Y_PASSPHRASE` is deliberately still read — without it an encrypted
session file cannot be decrypted at all.

The file each argument resolved to is what the export and import are given, so
the tenants named above are the tenants used. A glob is never resolved twice.

## One fragment per object

**One fragment per object, deliberately.** There is no second, constant
`c8y_DeviceMigration` fragment, so "what did this tool ever create in this
tenant?" is not an indexed query — `fragmentType` matches a fragment name
exactly, and the inventory query language wildcards fragment *values*, not
names. Answering it means either knowing the run ids, which the mapping files
and journals record, or a client-side `--filter` over the whole inventory. That
was weighed against stamping a second fragment onto every migrated object, and
the single run-scoped marker won.

## Refusing an import into the source tenant

`import` refuses to import back into the tenant the export came from: `migrate`
compares its two sessions, but `export` and `import` run by hand could otherwise
duplicate a hierarchy inside its own tenant. It looks up an exported object by
its **source** id: ids are tenant-scoped, so an id that resolves to an object with
the same name, type and creation time is the same object, and so the same tenant.
It does not compare hostnames, because `self` carries whatever host the platform
chose and a mismatch says nothing. `--allowSameTenant` overrides it.

## Bodies are written verbatim

Managed object bodies are written to disk **verbatim**, and a test asserts it
byte for byte. Go's `encoding/json` renders `float64(600)` as `600` and cannot
distinguish an absent field from an explicit `null`, and real managed objects
contain both. Re-encoding a body would silently rewrite a customer's data.

## Releasing

Three things are released:

| | |
| --- | --- |
| the command line tool | `c8y-devicemigration-<version>-<os>-<arch>.tar.gz`, and `.zip` for Windows, for darwin and linux on arm64 and amd64 plus 64-bit Windows. Each holds the binary, `LICENSE`, `README.md` and `docs/`, and `SHA256SUMS` covers them all |
| the microservice | `devicemigration-<version>.zip`, the manifest and image the platform is fed, and the same image in the registry at `ghcr.io/<owner>/c8y-devicemigration/microservice` |
| the user interface plugin | `devicemigration-ui-<version>.zip`, installed under **Administration → Ecosystem → Extensions** |

All three carry the same version. Both binaries report it —
`c8y-devicemigration --version`, and the microservice logs it at startup — and
it is stamped at link time from `internal/version`. The plugin carries only the
`X.Y.Z` core of it, because the platform refuses a pre-release suffix on a UI
plugin.

### Versions

[VERSION](../VERSION) holds the version being developed — the one the next
release will carry — and it is the only place a version is written down. What a
build calls itself is derived from that file and from git:

| the build | what it says |
| --- | --- |
| an exact `v` tag on a clean tree | `0.2.0` — a release |
| anything else | `0.2.0-SNAPSHOT` |

```sh
make version                    # what this build would call itself
make dist                       # the archives, into dist/
make service-package            # the microservice zip, into build/
make dist VERSION=1.2.0         # what a release of 1.2.0 would contain
```

**Snapshots are the default.** Taking the *nearest* tag instead would make a
working tree with a fortnight of unreleased work in it build an image labelled
exactly like the released version — and once that is deployed, nothing about the
running service says whether it is current. `0.2.0-SNAPSHOT` cannot be mistaken
for `0.2.0`.

**One snapshot version rather than one per commit**, so every upload replaces the
same version instead of leaving a list behind. The cost is that two snapshot
builds cannot be told apart by their version; when that question comes up, the
answers are to release, or to pass `VERSION` explicitly.

`make check` runs `version-check`, which refuses a `VERSION` file that is not a
plain `X.Y.Z` — the suffix is the build's to add — and, on a release build,
refuses a tag that disagrees with the file.

### Tags, and releasing

Tags still decide what a release is; `VERSION` decides what it is allowed to be
called. Four steps:

```sh
$EDITOR VERSION                      # 1. the version being released
git commit -am "release 1.2.0"       # 2. it has to be in a commit to be tagged
make tag                             # 3. creates v1.2.0 on that commit
git push origin v1.2.0               # 4. builds and publishes it
```

`make tag` refuses a dirty tree — a tag names a commit, and uncommitted work is
not in one — refuses a version that is already tagged, and refuses anything
`version-check` would refuse. It does not push: that is the irreversible half,
and it was never the part that needed automating. Tagging by hand
(`git tag v1.2.0`) works exactly as before; the target is a shortcut, not a
gate.

Afterwards, bump `VERSION` again so builds go back to calling themselves
snapshots of something newer.

The tag and the file must agree on their `X.Y.Z` core. A tag that disagrees is
refused by the release workflow in its first job, before anything is built —
publishing a version this repository never said it was working towards is worse
than a failed release. A pre-release of a declared version is fine: `v0.3.0-rc1`
while the file says `0.3.0`.

That runs [.github/workflows/release.yml](../.github/workflows/release.yml),
which resolves the version once, refuses it unless it is SemVer — the platform
rejects a manifest that is not — runs the Go checks before building anything,
then produces the archives, the microservice zip, the plugin zip and a
multi-architecture image, and opens a **draft** release with all of it attached.
The draft is deliberate: the notes are generated and somebody should read them
before this is what people download. Publish with
`gh release edit v1.2.0 --draft=false`.

A tag with a pre-release identifier (`v1.2.0-rc.1`) is marked as a pre-release
and does not move the image's `latest` tag.

To find out whether a release would work without making one, run the workflow
manually from the Actions tab with a version: everything is built and nothing is
published.

## Development

The test suite runs entirely against an HTTP server in the test process
(`internal/testfixture`), so it never touches a tenant and needs no session.

```sh
make check              # everything below, plus the UI checks
make go-build           # build bin/c8y-devicemigration
make go-test            # Go tests
make go-lint            # gofmt and go vet
make install            # -> $(INSTALL_DIR), default ~/.local/bin
make install-completion  # the binary plus the zsh completion script
make lint               # shellcheck and actionlint, each skipped with a warning if not installed
make docs               # regenerate the command reference in docs/cli.md
```

The [command reference](cli.md#command-reference) in `docs/cli.md` is generated
from the binary's own `--help` by `tools/gen-command-docs.sh`, between the
`<!-- BEGIN COMMAND REFERENCE -->` markers. Run `make docs` after changing a
command or an option; `make check` and CI fail if it was not.

`tests/fixtures/hierarchy.json` describes the tenant that server pretends to be.
It is shaped deliberately: three parents have two children of the same type,
because a parent with a single child cannot expose the class of bug where only
the first child of each parent is processed. It also contains a cycle, an object
with two parents, an addition-only branch, an unnamed object, and names carrying
quotes, brackets, tabs and non-ASCII.

### What the tests cannot cover

The fixture server encodes assumptions about how the platform behaves. When the
platform turns out to behave differently, the fixture has to change with it,
since a fixture that quietly drifts is worse than no test at all.

The suite still cannot see a real tenant, so after changing anything that talks
to the API, smoke it manually:

1. `c8y-devicemigration export --rootId <id> --output-directory ./smoke --session <source>`
2. Check one `exported_device_*.json` looks like the managed object, and that
   each `*_child_*.json` is a JSON array holding every child.
3. Import into a throwaway tenant:
   `c8y-devicemigration import --input-directory ./smoke --session <throwaway>`
4. Confirm the reported link count matches the number of references in the
   export, and spot-check the hierarchy in the UI.
5. Check the `Added external identity ...` lines appear for any device that had
   external IDs, and that those IDs resolve in the target tenant.
