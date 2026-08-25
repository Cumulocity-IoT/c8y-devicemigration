# Development entry points. `make check` is what CI runs.

TESTS_DIR := tests
VENDOR    := $(TESTS_DIR)/vendor
COMMANDS  := commands/export commands/import commands/revert commands/migrate

# Pinned so local and CI agree. Older shellcheck (e.g. Ubuntu's 0.9.0) has a
# cruder reachability analysis and floods the generated docopt parser with
# SC2317; newer versions understand its indirect dispatch.
SHELLCHECK_VERSION   := 0.11.0

BATS_VERSION         := v1.11.1
BATS_SUPPORT_VERSION := v0.3.0
BATS_ASSERT_VERSION  := v2.1.0

# Parallel jobs for the test suite. 1 keeps it sequential, which is what you
# want when debugging a single failure; anything else needs GNU parallel.
JOBS ?= 1
# An empty JOBS would silently drop --jobs and run the whole suite sequentially,
# which is a slow success rather than an honest failure. Refuse it.
ifeq ($(strip $(JOBS)),)
$(error JOBS is empty. Pass a number, for example JOBS=4)
endif
BATS := $(VENDOR)/bats-core/bin/bats --print-output-on-failure \
	$(if $(filter-out 1,$(JOBS)),--jobs $(JOBS))

.PHONY: check test test-fast test-bash3 lint parser-freshness docs docs-freshness \
	go-build go-test go-lint install install-completion bootstrap clean

# go-build is in here on purpose. Without it a stale bin/c8y-devicemigration can
# survive a green `make check`, and the next person to run the binary tests code
# that no longer exists - which happened once, with a bug that had already been
# fixed in the source.
check: lint parser-freshness docs-freshness go-lint go-build go-test test

bootstrap: $(VENDOR)/bats-core $(VENDOR)/bats-support $(VENDOR)/bats-assert

$(VENDOR)/bats-core:
	git clone --depth 1 --branch $(BATS_VERSION) https://github.com/bats-core/bats-core.git $@

$(VENDOR)/bats-support:
	git clone --depth 1 --branch $(BATS_SUPPORT_VERSION) https://github.com/bats-core/bats-support.git $@

$(VENDOR)/bats-assert:
	git clone --depth 1 --branch $(BATS_ASSERT_VERSION) https://github.com/bats-core/bats-assert.git $@

test: bootstrap
	@echo "bash: $$(bash --version | head -1)"
	@echo "parallel jobs: $(JOBS)"
	$(BATS) $(TESTS_DIR)

# The whole suite across every core. Needs GNU parallel; see the JOBS variable.
test-fast:
	@command -v parallel >/dev/null 2>&1 \
		|| { echo "GNU parallel is required for a parallel run"; exit 1; }
	$(MAKE) test JOBS=$$(getconf _NPROCESSORS_ONLN)

# Runs the suite through /bin/bash explicitly. On macOS that is bash 3.2, the
# oldest shell the commands must support; relying on the shebang is not enough
# because /usr/bin/env may find a newer bash earlier on PATH.
test-bash3: bootstrap
	@echo "forcing $$(/bin/bash --version | head -1)"
	@echo "parallel jobs: $(JOBS)"
	/bin/bash $(BATS) $(TESTS_DIR)

lint:
	@if command -v shellcheck >/dev/null 2>&1; then \
		have=$$(shellcheck --version | sed -n 's/^version: //p'); \
		if [ "$$have" != "$(SHELLCHECK_VERSION)" ]; then \
			echo "note: shellcheck $$have, CI pins $(SHELLCHECK_VERSION); results may differ"; \
		fi; \
		shellcheck $(COMMANDS) $(TESTS_DIR)/fake/c8y $(TESTS_DIR)/helpers/load.bash \
			tools/gen-command-docs.sh \
			&& echo "shellcheck $$have: clean"; \
	else \
		echo "shellcheck not installed, skipping (CI enforces this)"; \
	fi

# The embedded docopt parser is generated from the DOC string. A stale DOC is
# caught at runtime (exit 70), but a hand-edited parser with an untouched DOC is
# not, so compare the committed parser against a freshly generated one.
docs:
	@tools/gen-command-docs.sh

docs-freshness:
	@tools/gen-command-docs.sh --check

parser-freshness:
	@if command -v docopt.sh >/dev/null 2>&1; then \
		for f in $(COMMANDS); do \
			sed -n '/docopt parser below/,/docopt parser above/p' $$f \
				| sed '1d;$$d' | grep -v '^# shellcheck' > /tmp/committed.$$$$; \
			docopt.sh --parser $$f | grep -v '^# shellcheck' > /tmp/fresh.$$$$; \
			if ! diff -q /tmp/committed.$$$$ /tmp/fresh.$$$$ >/dev/null; then \
				echo "$$f: committed parser is stale, run: docopt.sh $$f"; \
				rm -f /tmp/committed.$$$$ /tmp/fresh.$$$$; exit 1; \
			fi; \
			rm -f /tmp/committed.$$$$ /tmp/fresh.$$$$; \
			echo "$$f: parser up to date"; \
		done; \
	else \
		echo "docopt.sh not installed, skipping (pipx install docopt-sh==2.0.3)"; \
	fi

# --- the Go port -------------------------------------------------------------
#
# A second implementation of the same commands on the go-c8y v2 library, living
# beside the bash one rather than replacing it. What the two have to agree on is
# recorded in docs/go-port/BEHAVIOUR-CONTRACT.md.
#
# go-test includes the differential test, which runs BOTH implementations against
# the same fixture and diffs the artefacts and stderr. It needs bash and jq and
# skips without them.
#
# The timeout is not decoration: the test fixture contains a cycle, so a broken
# visited set makes the export recurse forever rather than fail.
GO_TIMEOUT := 300s

go-build:
	go build -o bin/c8y-devicemigration ./cmd/c8y-devicemigration
	@echo "built bin/c8y-devicemigration"

go-test:
	go test ./... -timeout $(GO_TIMEOUT)

# Where `make install` puts the binary. Shell completion needs it on PATH under
# its own name: the generated script is keyed to the bare command name and calls
# the binary back to compute completions, so `./bin/c8y-devicemigration` on its
# own completes nothing.
INSTALL_DIR ?= $(HOME)/.local/bin

# Where `make install-completion` puts the zsh completion. The default is the
# Homebrew site-functions directory, which is already on zsh's fpath here.
ZSH_COMPLETION_DIR ?= $(shell brew --prefix 2>/dev/null)/share/zsh/site-functions

install: go-build
	@mkdir -p "$(INSTALL_DIR)"
	install -m 0755 bin/c8y-devicemigration "$(INSTALL_DIR)/"
	@echo "installed $(INSTALL_DIR)/c8y-devicemigration"
	@command -v c8y-devicemigration >/dev/null 2>&1 \
		|| echo "note: $(INSTALL_DIR) is not on PATH, so completion will not fire"

install-completion: install
	@mkdir -p "$(ZSH_COMPLETION_DIR)"
	c8y-devicemigration completion zsh > "$(ZSH_COMPLETION_DIR)/_c8y-devicemigration"
	@echo "installed $(ZSH_COMPLETION_DIR)/_c8y-devicemigration"
	@echo "start a new shell, or: rm -f ~/.zcompdump*; exec zsh"

go-lint:
	@unformatted=$$(gofmt -l cmd internal); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt would change these files:"; echo "$$unformatted"; exit 1; \
	fi; \
	echo "gofmt: clean"
	go vet ./...
	@echo "go vet: clean"

clean:
	rm -rf $(VENDOR) bin
