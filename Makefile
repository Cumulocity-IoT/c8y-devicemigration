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

.PHONY: check test test-fast test-bash3 lint workflow-lint parser-freshness docs docs-freshness \
	go-build go-test go-lint install install-completion bootstrap clean \
	service-build service-image service-package service-deploy dist \
	ui-install ui-types ui-types-freshness ui-test ui-typecheck ui-build ui-package ui-deploy \
	version version-check tag

# go-build is in here on purpose. Without it a stale bin/c8y-devicemigration can
# survive a green `make check`, and the next person to run the binary tests code
# that no longer exists - which happened once, with a bug that had already been
# fixed in the source.
check: version-check lint parser-freshness docs-freshness ui-types-freshness go-lint go-build \
	go-test ui-typecheck ui-test test

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

# Workflows are code too, and nothing else here would notice a broken one until
# it failed in CI - or, for the release workflow, until a tag was pushed.
# Optional in the same way shellcheck is: absent means skipped, not broken.
lint: workflow-lint
workflow-lint:
	@if command -v actionlint >/dev/null 2>&1; then \
		actionlint .github/workflows/*.yml && echo "actionlint: clean"; \
	else \
		echo "actionlint not installed, skipping (brew install actionlint)"; \
	fi

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

# What a build calls itself.
#
# ./VERSION holds the version being developed - the one the next release will
# carry - and it is the only place it is written down. Everything else is derived
# from it and from git:
#
#   a release      an exact v-tag on a clean tree, so 0.2.0
#   anything else  a snapshot of the version being developed, so
#                  0.2.0-SNAPSHOT
#
# Snapshots by default, because the alternative has already cost an afternoon:
# the old default took the nearest tag, so a working tree with a fortnight of
# unreleased work in it built an image labelled exactly like the released
# version. Nothing about the running service then said whether it was current,
# and the answer to "why is my change not there" was a deploy nobody had done.
#
# One snapshot version rather than one per commit, deliberately: every upload
# replaces the same version instead of leaving a list of them behind, which is
# what you want while iterating. The cost is that two snapshot builds cannot be
# told apart by their version - if that question comes up, the answer is to
# release, or to pass VERSION explicitly.
#
# The leading v is stripped with sed rather than $${v\#v}: a '#' inside a
# $(shell ...) is escaped differently by make 3.81 and make 4.4, and the version
# that gets it wrong silently comes out empty.
VERSION_FILE := VERSION
DEVELOPING := $(shell head -1 $(VERSION_FILE) 2>/dev/null | tr -d '[:space:]')

VERSION ?= $(shell \
	tag=$$(git describe --tags --match 'v[0-9]*.[0-9]*.[0-9]*' --exact-match 2>/dev/null \
		| sed 's/^v//'); \
	dirty=$$(git status --porcelain 2>/dev/null | head -1); \
	if [ -n "$$tag" ] && [ -z "$$dirty" ]; then echo "$$tag"; \
	else echo "$(DEVELOPING)-SNAPSHOT"; fi)

GO_LDFLAGS := -s -w -X github.com/Cumulocity-IoT/c8y-devicemigration/internal/version.Version=$(VERSION)

# What this build would call itself, without building anything. Worth having:
# the version is derived from three things at once, and "which one am I looking
# at" is a question that has already been answered wrongly.
version:
	@echo "$(VERSION)"

# Tags still decide what a release is; ./VERSION decides what it is allowed to be
# called. This does the tagging, so the two cannot drift:
#
#   1. bump VERSION, commit it
#   2. make tag       -> creates v<VERSION> on that commit
#   3. push the tag   -> the release workflow builds and publishes it
#   4. bump VERSION again, so snapshots move on to the next version
#
# It refuses a dirty tree, because a tag names a commit and uncommitted work is
# not in one - the version derivation would call that build a snapshot anyway,
# and a tag pointing at a commit that was never built is the kind of thing
# nobody notices until a release is wrong.
#
# Pushing is left to you: it is the irreversible half, and `git push origin
# v0.2.0` is not the part that needed automating.
tag: version-check
	@dirty=$$(git status --porcelain 2>/dev/null | head -1); \
	if [ -n "$$dirty" ]; then \
		echo "the working tree has uncommitted changes, so there is nothing to tag."; \
		echo "Commit them - including $(VERSION_FILE) - and try again."; exit 1; fi
	@if git rev-parse -q --verify "refs/tags/v$(DEVELOPING)" >/dev/null; then \
		echo "v$(DEVELOPING) already exists. Bump $(VERSION_FILE) first, or delete the tag."; \
		exit 1; fi
	git tag -a "v$(DEVELOPING)" -m "v$(DEVELOPING)"
	@echo
	@echo "tagged v$(DEVELOPING). To release it:"
	@echo "    git push origin v$(DEVELOPING)"
	@echo "Then bump $(VERSION_FILE) to the next version, so builds go back to"
	@echo "calling themselves snapshots of something newer."

# The version being developed has to be a plain SemVer version, and a release
# has to be the version somebody declared.
#
# The second half only bites on a release build - an exact tag on a clean tree -
# and compares the tag being released with ./VERSION, so tagging v0.3.0 while the
# file still says 0.2.0 fails here rather than shipping a release nobody wrote
# down. Bump the file first, in the commit that gets tagged.
#
# A dirty tree at a tag is the normal state straight after a release: the version
# in the file is the next one, and the work in the tree is a snapshot of it. That
# must not fail anything.
version-check:
	@test -n "$(DEVELOPING)" || { \
		echo "$(VERSION_FILE) is missing or empty, so nothing knows what is being built."; \
		exit 1; }
	@echo "$(DEVELOPING)" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$$' || { \
		echo "$(VERSION_FILE) says '$(DEVELOPING)', which is not a plain X.Y.Z version."; \
		echo "The suffixes are added by the build: a snapshot gets one, a release does not."; \
		exit 1; }
	@tag=$$(git describe --tags --match 'v[0-9]*.[0-9]*.[0-9]*' --exact-match 2>/dev/null \
		| sed 's/^v//'); \
	dirty=$$(git status --porcelain 2>/dev/null | head -1); \
	if [ -n "$$tag" ] && [ -z "$$dirty" ] && [ "$$tag" != "$(DEVELOPING)" ]; then \
		echo "this commit is tagged v$$tag but $(VERSION_FILE) says $(DEVELOPING)."; \
		echo "Put $$tag in $(VERSION_FILE) and tag that commit, or tag v$(DEVELOPING) instead."; \
		exit 1; \
	fi
	@echo "version: $(VERSION)"

go-build:
	@test -n "$(VERSION)" || { \
		echo "VERSION is empty, so the binaries could not say what they are."; exit 1; }
	go build -ldflags "$(GO_LDFLAGS)" -o bin/c8y-devicemigration ./cmd/c8y-devicemigration
	@echo "built bin/c8y-devicemigration $(VERSION)"
	go build -ldflags "$(GO_LDFLAGS)" -o bin/c8y-devicemigration-service ./cmd/c8y-devicemigration-service
	@echo "built bin/c8y-devicemigration-service $(VERSION)"

# -shuffle=on because a test that only passes in the order it was written is a
# test that will fail one day for reasons nobody can reproduce. This repository
# has already had two: a metrics gauge read while another test's worker was
# unwinding, and a job lookup answered from memory instead of from the platform.
# Go prints the seed it used, and `go test -shuffle=<seed>` replays it exactly.
go-test:
	go test ./... -timeout $(GO_TIMEOUT) -shuffle=on

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

# --- the microservice ---------------------------------------------------------
#
# The same engine, deployed on a parent tenant and subscribed to the tenants it
# migrates between. `make service-package` produces the zip the platform wants:
# cumulocity.json plus the image as a tarball, nothing else.
#
# The image is linux/amd64 because that is all the platform runs. On an Apple
# Silicon machine docker will emulate for the build, which is slow but correct -
# the alternative is an image that uploads fine and never starts.
SERVICE_NAME := devicemigration

# The manifest version has to be SemVer or the platform refuses the upload, so
# this takes the newest v-prefixed tag and falls back to a snapshot rather than
# to `git describe`, whose output for an untagged tree ("f8faeb2-dirty") is not
# a version at all. It is VERSION unless the caller says otherwise, so the
# manifest, the image tag, the archives and the plugin all say the same thing -
# they used to be derived separately, and the two derivations disagreed.
SERVICE_VERSION ?= $(VERSION)

SERVICE_DIR := build
SERVICE_ZIP := $(SERVICE_DIR)/$(SERVICE_NAME)-$(SERVICE_VERSION).zip

# Which container tool to use. podman works the same for build/save.
DOCKER ?= docker

service-build:
	@test -n "$(SERVICE_VERSION)" || { \
		echo "SERVICE_VERSION is empty, so there is nothing to tag the image with."; \
		echo "Pass it explicitly: make service-build SERVICE_VERSION=1.2.3"; exit 1; }
	$(DOCKER) build --platform linux/amd64 \
		--build-arg VERSION=$(SERVICE_VERSION) \
		-t $(SERVICE_NAME):$(SERVICE_VERSION) \
		-f docker/Dockerfile .
	@echo "built image $(SERVICE_NAME):$(SERVICE_VERSION)"

service-image: service-build
	@mkdir -p $(SERVICE_DIR)
	$(DOCKER) save $(SERVICE_NAME):$(SERVICE_VERSION) -o $(SERVICE_DIR)/image.tar
	@echo "saved $(SERVICE_DIR)/image.tar"

# The version in the manifest has to match what is deployed, so it is filled in
# here rather than committed - a committed version is one nobody remembers to
# bump.
service-package: service-image
	@echo "$(SERVICE_VERSION)" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+([-+].*)?$$' \
		|| { echo "SERVICE_VERSION '$(SERVICE_VERSION)' is not SemVer, and the platform will reject it"; exit 1; }
	sed 's/@VERSION@/$(SERVICE_VERSION)/' docker/cumulocity.json > $(SERVICE_DIR)/cumulocity.json
	@# Every zip, not just this version's: a directory holding two versions is
	@# one a release job could upload both of.
	@rm -f $(SERVICE_DIR)/*.zip
	cd $(SERVICE_DIR) && zip -q $(notdir $(SERVICE_ZIP)) cumulocity.json image.tar
	@echo "packaged $(SERVICE_ZIP)"
	@unzip -l $(SERVICE_ZIP)

# Uploads and subscribes in one step. Needs an active go-c8y-cli session on the
# tenant the microservice is to be deployed on.
service-deploy: service-package
	c8y microservices create --file $(SERVICE_ZIP) --name $(SERVICE_NAME)
	@echo "deployed. Subscribe the source and target tenants, and grant"
	@echo "ROLE_DEVICE_MIGRATION_ADMIN to whoever should be allowed to migrate"

# --- release artefacts --------------------------------------------------------
#
# `make dist` cross-compiles the command line tool for every platform a release
# covers and packs each one with its licence and its documentation. The release
# workflow calls exactly this, so what CI publishes can be reproduced here.
#
# The extension is deliberately absent: it is installed from the repository by
# `c8y extension install`, so there is nothing to build for it.
DIST_DIR := dist

# GOOS/GOARCH pairs. arm64 and amd64 for the two systems people run this from,
# and 64-bit Windows.
CLI_PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64

dist:
	@test -n "$(VERSION)" || { \
		echo "VERSION is empty, so the archives would have no version in their names."; \
		echo "Pass it explicitly: make dist VERSION=1.2.3"; exit 1; }
	@rm -rf $(DIST_DIR)
	@mkdir -p $(DIST_DIR)
	@for platform in $(CLI_PLATFORMS); do \
		goos=$${platform%/*}; goarch=$${platform#*/}; \
		name=c8y-devicemigration; \
		if [ "$$goos" = windows ]; then name=$$name.exe; fi; \
		stage=c8y-devicemigration-$(VERSION)-$$goos-$$goarch; \
		mkdir -p "$(DIST_DIR)/$$stage"; \
		echo "building $$goos/$$goarch"; \
		CGO_ENABLED=0 GOOS=$$goos GOARCH=$$goarch \
			go build -trimpath -ldflags "$(GO_LDFLAGS)" \
			-o "$(DIST_DIR)/$$stage/$$name" ./cmd/c8y-devicemigration || exit 1; \
		cp LICENSE README.md "$(DIST_DIR)/$$stage/"; \
		cp -R docs "$(DIST_DIR)/$$stage/"; \
		if [ "$$goos" = windows ]; then \
			( cd $(DIST_DIR) && zip -qr "$$stage.zip" "$$stage" ) || exit 1; \
		else \
			tar -czf "$(DIST_DIR)/$$stage.tar.gz" -C $(DIST_DIR) "$$stage" || exit 1; \
		fi; \
		rm -rf "$(DIST_DIR)/$$stage"; \
	done
	@# Written last and listing only the archives, so the file never tries to
	@# check itself.
	@cd $(DIST_DIR) && \
		if command -v sha256sum >/dev/null 2>&1; then \
			sha256sum *.tar.gz *.zip > SHA256SUMS; \
		else \
			shasum -a 256 *.tar.gz *.zip > SHA256SUMS; \
		fi
	@echo
	@ls -lh $(DIST_DIR)

