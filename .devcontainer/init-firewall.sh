#!/bin/bash
#
# The sandbox's egress filter: default-drop, with a DNS-fed allowlist.
#
# This is the security core of the devcontainer, and the one thing sudo lets the
# container user run. What sudo executes is the root-owned copy of this file
# baked into the image at /usr/local/bin/init-firewall.sh - NOT this bind-mounted
# source, which the agent inside can edit. Edits here take effect only when a
# person rebuilds the container. Review the .devcontainer/ diff before any
# rebuild; that review is the trust boundary.
#
# The design (docs/devcontainer.md has the full reasoning): dnsmasq is the
# container's only resolver, carrying a forwarding rule and an
# ipset-population rule per allowed domain and NO default upstream - so a lookup
# for anything off-list is REFUSED locally, and a lookup for an allowed name
# drops the answering IP into the `allowed` ipset at resolve time. iptables then
# default-drops OUTPUT except loopback, established flows, root's DNS to the one
# captured upstream, and TCP 443/80 to addresses in that set. Resolve-time
# population is what makes *.cumulocity.com expressible and survives CDN
# rotation; resolve-once lists can do neither.
#
# Idempotent on purpose: iptables state dies with the container's network
# namespace, so every container start runs this again (postStartCommand).

set -euo pipefail

# Every allowed domain covers itself AND all subdomains (dnsmasq domain match),
# so `cumulocity.com` is every tenant. Each entry says why it is here; keep it
# that way, and keep the list free of public DoH resolvers - an allowed host
# that answers DNS-over-HTTPS would be a resolver bypass.
ALLOWED_DOMAINS=(
	# Every Cumulocity tenant: the UI dev server proxies to one chosen per
	# session, which is the wildcard this whole design exists to allow.
	cumulocity.com
	# npm - the UI's packages, and Claude Code's own distribution.
	registry.npmjs.org
	# Go modules. GOTOOLCHAIN=local in the container env keeps go from also
	# wanting to download toolchains through this.
	proxy.golang.org
	sum.golang.org
	# make bootstrap git-clones the pinned bats repositories; release tarballs
	# and the gh CLI live on the others.
	github.com
	api.github.com
	codeload.github.com
	objects.githubusercontent.com
	raw.githubusercontent.com
	# The agent: its API, and the two hosts its headless login flow exchanges
	# tokens with. Telemetry hosts are deliberately absent - the container env
	# sets CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 so the agent knows not to
	# try - and so are the auto-updater's (DISABLE_AUTOUPDATER=1; updates
	# arrive via npm on rebuild).
	api.anthropic.com
	claude.ai
	platform.claude.com
	# VS Code downloads its server and extensions inside the container when a
	# window attaches; without these, "Reopen in Container" breaks confusingly.
	# Removable if only the devcontainer CLI is used.
	update.code.visualstudio.com
	vscode.download.prss.microsoft.com
	marketplace.visualstudio.com
	gallerycdn.vsassets.io
)

