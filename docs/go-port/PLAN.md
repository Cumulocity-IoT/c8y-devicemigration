# A Go port of the migration commands, on go-c8y v2

## Context

The four commands in `commands/` are bash scripts that shell out to `c8y` for
every single operation. That subprocess boundary has been the source of nearly
every bug this tool has had: `c8y` draining the stdin pipe that drove a
`while read` loop so only the first child of each parent was assigned; ambient
`C8Y_*` variables silently winning over `--session`; `sessions get` reporting the
active session and ignoring `--session` entirely; piped records being inlined as
jsonnet and failing to lex; `jq`'s `@tsv` doubling backslashes and corrupting
records on their way to the API. None of those are logic errors — they are all
consequences of talking to a CLI instead of an API.

This builds a second, standalone tool on the **go-c8y v2** library that mirrors
the existing behaviour 1:1, so the two can be compared and the Go one can
eventually replace the extension. A typed client with two explicit tenants
removes that entire class of failure by construction: no subprocess, no ambient
environment, no shell quoting, no pipes.

Decisions already taken:

- **Byte-identical**, enforced by a differential test that runs both
  implementations against the same fixture and diffs artefacts, stderr and exit
  codes. Drift fails the build.
- **V1 = session loading + pre-flight + `export` metadata.** A vertical slice
  proving session files → client → traversal → identical artefacts, with no
  writes to any tenant.
- **Decrypt go-c8y-cli session files** so encrypted files work as they do today.
- **cobra** for the CLI.
- The bash extension stays exactly as it is. Nothing in `commands/` changes.

**First implementation step: commit this plan and the behaviour contract into the
repo** at `docs/go-port/PLAN.md` and `docs/go-port/BEHAVIOUR-CONTRACT.md`, so both
of us can refer back to them. The contract is the long-form inventory of flags,
messages, artefact formats and invariants gathered during planning; the plan is
this file.

## What was established during planning

**The library.** Module `github.com/reubenmiller/go-c8y/v2`, pinned at commit
`5174b8382110b69bcceba29e6f2698efe1c03dc7`, Go 1.25. The v2 surface lives under
`pkg/c8y/api/`. Everything needed exists:

| Need | Library |
| --- | --- |
| client | `api.NewClient(api.ClientOptions{BaseURL, Auth: authentication.AuthOptions{...}})` |
| managed objects | `pkg/c8y/api/inventory/managedobjects` — `Get`, `List`, `ListAll`, `Create`, `Delete`, `Count` |
| relationships | `.../managedobjects/{childassets,childdevices,childadditions}` |
| external identities | `pkg/c8y/api/identity` |
| data | `pkg/c8y/api/{measurements,events,alarms}`; `Measurements.CreateList` is the measurement collection that mirrors `measurements createBulk` |
| processing mode | `pkg/c8y/api/context/processing_mode.go`, `pkg/c8y/api/contexthelpers` |
| results | `op.Result[T]` with `.Data`, `.Err`, `.Status`, `.HTTPStatus`, `Unwrap()`; `api.ErrHasStatus(err, 404)` |
| paging | `ListAll` returning an iterator with `.Items()` / `.Err()` |

`Count(ctx, ListOptions) op.Result[int64]` replaces the `--pageSize 1
--withTotalElements` count queries the limit pre-check uses today.

**Session files are ours to handle.** The library has no session, config,
encryption or keyring support — nothing in the tree matches any of those. So the
Go tool reads go-c8y-cli session files itself. `host` and `tenant` are plaintext;
`password` and `token` are encrypted with AES-256-GCM, key
`scrypt.Key(passphrase, salt, 16384, 8, 1, 32)`, value laid out as
`[nonce][ciphertext][salt]`, hex-encoded behind a marker prefix. That is ~40
lines with `golang.org/x/crypto/scrypt`.

**Byte-identical is achievable.** Verified against the committed fixtures:

- `*_child_*.json` — jq pretty-print, two-space indent, trailing newline:
  `[\n  "7874377868",\n  "4074377869"\n]\n`. Go's
  `json.MarshalIndent(ids, "", "  ")` plus a `\n` matches exactly.
- `*_identities.json` — same style, keys in the order `type` then `externalId`
  (note: the c8y `--select` order is the reverse). A struct with fields in that
  order reproduces it.
- `exported_device_*.json` — the raw response body, written verbatim by
  `--outputFileRaw`. Go writes the response bytes unchanged, so nothing can
  drift.
- `data/<id>/*.jsonl` — one compact record per line, as the API returned it.

**Number and null preservation.** jq 1.7.1 preserves `600.0` and explicit
`null`s, and `tests/fixtures/export-9object/` contains both. Go's
`encoding/json` renders `float64(600)` as `600`, which would silently rewrite a
customer's data. Bodies must therefore never round-trip through
`map[string]any`. Use `tidwall/gjson`/`sjson` — already transitive dependencies
of go-c8y v2 — so `del(...)` becomes `sjson.DeleteBytes` on the raw document and
every untouched byte stays untouched. This does not bite V1 (export writes
responses verbatim) but is load-bearing from the import increment onwards.

## Layout

Root module, so the Go tool and the bash extension live side by side:

```
go.mod                                  github.com/Cumulocity-IoT/c8y-devicemigration
cmd/c8y-devicemigration/main.go
internal/cli/                           cobra wiring, one file per command
internal/session/                       resolve, read, normalise host, decrypt
internal/c8yclient/                     session -> go-c8y v2 client
internal/export/                        traversal, artefacts, limits
internal/jsonraw/                       gjson/sjson helpers (later increments)
internal/testfixture/                   HTTP server that replays the fixtures
docs/go-port/PLAN.md
docs/go-port/BEHAVIOUR-CONTRACT.md
```

`.gitignore` gains `/bin/`. Existing entries already cover `*.test`, `*.out` and
force-include `tests/fixtures/**`, so Go tests can read the same fixtures.

## V1, in order

1. **Scaffolding.** `go.mod` with the library pinned to that exact commit,
   `cmd/c8y-devicemigration`, cobra root with `--session` as a persistent flag.
2. **`internal/session`.** Resolution mirrors `commands/migrate:220-262`: an
   existing file path wins; otherwise glob against the session home; zero
   matches and more-than-one matches are distinct, verbatim errors. Session home
   is `$C8Y_SESSION_HOME` or `~/.cumulocity` (see Deviations). `normalise_host`
   mirrors `migrate:269-275` — strip scheme, strip one trailing slash, lowercase.
   Decryption in its own file so it can be tested in isolation.
3. **`internal/c8yclient`.** Build a client from a resolved session. Verify
   first that basic auth works without an explicit `LoginWithOptions` call — if
   it does not, that changes this step and nothing else.
4. **Pre-flight.** Resolve both sessions, refuse same host+tenant, print the
   four lines `migrate` prints, then probe credentials with
   `Tenants.GetCurrent`-equivalent. Exposed as a `preflight` subcommand — the one
   deliberate addition to the CLI surface, because otherwise there is no way to
   check that decryption and auth work without touching a tenant.
5. **`export` (metadata).** Flags exactly as `commands/export:7-8`, minus the
   data flags, which arrive in increment 4. Traversal mirrors
   `export_device_recursive`: write object, identities and all child index files
   for all requested child types, emit the progress line, then descend. Two
   space-padded visited sets, one for the limits pre-flight and one for the real
   pass, so cycles terminate and a two-parent node is fetched once. Limits via
   `Count`, all violations collected before any is reported, the four-column
   table and the `--dateFrom` suggestion reproduced from `export:441-517`.
   Partial-export cleanup deletes recorded artefacts in reverse order.

## Status