# --- the user interface -------------------------------------------------------
#
# An Angular plugin, installable into Administration or any other application.
# The 1024 Web SDK builds with the Angular CLI and @c8y/devkit's builders;
# c8ycli is gone, and c8y-toolkit only deploys.
#
# Everything here degrades gracefully when the dependencies are not installed,
# the way lint does without shellcheck: a Go-only contributor should not need a
# node_modules directory to run the checks.
UI_DIR   := ui
UI_BIN   := $(UI_DIR)/node_modules/.bin
UI_TYPES := $(UI_DIR)/src/api/types.ts
OPENAPI  := internal/service/openapi.yaml

ui-install:
	cd $(UI_DIR) && npm install --no-audit --no-fund

# The types the UI codes against are generated from the same document the Go
# tests keep true to the handlers, so a field renamed in Go fails the UI build
# rather than arriving as undefined in a browser.
ui-types:
	@test -x $(UI_BIN)/openapi-typescript \
		|| { echo "ui dependencies missing, run: make ui-install"; exit 1; }
	$(UI_BIN)/openapi-typescript $(OPENAPI) --default-non-nullable=false -o $(UI_TYPES)
	@echo "generated $(UI_TYPES)"

# --default-non-nullable=false because a request field with a default is
# optional for the caller: without it the generated MigrationRequest demanded a
# childTypes the API is perfectly happy to fill in itself.
ui-types-freshness:
	@if [ -x $(UI_BIN)/openapi-typescript ]; then \
		fresh=$$(mktemp); \
		if ! $(UI_BIN)/openapi-typescript $(OPENAPI) --default-non-nullable=false -o $$fresh; then \
			echo "could not generate types from $(OPENAPI)"; rm -f $$fresh; exit 1; \
		fi; \
		if [ ! -s $$fresh ]; then \
			echo "generating types from $(OPENAPI) produced nothing"; rm -f $$fresh; exit 1; \
		fi; \
		if diff -q $$fresh $(UI_TYPES) >/dev/null 2>&1; then \
			echo "openapi types: up to date"; rm -f $$fresh; \
		else \
			echo "$(UI_TYPES) is stale, run: make ui-types"; \
			diff $$fresh $(UI_TYPES) | head -20; rm -f $$fresh; exit 1; \
		fi; \
	else \
		echo "ui dependencies not installed, skipping (make ui-install)"; \
	fi

