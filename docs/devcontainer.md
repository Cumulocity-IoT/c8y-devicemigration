# The devcontainer: an autonomous agent, contained

A sandbox in which a coding agent can run in **auto mode** — every tool call
executing without a person approving it — against this repository, without
being able to reach the host's home directory, SSH keys, tenant credentials,
or arbitrary network. It is [.devcontainer/](../.devcontainer/): a Dockerfile
holding the repository's whole toolchain, a `devcontainer.json` that mounts
the repository and nothing else of the host, and `init-firewall.sh`, the
egress filter the other two exist to run.

The design was chosen against three alternatives in what is now this page;
the losing options and the scoring are preserved in the git history of
`docs/concepts/devcontainer.md`.

## The threat, and the three boundaries

An agent's mistakes execute, and so does anything a prompt injection in
fetched content smuggles in. On a laptop the blast radius is the machine:
`~/.ssh`, the `~/.cumulocity` session files with real tenant credentials, and
an unrestricted network to carry anything away. The container reduces that to
a directory and an allowlist:

- **No host home directory.** The container sees the repository bind mount
  and three named volumes. Not `~`, not `~/.ssh`, not `~/.cumulocity`, and —
  decisively on Colima — **not the docker socket**: Colima's VM mounts the
  user's home directory, so the socket *is* the home directory. No
  devcontainer *feature* is used for the same reason; several mount that
  socket.
- **No arbitrary network.** Egress is default-drop with an allowlist of what
  the build actually needs, enforced in the kernel where the non-root agent
  cannot reach it. The one wildcard — `*.cumulocity.com`, because the UI dev
  server proxies to a tenant chosen per session — is what shaped the whole
  mechanism (below).
- **Host-only stays host-only.** `make service-deploy` / `ui-deploy` need
  the `~/.cumulocity` sessions, `service-build`/`service-image` need a
  docker daemon, `test-bash3` needs macOS bash 3.2 — none of that runs
  inside, and nothing inside can. CI covers the bash-3.2 suite.

## How the filter works

**dnsmasq feeds an ipset; iptables default-drops everything the ipset has
not vouched for.** dnsmasq is the container's only resolver. For each
allowed domain it carries a forwarding rule (`server=/domain/upstream`) and a
population rule (`ipset=/domain/allowed`), and — deliberately — **no default
upstream**: a lookup for anything off-list is REFUSED locally, so
off-allowlist access fails in milliseconds with a legible error, and DNS
tunnelling has no carrier. A lookup for an allowed name drops the answering
address into the `allowed` ipset *at resolve time*, which is what makes a
wildcard expressible at all — `cumulocity.com` covers every tenant — and
what keeps CDN rotation (npmjs is on Fastly) from turning a green start into
mysterious failures an hour in.

