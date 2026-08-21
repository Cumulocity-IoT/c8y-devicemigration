# Development entry points. `make check` is what CI runs.

TESTS_DIR := tests
VENDOR    := $(TESTS_DIR)/vendor
COMMANDS  := commands/export commands/import commands/revert

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

.PHONY: check test test-fast test-bash3 lint parser-freshness bootstrap clean

check: lint parser-freshness test

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
			&& echo "shellcheck $$have: clean"; \
	else \
		echo "shellcheck not installed, skipping (CI enforces this)"; \
	fi

# The embedded docopt parser is generated from the DOC string. A stale DOC is
# caught at runtime (exit 70), but a hand-edited parser with an untouched DOC is
# not, so compare the committed parser against a freshly generated one.
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

clean:
	rm -rf $(VENDOR)