V1 is implemented: `preflight`, `export` of the hierarchy and its external
identities, session loading with decryption, the differential test, and the
build and CI wiring.

Added after the plan was approved, because it is how the tool is actually used:
**`--session` is optional on `export`.** Without it the active go-c8y-cli session
is used, so `export` works straight after
`eval "$(c8y sessions set --sessionFilter my-tenant)"`. Precedence is
`--session` first, then the ambient `C8Y_*` variables, then the file
`C8Y_SESSION` names. The environment is preferred over that file on purpose: an
activated session exports its credentials already decrypted, so that route needs
no `C8Y_PASSPHRASE`. With `--session` the file wins and the ambient variables are
ignored, which is what `commands/export` achieves by clearing them.

Nothing is printed about which session was chosen, because `commands/export`
prints nothing either and the two outputs are compared byte for byte. `preflight`
is where you go to see what a session resolves to.

Increment 2 is implemented: `import` of objects, external identities and
relationships, with the mapping written after every created object, the marker
fragment, and the same-tenant refusal. `internal/jsonraw` edits managed object
bodies as raw text with sjson, so numbers and nulls survive.

Two things came out of building it. `Identity.Create` in the library calls
`IgnoreConflict()`, so a duplicate external id arrives as a success and had to be
detected through `StatusDuplicate` — see the contract. And the same-tenant guard
found that `commands/import` has the same gap, so it should be added there too.

Increment 3 is implemented: `revert` from a mapping file or by `--runId`, with
the plan printed before anything is touched, the outcome established by
re-reading every object, and `--force` to skip the confirmation.

Two deliberate differences from the bash version, both recorded in the contract:
it asks **once** about a plan it has just printed in full, rather than relying on
the c8y binary's per-request dialog and its "Yes to All"; and the deletes are
separate requests, because batching them in bash was only ever a way to make
that dialog answerable once.

## Later increments

Each is independently shippable and each extends the differential test.

| # | Scope |
| --- | --- |
| 4 | Device data both ways: measurements/events/alarms, attachments, processing mode, batch sizes, and the rule that a failed record makes the import exit non-zero. |
| 5 | `migrate`: chained, then `--incremental`, then `--resume` with the journal. |

## `import` must refuse the tenant the export came from

A gap in **both** implementations today. `migrate` refuses two sessions that
resolve to the same tenant, but nothing stops someone running `export` and then
`import` by hand and pointing the import back at the source. The result is the
whole hierarchy duplicated inside its own tenant with fresh ids — recoverable
through `revert`, but only if you notice.

The source tenant is knowable from the artefacts. Every
`exported_device_<id>.json` carries `self`, so the source **host** is there for
the reading. The source tenant *id* is not, which matters because several tenants
can share one host and refusing those would block legitimate work.

So the check is in two steps, and only the first is free:

1. Read `self` from any one exported object, normalise it the way
   `normalise_host` does, and compare it with the target session's host.
   Different host, different tenant — proceed.
2. Same host: confirm with a single request. `GET` one exported object's
   **source** id in the target tenant and compare the `self` it returns with the
   `self` in the export. Ids are tenant-scoped, so a match means this really is
   the tenant the export came from.

**Default is to stop**, with `--allowSameTenant` to override — someone
deliberately duplicating a hierarchy within one tenant is a real if unusual
thing to want, and the flag makes it a decision rather than an accident. The
refusal names both the host and the file it compared, in the same shape as
`migrate`'s existing same-tenant message, and it happens **before** the first
object is created.

Worth doing in `commands/import` at the same time, so the two implementations
stay comparable and the bash tool stops having the gap. That means the message
and the flag name are part of the contract and the differential test covers
them.

## Testing

Three layers, and the third is the point of the exercise.

**Unit tests** for the pure logic: session resolution and its ambiguity refusal,
host normalisation across mismatched schemes, decryption, the limits table and
the window suggestion, artefact formatting.

