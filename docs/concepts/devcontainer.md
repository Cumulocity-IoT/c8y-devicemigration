# Running an autonomous agent in this repository, safely

> **Status: concept, nothing implemented.** No `.devcontainer/` exists yet.
> The design below was researched against this repository's actual build
> (Makefile, CI, the UI dev server's own proxy code) and against the official
> Claude Code devcontainer documentation, and Option 4 was chosen. Claims that
> rest on code that was read are stated as fact with references; claims about
> the host environment that have not been observed are collected in
> ["to verify"](#to-verify) at the end. When this is built, it graduates to a
> real `docs/devcontainer.md` and this file is retired.

## The problem

Letting a coding agent run in **auto mode** — every tool call executes without
a person approving it — means the agent's mistakes execute too, and so does
anything a prompt injection in fetched content manages to smuggle in. On a
developer laptop that blast radius is the whole machine: `~/.ssh`, the
`~/.cumulocity` session files with real tenant credentials, and an unrestricted
network to carry anything away.

The goal is a container in which auto mode is safe *enough*:

- **No host home directory.** Not `~/.ssh`, not `~/.cumulocity`, not the
  keychain. The container sees the repository and nothing else of the host.
- **No arbitrary network.** Egress is an allowlist of what the build actually
  needs, so neither an accident nor an injected instruction can send data to
  an arbitrary host — with one wildcard requirement below.
- **The UI dev workflow survives.** `cd ui && npm start -- --url
  https://<tenant>.cumulocity.com` must keep working with the dev server
  *inside* the container and the browser on the host, which drags a live
  Cumulocity tenant — chosen per session — into the allowed set. Hence
  `*.cumulocity.com` as a wildcard, not a fixed host.

Three boundary decisions were made up front:

- **Runtime is Colima.** One consequence dominates the design: Colima's VM
  mounts the user's home directory, so the docker socket **is** the home
  directory. The socket must never be mounted into the sandbox, and no
  devcontainer feature that mounts it (docker-outside-of-docker) may be used.
- **Deploys stay on the host.** `make service-deploy` and `ui-deploy` need the
  `~/.cumulocity` sessions; those credentials do not enter the container, so
  those targets do not run there. Same for `make service-build`/`service-image`
  (they need a docker daemon — see the socket rule above) and `make test-bash3`
  (macOS bash 3.2 does not exist on Linux; CI covers it).
- **The agent's checks all run inside.** `make check` must pass in-container
  with no step silently skipped — the Makefile degrades gracefully when a lint
  tool is absent, and a sandbox whose `check` is *quieter* than CI would be a
  trap. So the image pins the same tools CI pins.

## What is already true (verified in the code)

- **The UI dev server binds localhost:4200 by default** and must be started
  with `--host 0.0.0.0` in a container or a published port forwards to
  nothing (`@c8y/devkit` dev-server schema defaults; docs/ui.md's development
  section). Everything — app, HMR websocket — is on the one port.
- **The browser must reach it as `localhost:4200`.** The dev proxy rewrites
  the tenant's auth cookies with `cookieDomainRewrite: 'localhost'`
  (`@c8y/devkit` webpack proxy), so the container port has to be published to
  the host's loopback as 4200, and any other hostname breaks tenant login.
- **The dev server's upstream proxy ignores `HTTP(S)_PROXY`.** It is
  `http-proxy` with an explicit target; an egress-filtering HTTP proxy would
  silently break tenant proxying. Whatever filters egress must be transparent
  at the packet level. (Also on principle: an environment variable is not a
  boundary — the agent can unset it.)
- **The build's egress set is small and known.** Go modules from
  `proxy.golang.org` + `sum.golang.org` (no vendor directory); npm from
  `registry.npmjs.org` only (all 1542 resolved URLs in ui/package-lock.json;
  no .npmrc, the `@c8y` scope is public); `make bootstrap` git-clones the
  three pinned bats repositories from `github.com`. That is the whole of
  `make check`'s network.
- **Claude Code's own needs are documented**: `api.anthropic.com` for the
  API, `claude.ai` + `platform.claude.com` for login — which works headless
  via a paste-a-code flow, so no browser is needed inside the container.
  Auth survives rebuilds only if `~/.claude` is a volume **and**
  `CLAUDE_CONFIG_DIR` points at it. Telemetry hosts can stay blocked by
  setting `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1` rather than
  allowlisting them, and `DISABLE_AUTOUPDATER=1` removes the updater's hosts.
- **A reference exists.** Anthropic's own devcontainer for Claude Code uses
  exactly this skeleton: `NET_ADMIN`/`NET_RAW`, an iptables default-drop
  `init-firewall.sh`, and a sudoers entry that permits only that script.
  Option 4 below is that skeleton with one substitution for the wildcard.

## The options

### Option 1 — resolve-once IP allowlist (the reference as-is)

At container start, resolve each allowed domain, freeze the IPs into an
ipset, default-drop everything else. Simple and proven — and it cannot say
`*.cumulocity.com`: the tenant hostname is chosen per session and the
subdomain set is unbounded. It also rots mid-session as CDN IPs rotate
(npmjs is on Fastly), turning a green start into mysterious failures an hour
in. Rejected as-is; its skeleton is kept.

### Option 2 — an HTTP(S) proxy with a domain ACL

Run squid/tinyproxy with an allowlist, set `HTTP_PROXY`/`HTTPS_PROXY`.
Wildcards are natural and the filtering is by *name*, not IP. Rejected
outright for two reasons already stated as fact: the UI dev server's upstream
proxy does not honor those variables, so the primary interactive use case
breaks silently; and an environment variable is advisory — the process being
constrained can remove it. Enforcement has to live below the process.

### Option 3 — a transparent SNI-filtering sidecar

A second container on a custom network inspects the TLS SNI of every
connection and admits by hostname. Strictly stronger than anything IP-based:
immune to shared-CDN-IP reuse, wildcards natural, DNS games irrelevant. Costs:
a compose-based devcontainer, a second privileged component, harder failure
diagnosis on Colima, and encrypted client hello will progressively blind SNI
inspection anyway. **Deferred** — the right second step if the threat model
hardens, not the first.

### Option 4 — dnsmasq-fed ipset (chosen)

The reference skeleton, with the ipset populated **at resolve time** instead
of once at start. dnsmasq is the container's only resolver; for each allowed
domain it carries a `server=/domain/upstream` forwarding rule and an
`ipset=/domain/allowed` rule, and — deliberately — **no default upstream**.
iptables default-drops OUTPUT except loopback, established flows, DNS to the
one captured upstream resolver, and TCP 443/80 to addresses in the ipset.

What this buys, point by point:

- `ipset=/cumulocity.com/allowed` matches **every** subdomain, so the
  per-session tenant works with no per-tenant configuration.
- IPs enter the set exactly when a name is resolved, so CDN rotation is
  followed instead of fought.
- No default upstream means a lookup for anything off-list is **REFUSED
  locally**: DNS tunnelling (exfiltration by encoding data in queries) has no
  carrier, and the agent gets a fast, legible failure instead of a hung
  connect.
- The filter is transparent: the dev server, go, git, npm and curl need no
  proxy awareness at all.
- The non-root agent cannot lift it: the rules live in the kernel,
  `NET_ADMIN` is useless without root, and sudo permits exactly one
  root-owned script.

### Scores

Judged on: **enforcement** (can the constrained process bypass or disable
it); **wildcard** support for `*.cumulocity.com`; **dev-server fit** (does
tenant proxying keep working untouched); **cost** (components, moving parts;
5 = cheapest); **legibility** (does a blocked action fail fast and say why).

| Option | Enforcement | Wildcard | Dev server | Cost | Legibility | Σ |
|---|---|---|---|---|---|---|
| **4** dnsmasq-fed ipset | 4 | 5 | 5 | 4 | 5 | **23** |
| 3 SNI sidecar | 5 | 5 | 5 | 2 | 3 | 20 |
| 1 resolve-once ipset | 4 | 1 | 5 | 5 | 3 | 18 |
| 2 env-var proxy | 1 | 5 | 1 | 4 | 4 | 15 |

Option 4 loses one enforcement point to the sidecar for the shared-CDN-IP
residual (below) and wins everywhere else that matters here.

## The design

Four new files, nothing else in the repository changes.

### `.devcontainer/init-firewall.sh` — the security core

Root-owned, idempotent, `set -euo pipefail`. In order:

1. **Volume ownership fixup** — the named volumes (below) mount root-owned;
   chown them to the container user. It lives here because this script is
   the single sudo entry point, and a second sudoers line for a chown helper
   would widen the audited surface for nothing.
2. **Capture the upstream resolver** — save Docker's injected
   `/etc/resolv.conf` aside on first run, reuse the copy on re-runs.
3. **Write the dnsmasq config** from an `ALLOWED_DOMAINS` array, each entry
   carrying a why-comment: per-domain `server=` + `ipset=` lines, no default
   `server=`, `listen-address=127.0.0.1`, `no-resolv`; restart dnsmasq;
   point `/etc/resolv.conf` at 127.0.0.1.
4. **The allowlist**:
   - `cumulocity.com` — every tenant, the wildcard this design exists for
   - `registry.npmjs.org` — npm, and Claude Code's own npm package
   - `proxy.golang.org`, `sum.golang.org` — Go modules
   - `github.com`, `codeload.github.com`, `api.github.com`,
     `objects.githubusercontent.com`, `raw.githubusercontent.com` —
     `make bootstrap`'s bats clones, release tarballs
   - `api.anthropic.com`, `claude.ai`, `platform.claude.com` — the agent's
     API and its headless login
   - `update.code.visualstudio.com`, `vscode.download.prss.microsoft.com`,
     `marketplace.visualstudio.com`, `gallerycdn.vsassets.io` — VS Code
     downloads its server and extensions *inside* the container; without
     these, attaching breaks confusingly. Removable if only the devcontainer
     CLI is used.
   - Deliberately absent: telemetry hosts (statsig, sentry, datadog —
     blocked and told so via `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`),
     and the auto-updater's hosts (`DISABLE_AUTOUPDATER=1`; updates arrive
     via npm on rebuild).
5. **ipset** `allowed hash:ip timeout 86400` — the timeout bounds how long a
   stale CDN address stays reachable; conntrack's ESTABLISHED rule keeps
   live flows working past expiry.
6. **iptables** — OUTPUT policy DROP, allowing: loopback;
   ESTABLISHED/RELATED; UDP/TCP 53 **only to the captured upstream** (any
   other resolver, and DoT on 853, is dropped — the ipset cannot be
   sidestepped by asking a different DNS server); TCP 443/80 to
   `--match-set allowed dst`; then a REJECT rule so off-list connects fail
   in milliseconds instead of hanging the agent for two minutes. INPUT
   policy DROP allowing loopback, ESTABLISHED, and TCP 4200 (the published
   dev server; the docker publish proxy connects in from the bridge).
   FORWARD DROP. ip6tables all-DROP as belt-and-braces under the
   IPv6-disabling sysctls.
7. **Self-verification, fatal on failure** — `dig example.com @127.0.0.1`
   must come back REFUSED; `curl https://example.com` must fail;
   `curl https://registry.npmjs.org` and `https://api.anthropic.com` must
   connect; then print an unmistakable `FIREWALL OK`. If `ipset create`
   itself fails, die naming the fix (the Colima VM kernel lacking `ip_set`)
   rather than continuing open.

### `.devcontainer/Dockerfile`

Base `node:22-bookworm` — Node 22 matches CI's pin, the full image ships
git/python3/make and a uid-1000 user, Debian's dnsmasq is built with ipset
support, and Go is easier to add to a Node image than the reverse.
Multi-arch via `TARGETARCH` (Colima on Apple Silicon is arm64).

- apt: `jq parallel zip unzip xz-utils pipx dnsmasq ipset iptables dnsutils
  iproute2 procps sudo less`
- Go **1.25.1** pinned tarball (go.mod's version; the image build runs on the
  host with an open network — only the running container is filtered)
- **shellcheck 0.11.0** pinned release tarball — the exact version CI pins,
  because a missing or older shellcheck makes `make check` quieter
  in-container than in CI, which is the trap this image must not set.
  Likewise `docopt-sh==2.0.3` via pipx and a pinned actionlint.
- `npm install -g @anthropic-ai/claude-code`
- pre-create the volume mount points under the user's home, owned by the user
- `COPY init-firewall.sh /usr/local/bin/` root-owned 0755, and the **only**
  sudo grant: `node ALL=(root) NOPASSWD: /usr/local/bin/init-firewall.sh`
- `unset SSH_AUTH_SOCK` in a profile snippet (see the gotchas)
- `USER node`

### `.devcontainer/devcontainer.json`

- `runArgs`: `--cap-add=NET_ADMIN --cap-add=NET_RAW` plus sysctls disabling
  IPv6 (one rule set to keep honest instead of two).
- `appPort: ["127.0.0.1:4200:4200"]` — a docker-level publish pinned to the
  host's loopback, because the cookie rewrite requires `localhost:4200` in
  the browser and loopback keeps the port off the LAN. `appPort` rather than
  `forwardPorts` so the devcontainer CLI works without VS Code's forwarder.
- Mounts — the workspace bind (implicit) plus three named volumes:
  `~/.claude` (auth and history survive rebuilds), `~/go` (module cache off
  the slow virtiofs bind), and `ui/node_modules` (784 MB performs far better
  on the VM's own filesystem, **and** bind-shadowing keeps the container's
  tree independent of any host-side `node_modules`, so host runs and
  container runs cannot corrupt each other). Nothing else: no `~`, no
  `~/.ssh`, no docker socket, and **no devcontainer features** — several
  mount the socket, which on Colima is the home directory.
- `containerEnv`: `GOTOOLCHAIN=local` (go.mod could otherwise trigger a
  toolchain auto-download through the firewall), `CLAUDE_CONFIG_DIR`
  pointing at the `~/.claude` volume (without it the OAuth account in
  `.claude.json` dies on rebuild),
  `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`, `DISABLE_AUTOUPDATER=1`.
- `postCreateCommand`: `sudo init-firewall.sh && make bootstrap ui-install
  && go mod download` — firewall **first**, then dependency priming
  *through* it, so day one doubles as a live test of the allowlist.
- `postStartCommand`: `sudo init-firewall.sh` — iptables state dies with the
  network namespace, so every start re-applies it.

### The trust boundary, stated plainly

The firewall script's *source* lives in this repository, inside the bind
mount — the agent can edit it. What sudo runs is the **root-owned copy baked
into the image** at build time. An agent's edits to `.devcontainer/` take
effect only when the user rebuilds the container, which is a host-side,
human-driven action. That is the boundary, and it comes with a standing
rule: **review the `.devcontainer/` diff before any rebuild.** If the script
ran from the bind mount instead, the agent could rewrite it and re-run sudo
in the same breath — which is why it must not.

## Daily flow

1. `colima start`, then VS Code "Reopen in Container" (or `devcontainer up`).
   First build primes bats, `ui/node_modules` and the Go cache through the
   firewall.
2. Run the agent from a clean shell — `docker exec -it <container> bash`,
   then `claude --dangerously-skip-permissions`. First run only: `/login`
   prints a URL, open it in the **host** browser, paste the code back. The
   token lands in the named volume and outlives rebuilds.
3. The UI: `cd ui && npm start -- --url https://<tenant>.cumulocity.com
   --host 0.0.0.0` — the `--host` is mandatory in a container — and on the
   host open `http://localhost:4200/apps/administration/index.html?remotes=…`
   exactly as [docs/ui.md](../ui.md) already describes.
4. Host-only, as before: deploys, image builds, `test-bash3`, tagging.

## Gotchas that would quietly breach the isolation

- **VS Code forwards the host's SSH agent automatically** when
  `SSH_AUTH_SOCK` is set on the host, which on macOS it always is — a signing
  oracle for every host key, exactly what this sandbox exists to withhold.
  Layered mitigations: the image unsets `SSH_AUTH_SOCK` in its profile;
  running the agent via `docker exec` never sets up the forward at all; and
  the honest residual — while a VS Code window is attached, the proxied
  socket *file* exists under `/tmp` and a process that globs for it could use
  it — is handled by not leaving a window attached during long autonomous
  runs.
- **`~/.gitconfig` is auto-copied** by VS Code. Accepted: name and email are
  attribution, not secrets, and a `credential.helper = osxkeychain` line is
  inert on Linux. Commits and pushes stay host-side anyway.
- **No GPG in the image**, so VS Code's GPG-agent forwarding never activates.
- One untracked file rides along in the bind mount: `id_mapping.json`, a
  real tenant's object-id mapping. Ids, not credentials — accepted, but
  worth knowing it is visible inside.

## Residual risks, accepted knowingly

- **Shared-CDN-IP reuse.** Once `registry.npmjs.org` resolves, that Fastly
  address sits in the ipset for up to 24 h; any unrelated hostname co-hosted
  on it is reachable by direct-IP HTTPS with a forged SNI until the entry
  expires. The threat model here is accidental and prompt-injected egress,
  not a determined evader — but injections can contain literal IPs, which is
  why the entries time out. Closing it fully is Option 3, the deferred
  sidecar.
- **DoH inside an allowed host.** An allowed host that also serves
  DNS-over-HTTPS would be a resolver bypass. None of the listed hosts is a
  public DoH resolver; keep that property when extending the list.
- **Allowed hosts are exfiltration channels by definition** — a gist on
  github.com, a managed object on a tenant. Egress filtering bounds *where*
  data can go, not *whether*. No credentials are mounted, so authenticated
  channels do not exist unless the agent is handed a token.
- **A startup gap of seconds**: the network is open between container start
  and `postStartCommand`. The agent is not running yet; an
  `overrideCommand`-based entrypoint could close it, deliberately not done
  in the first version for debuggability.

## Verification checklist (run when built)

Inside: `curl https://example.com` rejected fast · `curl
https://registry.npmjs.org` connects · `curl -si
https://<tenant>.cumulocity.com/tenant/loginOptions` answers · `dig
example.com` REFUSED and `dig @1.1.1.1 example.com` times out · `~/.ssh`
does not exist · the mount table shows the workspace bind and three named
volumes and nothing else · `sudo -l` lists only the firewall script and
`sudo iptables -L` is denied · `make check JOBS=$(nproc)` is green with **no
"skipping" lines** for shellcheck, docopt-sh or actionlint.

From the host: browser login through `localhost:4200` completes end to end ·
`docker inspect` shows only the expected mounts, and `CapAdd` is exactly
NET_ADMIN + NET_RAW.

## To verify

Held as expectations, not facts, until the container is first built:

- The Colima VM kernel ships `ip_set`/`xt_set` (the script fails loudly if
  not, but the fix — enabling the module in the VM — has not been rehearsed).
- Debian bookworm's dnsmasq honors `ipset=` as packaged (compiled in, and
  not neutered by the systemd unit's sandboxing inside a container).
- VS Code's SSH-agent forwarding behaves as described against a container
  whose profile unsets `SSH_AUTH_SOCK` (the socket file's presence under
  `/tmp` while a window is attached).
- The docker publish proxy's inbound connections to 4200 arrive from the
  bridge gateway address under Colima the way they do under Docker Desktop.
- Headless `claude` login's paste-code flow needs no host beyond `claude.ai`
  and `platform.claude.com`.
