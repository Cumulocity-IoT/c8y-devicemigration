# The sandbox image: this repository's whole toolchain, an autonomous agent,
# and the firewall that contains it. See docs/devcontainer.md for the design.
#
# node:22-bookworm rather than a Go base: Node 22 matches CI's pin, the full
# image already ships git, python3 and make with a uid-1000 `node` user, and
# Debian's dnsmasq is built with the ipset support the firewall depends on.
# Adding Go to a Node image is one tarball; the reverse is not.
#
# The image build runs on the host with an open network - only the *running*
# container is filtered - so pinned tarballs may come from hosts the runtime
# allowlist never mentions (go.dev, github release downloads).
FROM node:22-bookworm

# Provided by buildx: arm64 under Colima on Apple Silicon, amd64 elsewhere.
ARG TARGETARCH

# The build/test tools the Makefile calls, and the firewall's own stack
# (dnsmasq, ipset, iptables; dnsutils for the verification digs). Every tool
# `make check` can use is baked in deliberately: the Makefile skips gracefully
# when one is absent, and a sandbox whose check is quieter than CI is a trap.
RUN apt-get update && apt-get install -y --no-install-recommends \
	zip unzip xz-utils \
	dnsmasq ipset iptables dnsutils iproute2 procps \
	sudo less \
	&& rm -rf /var/lib/apt/lists/*

# Go, the exact version go.mod names. GOTOOLCHAIN=local in the container env
# forbids auto-downloading a newer one through the firewall, so what is here is
# what runs.
ARG GO_VERSION=1.25.1
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" \
	| tar -C /usr/local -xz
ENV PATH=$PATH:/usr/local/go/bin:/home/node/go/bin

# shellcheck, the exact version CI pins (Makefile: SHELLCHECK_VERSION) - a
# missing one would make `make lint` silently skip.
ARG SHELLCHECK_VERSION=0.11.0
RUN arch=$([ "$TARGETARCH" = "arm64" ] && echo aarch64 || echo x86_64) \
	&& curl -fsSL "https://github.com/koalaman/shellcheck/releases/download/v${SHELLCHECK_VERSION}/shellcheck-v${SHELLCHECK_VERSION}.linux.${arch}.tar.xz" \
	| tar -xJ -C /tmp \
	&& install /tmp/shellcheck-v${SHELLCHECK_VERSION}/shellcheck /usr/local/bin/ \
	&& rm -rf /tmp/shellcheck-v${SHELLCHECK_VERSION}

# actionlint for workflow-lint. Pinned like everything else; the Makefile only
# suggests brew, which does not exist here. 1.7.12 and not older: 1.7.7 does not
# yet tell shellcheck about `env:` keys, so it flags SC2153 on a workflow the
# real toolchain accepts.
ARG ACTIONLINT_VERSION=1.7.12
RUN curl -fsSL "https://github.com/rhysd/actionlint/releases/download/v${ACTIONLINT_VERSION}/actionlint_${ACTIONLINT_VERSION}_linux_${TARGETARCH}.tar.gz" \
	| tar -xz -C /usr/local/bin actionlint

# The agent itself. Updates arrive by rebuilding this image (the runtime env
# sets DISABLE_AUTOUPDATER=1, and the updater's hosts are not in the allowlist).
RUN npm install -g @anthropic-ai/claude-code

# Mount points for the named volumes, pre-owned by the user so a first mount
# has the right owner even before the firewall script's chown runs.
RUN mkdir -p /home/node/.claude /home/node/go && chown -R node:node /home/node

# The firewall, root-owned in the image - NOT run from the bind-mounted repo.
# The source lives in .devcontainer/ where the agent can edit it; this copy is
# what sudo actually executes, and it changes only when a person rebuilds.
# The sudoers line below is the container user's ONLY sudo grant.
COPY init-firewall.sh /usr/local/bin/init-firewall.sh
RUN chown root:root /usr/local/bin/init-firewall.sh \
	&& chmod 755 /usr/local/bin/init-firewall.sh \
	&& echo "node ALL=(root) NOPASSWD: /usr/local/bin/init-firewall.sh" >/etc/sudoers.d/node-firewall \
	&& chmod 440 /etc/sudoers.d/node-firewall

# The bind-mounted repository keeps the host user's uid (501 under Colima),
# while the container user is 1000 - writable, because Colima maps permissions,
# but git refuses the mismatch as "dubious ownership", which kills go build's
# VCS stamping and silently degrades the Makefile's git-derived version. The
# ownership check defends multi-user machines from repos owned by somebody
# else; in a single-user sandbox whose repo is by construction owned by a
# foreign uid, it can only misfire.
RUN git config --system safe.directory '*'

# VS Code forwards the host's SSH agent into integrated terminals whenever the
# host has one, which on macOS is always - a signing oracle for every host key,
# exactly what this sandbox exists to withhold. Unset in both files because VS
# Code terminals are not reliably login shells. Residual risk and the docker
# exec alternative are in docs/devcontainer.md.
RUN echo 'unset SSH_AUTH_SOCK' >/etc/profile.d/00-no-ssh-agent.sh \
	&& echo 'unset SSH_AUTH_SOCK' >>/etc/bash.bashrc

USER node