**Fixture-driven HTTP tests.** `internal/testfixture` serves
`tests/fixtures/hierarchy.json` over `httptest` — the same file the bats suite
uses — so both implementations are exercised against the same synthetic tenant,
including its cycle, its two-parent node, its unnamed object, its awkward names
and alarm `a3`'s escaped-JSON text.

**The differential test**, `internal/export/differential_test.go`:

1. Generate the expected API responses by invoking `tests/fake/c8y` itself, so
   the Go server replays exactly the bytes the bash side sees. Reusing the fake
   as the fixture generator is what removes any chance of the two drifting.
2. Run `commands/export` against the fake into directory A.
3. Run the Go export against the httptest server into directory B.
4. Assert A and B are identical file-for-file and byte-for-byte, that stderr
   matches line-for-line, and that exit codes match.

Skips when `bash` or `jq` is unavailable so it stays hermetic. Wired into `make
check` and CI.

**What cannot be shared.** The 254 bats tests assert against
`$FAKE_C8Y_REC/calls.log` — the record of `c8y` invocations and their flags.
A library-based tool makes no such calls, so every assertion about
`--nullInput`, `--batchSize 50`, `--silentStatusCodes` or session-per-call is
meaningless for the Go tool and must not be faked to pass. Those tests keep
guarding the bash implementation. The Go tool inherits the assertions that are
about observable behaviour: artefact layout, `id_mapping.json`, the journal,
stderr text, exit codes, and the resulting state of the tenant.

## Deviations, deliberate and stated

- **Session home** comes from `$C8Y_SESSION_HOME` or `~/.cumulocity` rather than
  `c8y settings list`, because the Go tool must not depend on the `c8y` binary.
  If someone has overridden `session.home` in c8y's settings, the paths differ.
  A `--sessionHome` flag covers it.
- **Exit code 70** (docopt digest mismatch) has no Go equivalent.
- **`preflight`** is a new subcommand, as above.
- The behaviour contract records seven places where the bash code and its own
  documentation disagree — most notably that `export --dataOnly` still writes
  child index files and still recurses, despite its help text. **The Go port
  mirrors the code, not the help text**, and the bash help text gets corrected
  separately so the two stop disagreeing.

## Build and CI

`Makefile` gains `go-build`, `go-test`, `go-lint` (`gofmt -l` plus `go vet`) and
`diff-test`, and `check` gains all four. CI gains one `go` job using
`actions/setup-go` pinned to 1.25, running build, vet, gofmt and tests on
ubuntu and macOS. The three existing jobs are untouched.

## Verification

1. `make go-test` — unit and fixture tests green.
2. `make diff-test` — the bash and Go exports are byte-identical for: the
   default `--childTypes`, `--childTypes asset,device,addition`,
   `--no-children`, a limit violation against `hierarchy-wide.json`, and an
   unresolvable and an ambiguous session.
3. `make check` — everything above plus the untouched 254 bats tests.
4. Against a real tenant, with `C8Y_PASSPHRASE` exported:
   `./bin/c8y-devicemigration preflight --sourceSession <src> --targetSession <tgt>`
   prints both tenants and confirms decryption and auth work; then
   `./bin/c8y-devicemigration export --rootId <id> --output-directory ./smoke
   --session <src>` and diff that against the bash export of the same tree.
5. Mutation-check the new work as with everything else in this repo: break the
   visited set, the ambiguity refusal, the host normalisation and the limit
   check in turn, and confirm a named test fails each time.

## Risks

- **Basic auth may need an explicit login call.** Cheap to establish in step 3;
  it changes only that step.
- **The library is pinned to a commit, not a release**, and its API is new. A
  later bump may move method names. The pin is deliberate and `go.sum` locks it.
- **Byte-identical stderr is a strict contract.** It is also the only way to
  know the port is faithful, and the differential test makes any drift a build
  failure rather than a surprise in production.
