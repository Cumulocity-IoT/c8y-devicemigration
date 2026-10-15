# The devcontainer: an autonomous agent, contained

A sandbox in which a coding agent can run in **auto mode** — every tool call
executing without a person approving it — against this repository, without
being able to reach the host's home directory, SSH keys, tenant credentials,
or arbitrary network. It is [.devcontainer/](../.devcontainer/): a Dockerfile
holding the repository's whole toolchain, a `devcontainer.json` that mounts
the repository and nothing else of the host, and `init-firewall.sh`, the
egress filter the other two exist to run.

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
  docker daemon — none of that runs inside, and nothing inside can.

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
| `github.com` + `api.`/`codeload.`/`objects.`/`raw.` | release tarballs, the gh CLI |
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
   up`). The first start primes `ui/node_modules` and the Go module
   cache *through* the firewall — day one doubles as a live test of the
   allowlist.
2. Run the agent from a clean shell: `docker exec -it <container> bash`, then
   `claude --dangerously-skip-permissions`. First run only: `/login` prints a
   URL — open it in the **host** browser, paste the code back. The token
   lands in the `c8y-devmig-claude` volume and survives rebuilds
   (`CLAUDE_CONFIG_DIR` points there; without it the OAuth account in
   `.claude.json` would not). **Claude Code for VS Code** is listed in
   `customizations.vscode.extensions`, so the host's VS Code installs it into
   the container when a window attaches — the editor half of the same agent,
   driving the `claude` the image already carries. It comes only with a VS
   Code window; `devcontainer up` and `docker exec` sessions get the CLI and
   nothing else, which is where the long autonomous runs belong anyway (see
   the SSH-agent gotcha below).
3. The checks all run inside: `make check` — on Linux `make` *is* GNU make.
   Every tool the Makefile can use is baked into the image at CI's own pins
   (shellcheck 0.11.0, actionlint 1.7.12), deliberately: the Makefile skips
   gracefully when a tool is absent, and a sandbox whose `check` is quieter —
   or merely *different* — than CI would be a trap.
4. The UI dev server, inside the container:
   `cd ui && npm start -- --url https://<tenant>.cumulocity.com --host
   0.0.0.0` — `--host` is mandatory, the server binds localhost by default
   and a published port would forward to nothing. On the host, open
   `http://localhost:4200/apps/administration/…` exactly as
   [docs/ui.md](ui.md) describes. It must be `localhost`: the dev proxy
   rewrites tenant auth cookies with `cookieDomainRewrite: 'localhost'`,
   which is also why `appPort` pins the publish to the host's loopback.
5. Host-only, as before: deploys, image builds, tagging.

Three named volumes keep state across rebuilds: the agent's `~/.claude`, the
Go module cache, and `ui/node_modules` — the last both for speed (784 MB
performs far better on the VM's filesystem than over the virtiofs bind) and
for isolation (bind-shadowing keeps the container's tree apart from any
host-side one, so host runs and container runs cannot corrupt each other).

## Why image builds stay outside

`make service-build` / `service-image` are on the host-only list above because
they cannot run inside. Rootless podman and buildah need no daemon and no
socket, but the container cannot run them either:

- `unshare(CLONE_NEWUSER)` is refused by docker's default seccomp profile.
  That part is fixable, with `--security-opt seccomp=unconfined` in `runArgs`.
- With that lifted, `newuidmap` fails: `write to uid_map failed: Operation not
  permitted`. Installing a multi-range mapping needs `CAP_SYS_ADMIN` over the
  new namespace, which the bounding set here does not carry — this container
  has `NET_ADMIN` and `NET_RAW` and nothing else.
- Buildah falls back to a single-uid mapping (podman just fails), and under
  that fallback the image cannot be built either: `COPY --chown=65532:65532`
  is `lchown: invalid argument`, and committing a layer dies on `remount /:
  permission denied`. Mounts inside a single-mapped namespace are refused
  outright — `unshare --user --map-root-user --mount` cannot even change the
  propagation of `/`.

So the price of building images in here is `--cap-add=SYS_ADMIN`, in a sandbox
whose only other capability grant exists to run the firewall and is useless to
the non-root agent. That is a bad trade for a convenience, and the convenience
is small: CI builds the same zip on every push, and the host builds it in one
command. Two things worth knowing if it is ever revisited: the base image pull
needs `docker.io` **and** `production.cloudfront.docker.com` on the allowlist —
Hub redirects blob downloads there — and podman needs
`unqualified-search-registries` configured, or a bare `golang:1.25-alpine` does
not resolve at all.

The daemonless alternative remains open and costs no privilege: build the
scratch image with go-containerregistry from the Go toolchain that is already
here, and keep `docker/Dockerfile` as what CI builds, pinned to it by a test.

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
- **Untracked files ride along in the bind mount.** Anything in the working
  tree is visible inside, including export output and mapping files the
  `.gitignore` keeps out of commits — keep credentials files elsewhere.

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
  close is possible and deliberately not done, for debuggability.