iptables then drops OUTPUT except: loopback (the agent's path to dnsmasq),
established flows, DNS to the one captured upstream resolver **for root
only** — dnsmasq runs as root, and the owner match is what stops a non-root
process from bypassing dnsmasq's REFUSED-by-default and tunnelling data out
through the upstream's recursion — and TCP 443/80 to addresses in the ipset.
A final REJECT rule makes off-list connects fail fast rather than hang; the
DROP policy remains as the backstop. INPUT drops everything but loopback,
established flows and TCP 4200 (the dev server's published port). IPv6 is
disabled by sysctl and all-dropped besides, so there is one rule set to keep
honest.

Two shapes were rejected on facts about this repository. An **HTTP(S) proxy
with a domain ACL** cannot work: the UI dev server's upstream proxy is
`http-proxy` with an explicit target and ignores proxy environment variables,
so tenant proxying would break silently — and an environment variable is not
a boundary anyway, the constrained process can unset it. A **resolve-once IP
allowlist** (Anthropic's reference devcontainer, whose
NET_ADMIN/iptables/sudoers skeleton this keeps) cannot say
`*.cumulocity.com` and rots as CDN addresses rotate. A transparent
**SNI-filtering sidecar** would be strictly stronger and remains the
hardening step if the threat model ever demands it; it costs a second
privileged container and a compose setup, for a threat model — accidental
and prompt-injected egress, not a determined kernel-adjacent evader — that
does not demand it today.

### The allowlist

Every entry covers its subdomains. What is on it, and why — the list itself
lives in [init-firewall.sh](../.devcontainer/init-firewall.sh) with the same
annotations:

| Domain | Why |
| --- | --- |
| `cumulocity.com` | every tenant; the per-session `--url` of the UI dev server |
| `registry.npmjs.org` | npm packages, and Claude Code's own distribution |
| `proxy.golang.org`, `sum.golang.org` | Go modules |
| `github.com` + `api.`/`codeload.`/`objects.`/`raw.` | `make bootstrap`'s bats clones, release tarballs |
| `api.anthropic.com`, `claude.ai`, `platform.claude.com` | the agent's API and its headless login |
| four VS Code hosts | the server and extensions VS Code downloads *inside* the container on attach; removable if only the devcontainer CLI is used |

Deliberately absent: telemetry hosts —
`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1` tells the agent not to try, so
blocked telemetry never reads as a broken network — and the auto-updater's
hosts (`DISABLE_AUTOUPDATER=1`; the agent updates when the image is rebuilt).
When extending the list, keep it free of public DoH resolvers: an allowed
host that answers DNS-over-HTTPS would be a resolver bypass.

## The trust boundary, stated plainly

The firewall script's *source* is in the repository, inside the bind mount —
the agent can edit it. What sudo runs is the **root-owned copy baked into the
image** at build time, and the container user's single sudoers line names
exactly that path. An agent's edits to `.devcontainer/` take effect only when
a person rebuilds the container, which is a host-side, human-driven action.
That is the boundary, and it carries a standing rule: **review the
`.devcontainer/` diff before any rebuild.** If the script ran from the bind
mount instead, the agent could rewrite it and re-run sudo in the same breath.

The script re-applies on every container start (`postStartCommand`) because
iptables state dies with the network namespace, and it **verifies itself
fatally**: off-list DNS must come back REFUSED, an off-list HTTPS connect
must fail, npmjs and the agent's API must connect — otherwise the start
aborts, because a filter that half-applied is worse than none. It also owns
the named volumes' chown (they mount root-owned), so the audited sudo
surface stays one line.

## Daily flow

1. `colima start`, then VS Code **Reopen in Container** (or `devcontainer
   up`). The first start primes bats, `ui/node_modules` and the Go module
   cache *through* the firewall — day one doubles as a live test of the
   allowlist.
2. Run the agent from a clean shell: `docker exec -it <container> bash`, then
   `claude --dangerously-skip-permissions`. First run only: `/login` prints a
   URL — open it in the **host** browser, paste the code back. The token
   lands in the `c8y-devmig-claude` volume and survives rebuilds
   (`CLAUDE_CONFIG_DIR` points there; without it the OAuth account in
   `.claude.json` would not).
3. The checks all run inside: `make check JOBS=$(nproc)` — on Linux `make`
   *is* GNU make. Every tool the Makefile can use is baked into the image at
   CI's own pins (shellcheck 0.11.0, docopt-sh 2.0.3, actionlint 1.7.12,
   jq 1.7.1, parallel), deliberately: the Makefile skips gracefully when a
   tool is absent, and a sandbox whose `check` is quieter — or merely
   *different* — than CI would be a trap. Three of those pins were set by
   in-container failures on day one (see below), which is the system working.
4. The UI dev server, inside the container:
   `cd ui && npm start -- --url https://<tenant>.cumulocity.com --host
   0.0.0.0` — `--host` is mandatory, the server binds localhost by default
   and a published port would forward to nothing. On the host, open
   `http://localhost:4200/apps/administration/…` exactly as
   [docs/ui.md](ui.md) describes. It must be `localhost`: the dev proxy
   rewrites tenant auth cookies with `cookieDomainRewrite: 'localhost'`,
   which is also why `appPort` pins the publish to the host's loopback.
5. Host-only, as before: deploys, image builds, `test-bash3`, tagging.

Three named volumes keep state across rebuilds: the agent's `~/.claude`, the
Go module cache, and `ui/node_modules` — the last both for speed (784 MB
performs far better on the VM's filesystem than over the virtiofs bind) and
for isolation (bind-shadowing keeps the container's tree apart from any
host-side one, so host runs and container runs cannot corrupt each other).

## Gotchas that would quietly breach the isolation

- **VS Code forwards the host's SSH agent automatically** whenever the host
  has `SSH_AUTH_SOCK` set, which on macOS is always — a signing oracle for
  every host key, exactly what this sandbox exists to withhold. The image
  unsets `SSH_AUTH_SOCK` in both `bash.bashrc` and a profile snippet;
  running the agent via `docker exec` never sets the forward up at all; and
  the honest residual — the proxied socket *file* exists under `/tmp` while
  a VS Code window is attached, and a process that globs for it could use
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
  address sits in the ipset for up to 24 h (the entries time out; conntrack
  keeps established flows alive past expiry); any unrelated hostname
  co-hosted on that address is reachable by direct-IP HTTPS with a forged
  SNI until then. Closing this fully is the deferred SNI sidecar.
- **Allowed hosts are exfiltration channels by definition** — a gist on
  github.com, a managed object on a tenant. Egress filtering bounds *where*
  data can go, not *whether*. No credentials are mounted, so authenticated
  channels do not exist unless the agent is handed a token.
- **A startup gap of seconds:** the network is open between container start
  and `postStartCommand`. The agent is not running yet; an entrypoint-based
  close is possible and deliberately not done in the first version, for
  debuggability.

## Verified when this was built

Run on Colima (arm64) the day this shipped, all inside the container:

- `init-firewall.sh` self-verification: **`FIREWALL OK`** — which proves in
  one line that the Colima VM kernel autoloads `ip_set`/`xt_set`, that
  Debian bookworm's dnsmasq honors `ipset=` as packaged, that off-list DNS
  is REFUSED, and that the allowlist path carries traffic.
- An arbitrary tenant, `https://eu-latest.cumulocity.com/tenant/loginOptions`
  → HTTP 401: the wildcard admits a host it was never told about, and the
  connection reaches the platform.
- `curl https://example.com` fails in **5 ms** (the REFUSED resolver answers
  before iptables is even consulted).
- The upstream resolver, queried directly by uid 1000: *no servers could be
  reached* — the owner match holds, so the agent's only resolver is dnsmasq.
- `sudo -l` lists exactly the firewall script; `sudo iptables -L` is denied.
- `SSH_AUTH_SOCK` is unset in a login shell.
- Toolchain as pinned: go1.25.1, Node 22, shellcheck 0.11.0, actionlint
  1.7.12, docopt.sh, parallel, claude present.
- `make bootstrap ui-install`, `go mod download` and the full
  `make check JOBS=$(nproc)` — through the firewall, green, with no
  "skipping" lines.
- The first in-container `make check` also demonstrated why the pins matter,
  by failing: the image initially carried actionlint 1.7.7, which predates
  actionlint telling shellcheck about `env:` keys and flagged SC2153 on a
  workflow the real toolchain accepts. A drifted linter makes `check` say
  something different inside than outside — in either direction — which is
  exactly the trap the pinning exists to close. The image now pins 1.7.12,
  matching what the repository develops against.
- And one refusal that proved a boundary: a test harness ran `sudo chown`
  inside the container and was denied — the single sudoers line really does
  admit only the firewall script.
- The differential test earned its keep across *tool* versions, not just
  implementations: with bookworm's jq 1.6 in the image, the bash exporter's
  jq-piped records differed from Go's verbatim ones — jq 1.6 reformats JSON
  numbers — and `TestDifferentialExportWithData` failed exactly there. The
  image pins jq 1.7.1 from upstream, matching the host and CI's ubuntu.
- A second first-run finding: the bind mount keeps the host uid (501 under
  Colima) while the container user is 1000 — writable, but git refuses the
  mismatch as "dubious ownership", which kills `go build`'s VCS stamping and
  silently degrades the Makefile's git-derived version. The image bakes
  `git config --system safe.directory '*'`: the ownership check defends
  multi-user machines from repos owned by somebody else, and in a
  single-user sandbox whose repo is by construction owned by a foreign uid
  it can only misfire.

Still to be proven by use, having no substitute for a real session: the
browser login end-to-end through the loopback publish, the headless
`claude` login's paste-code flow, and the SSH-agent socket residual while a
VS Code window is attached.
