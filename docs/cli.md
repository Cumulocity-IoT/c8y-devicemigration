# The standalone binary

A single Go binary built on the **go-c8y v2** library, talking to the REST API
directly instead of shelling out to `c8y`. Same commands as
[the extension](extension.md) and the same behaviour — a differential test diffs
their output on every build — plus a `preflight` command of its own.

What it migrates, and the limits it enforces, is in
[the main README](../README.md). Why it exists is in
[docs/internals.md](internals.md#why-there-are-two-implementations).

It reads the same session files `go-c8y-cli` writes, including encrypted ones —
`C8Y_PASSPHRASE` is used exactly as `c8y` uses it. Unlike `c8y` it does not need
a session to be active first, and it refuses a `--session` glob that matches more
than one file rather than silently taking the first.

## Building and installing

From a release, which is the easiest way and needs no Go toolchain — pick the
archive for your platform from the
[releases page](https://github.com/Cumulocity-IoT/c8y-devicemigration/releases),
check it against `SHA256SUMS`, and put the binary on your `PATH`:

```sh
tar -xzf c8y-devicemigration-<version>-darwin-arm64.tar.gz
shasum -a 256 -c SHA256SUMS
```

`c8y-devicemigration --version` then says which build it is, which is worth
knowing before reporting anything about it.

Or from source:

```sh
make go-build   # -> bin/c8y-devicemigration
make install    # -> $(INSTALL_DIR), default ~/.local/bin
```

```sh
make go-build      # -> bin/c8y-devicemigration

# --session is optional: without it the active go-c8y-cli session is used
eval "$(c8y sessions set --sessionFilter source-tenant)"
./bin/c8y-devicemigration export --rootId 12345 --output-directory ./my-export

./bin/c8y-devicemigration preflight \
    --sourceSession 'source.eu-latest.cumulocity.com-me.json' \
    --targetSession 'target.eu-latest.cumulocity.com-me.json'

./bin/c8y-devicemigration export --rootId 12345 \
    --output-directory ./my-export \
    --session 'source.eu-latest.cumulocity.com-me.json'
```

It reads the same session files go-c8y-cli writes, including encrypted ones —
`C8Y_PASSPHRASE` is used exactly as `c8y` uses it. Unlike `c8y`, it does not need
a session to be active first, and it refuses a `--session` glob that matches more
than one file rather than silently taking the first.

**The port is complete**: `preflight`, `export`, `import`, `revert` and
`migrate`, covering the hierarchy, its external identities, its relationships,
the run marker fragment, device data — measurements, events, alarms and event
attachments — and `migrate`'s `--incremental` and `--resume`.

```sh
eval "$(c8y sessions set --sessionFilter source-tenant)"   # any session will do

./bin/c8y-devicemigration migrate --rootId 12345 \
    --sourceSession source.json --targetSession target.json \
    --dataTypes measurements,events,alarms --incremental --force
```

Because migrate calls the export and import in-process rather than shelling out,
there is no session plumbing between phases and no subprocess boundary to lose an
exit code across.

`--dataTypes all` is a shortcut for `measurements,events,alarms` in both
implementations. It cannot be combined with named types: `all,events` is refused
rather than guessed at, because either reading of it is plausible.

## Confirmations

Destructive commands ask in the shape `c8y` asks, so the keys mean what someone
used to `c8y` expects:

```
Confirm (job: 1)
Migrate 12345 from source.eu-latest.cumulocity.com (t11223344) to target.eu-latest.cumulocity.com (t55667788)
[y] Yes  [a] Yes to All  [n] No  [l] No to All
```

`y`, `yes`, `a` and `yestoall` proceed; anything else, including an empty line,
declines — a typo must never be read as consent. `--force` skips the question.
The dialog goes to **stderr**, as c8y's does, so redirecting stdout does not hide
it.

One question rather than c8y's per-request dialog: `revert` has already printed
its whole plan by then, and asking once about that is clearer than asking N
times.

## Shell completion

```sh
make install-completion
```

That does both halves, because completion needs both:

1. **The binary has to be on `PATH` under its own name.** The generated script is
   keyed to the bare command name and calls the binary back to compute
   completions, so a `./bin/c8y-devicemigration` that is not on `PATH` completes
   nothing — silently. `make install` puts it in `$(INSTALL_DIR)`, which defaults
   to `~/.local/bin`.
2. **The script has to be somewhere on the shell's completion path.**
   `$(ZSH_COMPLETION_DIR)` defaults to Homebrew's `share/zsh/site-functions`,
   which is already on zsh's `fpath`.

Then start a new shell. If completions do not appear, zsh is serving a cached
completion dump: `rm -f ~/.zcompdump*; exec zsh`.

Both directories are overridable:

```sh
make install-completion INSTALL_DIR=~/bin ZSH_COMPLETION_DIR=~/.zsh/completions
```

For a directory of your own, add it to `fpath` **before** `compinit` runs:

```sh
fpath=(~/.zsh/completions $fpath)
autoload -Uz compinit && compinit
```

macOS zsh already runs `compinit`, so there is usually nothing to add.

For other shells:

```sh
source <(c8y-devicemigration completion bash)     # bash
c8y-devicemigration completion fish | source      # fish
c8y-devicemigration completion powershell         # powershell
```

Command and flag names complete out of the box. Flag **values** complete for the
ones with a knowable set:

| Flag | Completes to |
| --- | --- |
| `--session`, `--sourceSession`, `--targetSession` | the session files in the session home, by name |
| `--dataTypes` | `measurements`, `events`, `alarms`, `all` — comma-aware, dropping what is already chosen, and offering `all` only on its own |
| `--childTypes` | `asset`, `device`, `addition`, the same way |

`--rootId` is deliberately **not** completed. Resolving device names means
querying the tenant, and completion runs on every tab press: a shell that pauses
for an authenticated round trip is worse than one that completes nothing. That is
the one place `c8y` goes further, and it can afford to because it already has a
session loaded.

Dates work as they do in the extension, including relative forms like `-30d`,
with one difference worth knowing: the bash version handed them to `c8y` to
parse, so this one parses them itself and **refuses** a form it does not
understand rather than guessing at a window.

`revert` prints its plan and asks once before deleting anything, where the bash
version leans on c8y's own per-request dialog; `--force` skips the question in
both. It establishes what actually happened by re-reading every object rather
than trusting the delete responses, so propagation from a parent group and a
declined confirmation are both reported correctly.
`docs/go-port/BEHAVIOUR-CONTRACT.md` records what the two implementations have to
agree on, down to the trailing newline.