# --- 1. Volume ownership -------------------------------------------------------
#
# Named volumes mount root-owned on first use. This is the single sudo entry
# point, so the chown lives here rather than behind a second sudoers line that
# would widen the audited surface for nothing.
# A failed chown warns rather than aborts: on the named volumes it always
# works, and the one case it cannot - the glob matching a bind-mounted
# directory virtiofs will not let even root re-own - must not take the
# firewall down with it. If a volume ever really has the wrong owner, npm and
# go fail immediately afterwards with permission errors that name the path.
for dir in /home/node/.claude /home/node/go /workspaces/*/ui/node_modules; do
	if [ -d "$dir" ]; then
		chown node:node "$dir" 2>/dev/null ||
			echo "FIREWALL: warning: could not chown $dir (harmless on a bind mount)" >&2
	fi
done

# --- 2. The upstream resolver --------------------------------------------------
#
# Docker injects the real resolvers into /etc/resolv.conf at start. The first
# run saves them; re-runs (resolv.conf already points at dnsmasq) reuse the
# saved copy. Overwritten with cp, not mv: docker bind-mounts the file.
if ! grep -q '^nameserver 127\.0\.0\.1$' /etc/resolv.conf; then
	cp /etc/resolv.conf /etc/resolv.conf.upstream
fi
if [ ! -s /etc/resolv.conf.upstream ]; then
	echo "FIREWALL: no upstream resolver captured - /etc/resolv.conf was empty" >&2
	exit 1
fi
mapfile -t UPSTREAMS < <(awk '/^nameserver /{print $2}' /etc/resolv.conf.upstream)
if [ "${#UPSTREAMS[@]}" -eq 0 ]; then
	echo "FIREWALL: /etc/resolv.conf.upstream names no resolver" >&2
	exit 1
fi

# --- 3. dnsmasq ----------------------------------------------------------------
#
# Its own config file and a manual start: relying on the Debian packaging's
# conf-dir and init wiring inside a container is how a filter half-applies.
# no-resolv + no default server= is the load-bearing part: anything without an
# explicit forwarding rule below is REFUSED locally, so off-list DNS fails fast
# and DNS tunnelling has no carrier. user=root so the ipset population keeps
# CAP_NET_ADMIN; this is a root-owned firewall component either way.
{
	echo "# Written by init-firewall.sh - edits are overwritten on every start."
	echo "listen-address=127.0.0.1"
	echo "bind-interfaces"
	echo "user=root"
	echo "no-resolv"
	echo "no-hosts"
	for domain in "${ALLOWED_DOMAINS[@]}"; do
		for upstream in "${UPSTREAMS[@]}"; do
			echo "server=/${domain}/${upstream}"
		done
		echo "ipset=/${domain}/allowed"
	done
} >/etc/dnsmasq-sandbox.conf

# --- 4. ipset ------------------------------------------------------------------
#
# The 24h timeout bounds how long a stale CDN address stays reachable after a
# resolve; established flows outlive their entry via conntrack. If create fails,
# the Colima VM kernel is missing ip_set - say so instead of continuing open.
if ! ipset create allowed hash:ip timeout 86400 -exist; then
	echo "FIREWALL: 'ipset create' failed - the VM kernel lacks ip_set/xt_set." >&2
	echo "On Colima: colima ssh -- sudo modprobe ip_set xt_set" >&2
	exit 1
fi

# dnsmasq starts only after the set exists, or its first adds would fail.
pkill -x dnsmasq 2>/dev/null || true
dnsmasq --conf-file=/etc/dnsmasq-sandbox.conf
pgrep -x dnsmasq >/dev/null || {
	echo "FIREWALL: dnsmasq did not start" >&2
	exit 1
}
printf 'nameserver 127.0.0.1\n' >/etc/resolv.conf

# --- 5. iptables ---------------------------------------------------------------
iptables -F OUTPUT
iptables -F INPUT
iptables -P OUTPUT DROP
iptables -P INPUT DROP
iptables -P FORWARD DROP

# Loopback: the agent's DNS goes to dnsmasq this way.
iptables -A OUTPUT -o lo -j ACCEPT
iptables -A INPUT -i lo -j ACCEPT

# Answers to connections already admitted.
iptables -A OUTPUT -m state --state ESTABLISHED,RELATED -j ACCEPT
iptables -A INPUT -m state --state ESTABLISHED,RELATED -j ACCEPT

# DNS to the captured upstream - and only for root, which is dnsmasq. Without
# the owner match the agent could bypass dnsmasq's REFUSED-by-default and
# tunnel data out as queries through the upstream's recursion; with it, the
# agent's only resolver is dnsmasq on loopback. DoT (853) and any other
# resolver fall through to the REJECT below.
for upstream in "${UPSTREAMS[@]}"; do
	iptables -A OUTPUT -d "$upstream" -p udp --dport 53 -m owner --uid-owner 0 -j ACCEPT
	iptables -A OUTPUT -d "$upstream" -p tcp --dport 53 -m owner --uid-owner 0 -j ACCEPT
done

# The allowlist itself: HTTPS and plain HTTP to addresses dnsmasq has vouched
# for. Nothing else opens ports outward.
iptables -A OUTPUT -p tcp --dport 443 -m set --match-set allowed dst -j ACCEPT
iptables -A OUTPUT -p tcp --dport 80 -m set --match-set allowed dst -j ACCEPT

# REJECT rather than the DROP policy for what remains, so an off-list connect
# fails in milliseconds with a legible error instead of hanging a tool for two
# minutes. The DROP policy stays as the backstop underneath.
iptables -A OUTPUT -j REJECT

# The one inbound door: the dev server's published port. The docker publish
# proxy connects in from the bridge, so this cannot be scoped to loopback from
# in here - the loopback pinning happens host-side, in appPort.
iptables -A INPUT -p tcp --dport 4200 -j ACCEPT

# IPv6 is disabled by sysctl in runArgs; an all-DROP ruleset is the
# belt-and-braces underneath (tolerated if the kernel has no ip6tables at all).
if command -v ip6tables >/dev/null 2>&1; then
	ip6tables -P OUTPUT DROP 2>/dev/null || true
	ip6tables -P INPUT DROP 2>/dev/null || true
	ip6tables -P FORWARD DROP 2>/dev/null || true
fi

# --- 6. Self-verification, fatal on failure ------------------------------------
#
# A filter that half-applied is worse than none, because it looks applied. Each
# probe checks one property; any failure aborts the start.
fail() {
	echo "FIREWALL: $1" >&2
	exit 1
}

dig +time=3 +tries=1 example.com @127.0.0.1 2>/dev/null | grep -q 'status: REFUSED' ||
	fail "an off-list DNS lookup was not REFUSED - dnsmasq is not filtering"

if curl -s --max-time 5 -o /dev/null https://example.com 2>/dev/null; then
	fail "an off-list HTTPS connection succeeded - the ipset match is not filtering"
fi

curl -fsS --max-time 15 -o /dev/null https://registry.npmjs.org/ ||
	fail "registry.npmjs.org is unreachable - the allowlist path is broken"

curl -s --max-time 15 -o /dev/null https://api.anthropic.com ||
	fail "api.anthropic.com is unreachable - the agent could not work"

echo "FIREWALL OK: default-drop active, $(ipset list allowed | grep -c '^[0-9]' || true) addresses currently allowed"