ui-test:
	@if [ -x $(UI_BIN)/jest ]; then \
		cd $(UI_DIR) && ./node_modules/.bin/jest; \
	else \
		echo "ui dependencies not installed, skipping (make ui-install)"; \
	fi

ui-typecheck:
	@if [ -x $(UI_BIN)/tsc ]; then \
		cd $(UI_DIR) && ./node_modules/.bin/tsc --noEmit -p tsconfig.json && echo "tsc: clean"; \
	else \
		echo "ui dependencies not installed, skipping (make ui-install)"; \
	fi

ui-build:
	cd $(UI_DIR) && ./node_modules/.bin/ng build --configuration production

# The plugin's own version lives in ui/package.json, because npm needs it there.
# This sets it so the zip the platform is given says the same thing as
# everything else in a release.
ui-package:
	@echo "$(VERSION)" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+([-+].*)?$$' \
		|| { echo "VERSION '$(VERSION)' is not SemVer, and the platform will reject it"; exit 1; }
	cd $(UI_DIR) && npm version --no-git-tag-version --allow-same-version $(VERSION) >/dev/null
	$(MAKE) ui-build
	@mkdir -p $(SERVICE_DIR)
	cp $(UI_DIR)/dist/devicemigration-ui.zip $(SERVICE_DIR)/devicemigration-ui-$(VERSION).zip
	@echo "packaged $(SERVICE_DIR)/devicemigration-ui-$(VERSION).zip"
	@unzip -l $(SERVICE_DIR)/devicemigration-ui-$(VERSION).zip | tail -3

# Needs an active go-c8y-cli session on the tenant to install it into.
ui-deploy: ui-package
	cd $(UI_DIR) && ./node_modules/.bin/c8y-toolkit deploy dist/devicemigration-ui.zip
	@echo "deployed. Add it to an application under Administration > Ecosystem > Extensions"

go-lint:
	@unformatted=$$(gofmt -l cmd internal); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt would change these files:"; echo "$$unformatted"; exit 1; \
	fi; \
	echo "gofmt: clean"
	go vet ./...
	@echo "go vet: clean"

clean:
	rm -rf $(VENDOR) bin $(SERVICE_DIR) $(DIST_DIR) $(UI_DIR)/dist
