# The user interface

An Angular plugin for Cumulocity, so a migration can be performed by somebody who
has not read any of the rest of this repository.

It drives [the microservice](microservice.md), which means the microservice has to
be deployed and subscribed first — the UI holds no credentials of its own and
talks to no tenant directly.

> **Status: increments B, C and D.** The plugin, the generated API client, the
> history view with revert, the four-step wizard and the live progress screen.
> None of it has been opened in a browser against a real tenant yet. The design
> it was built to is in the
> [UI design](https://claude.ai/code/artifact/42204d1b-d968-4030-b1e9-aeca0cb8c8cd).

## Installing it

```sh
make ui-package        # -> build/devicemigration-ui-<version>.zip
```

Install that zip under **Administration → Ecosystem → Extensions**, then add it to
an application. Administration is where this is documented and tested, because
migrating tenants is an operator task and the microservice only accepts
parent-tenant administrators anyway; nothing stops it being added to Device
Management or a custom application instead.

Then grant `ROLE_DEVICE_MIGRATION_ADMIN` to whoever should use it. The navigator
entry is hidden without it, because every call the UI makes would be refused.

## Who can use it, and where

Only in the tenant the microservice is deployed on. A session belongs to one
tenant, and the microservice refuses anybody who is not a user of its own — so
opening the plugin in a subtenant produces a refusal on every screen, naming the
tenant that may use it. That is the microservice's rule, not the UI's, and the UI
shows its words rather than paraphrasing them.

## What it is built on

| | |
| --- | --- |
| `src/api/types.ts` | **generated** from [internal/service/openapi.yaml](../internal/service/openapi.yaml). Never edited by hand |
| `src/api/client.ts` | the API as plain TypeScript over a fetch-like callable |
| `src/api/migration.service.ts` | three lines, handing the client Cumulocity's own `FetchClient` |
| `src/progress.ts` | turning a job into something a screen can show |
| `src/wizard/steps.ts` | which step is reachable, and why not |
| `src/wizard/flow.ts` | the wizard's rules: what invalidates what, and what is sent |
| `src/wizard/wizard.component.*` | the four steps and the progress screen |
| `src/history/` | past migrations and reverts, with revert |

Two things are deliberate about that shape.

**The types are generated, and staleness fails the build.** `make check` runs
`ui-types-freshness`, which regenerates from the document and refuses to pass if
the committed file differs — the same treatment the docopt parsers and the
command reference already get. It earned its place immediately: with `default:`
values in the document, the generator made `childTypes` a *required* field, so
calling the API the way the API actually allows would not have compiled.

**The logic is not in Angular.** Everything worth testing — which failure means
what, whether a progress line can be believed, which step is reachable, what
invalidates an estimate — is a pure function or a plain class, so it is tested
without a browser or a dependency injector. The Angular pieces are wiring.

## The four steps

1. **Introduction** — what this does and does not do, before anything is chosen.
2. **Source** — the tenant, then the root by search or by expanding a group, then
   what to include. The tenant the microservice runs in is listed but not
   selectable as a source: it is where job records live.
3. **Target** — the tenant, then a pre-flight that proves both service users
   without reading either hierarchy.
4. **Review** — the estimate: counts against the engine's own limits, with the
   busiest device shown per record kind, because the limits are per device.

Then the progress screen, polling every three seconds — every call costs the
service a round trip to verify the caller, so it is deliberately unhurried and
stops the moment the job settles.

Two rules in the wizard are worth stating because they are easy to get wrong and
were tested first:

- **Changing anything that changes what would move voids the estimate.** A new
  data window, different child types, a different root. An estimate that
  describes a different migration from the one about to start is worse than no
  estimate.
- **A new target has to prove itself again.** Choosing a different target clears
  the pre-flight, so nobody reaches Start through a check that happened for a
  tenant they are no longer migrating into.

The progress bar counts the engine's own `Device N of M` lines. If that wording
changes the bar disappears and the log does not — a bar inventing a number is
worse than no bar.

## Development

```sh
make ui-install                     # once; the Web SDK is a large tree
cd ui && npm start                  # serves against a real tenant's Administration
make ui-test ui-typecheck           # what runs in make check
```

`npm start` proxies to a live Administration and loads this plugin into it, which
is the only way to see it in its host application. It needs a tenant to point at.

### Versions, and one pin worth knowing about

The Web SDK is `1024.15.16`, which needs Angular 21 and Node 20.19 or newer. Two
things about the toolchain are not what older examples suggest:

- **`c8ycli` no longer exists.** Building is `ng build` with `@c8y/devkit`'s
  Angular builders; `c8y-toolkit` only deploys.
- **`monaco-editor` is pinned to 0.52.2** and must stay pinned. The devkit build
  always compiles Monaco's workers from paths like
  `monaco-editor/esm/vs/editor/editor.worker.js`, and from 0.53 Monaco added an
  `exports` map that rewrites `./X.js` to `./esm/vs/X.js` — so that request
  resolves to `esm/vs/esm/vs/…` and the build fails on five workers this plugin
  does not even use.

## What is not here

- **No end-to-end test.** There is no browser test and no mocked platform
  standing in for one: it would prove only that the mock agrees with itself. The
  pure logic is tested; the wiring is verified by running it against a tenant.
- **No export or import screens.** Both exist to produce and consume files, and a
  browser is the wrong place for either. They stay in
  [the command line tool](cli.md).
- **No tenant subscription screen.** The platform already has one, and doing it
  here would duplicate a permission model that is not ours.
