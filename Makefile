# Development entry points. `make check` is what CI runs.

TESTS_DIR := tests
VENDOR    := $(TESTS_DIR)/vendor
COMMANDS  := commands/export commands/import

BATS_VERSION         := v1.11.1
BATS_SUPPORT_VERSION := v0.3.0
BATS_ASSERT_VERSION  := v2.1.0

.PHONY: check test lint parser-freshness bootstrap clean

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
	$(VENDOR)/bats-core/bin/bats --print-output-on-failure $(TESTS_DIR)

# Runs the suite through /bin/bash explicitly. On macOS that is bash 3.2, the
# oldest shell the commands must support; relying on the shebang is not enough
# because /usr/bin/env may find a newer bash earlier on PATH.
test-bash3: bootstrap
	@echo "forcing $$(/bin/bash --version | head -1)"
	/bin/bash $(VENDOR)/bats-core/bin/bats --print-output-on-failure $(TESTS_DIR)

lint:
	@if command -v shellcheck >/dev/null 2>&1; then \
		shellcheck $(COMMANDS) $(TESTS_DIR)/fake/c8y $(TESTS_DIR)/helpers/load.bash \
			&& echo "shellcheck: clean"; \
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
