#!/usr/bin/env bats
#
# What a build calls itself.
#
# This is here because getting it wrong is expensive and silent. The old scheme
# took the version from the NEAREST tag, so a working tree with unreleased work
# in it produced an image labelled exactly like the released version - and a
# deployed microservice then had nothing on it to say whether it was current.
# An afternoon went into looking for a bug in code the platform was not running.
#
# The Makefile is exercised in a throwaway git repository holding nothing but the
# Makefile and the VERSION file, because the point of these tests is what the
# derivation says about a repository state, and states are easier to create than
# to find.

load helpers/load

setup() {
    REPO="$BATS_TEST_TMPDIR/repo"
    mkdir -p "$REPO"
    cp "$REPO_DIR/Makefile" "$REPO_DIR/VERSION" "$REPO"
    cd "$REPO"

    git init --quiet .
    git config user.email version@test
    git config user.name version
    git add Makefile VERSION
    git commit --quiet -m "a repository with a version in it"
}

# GNU make 4 and make 3.81 disagree about enough that the version is derived
# without any of it - but run whichever is here, since that is what a person
# would get.
version() { run make -s version; }

# --- what a build calls itself ----------------------------------------------

@test "version: an untagged commit is a snapshot of the version being developed" {
    version
    [ "$status" -eq 0 ]
    [ "$output" = "$(cat VERSION)-SNAPSHOT" ]
}

@test "version: every snapshot of a version is the same version" {
    version
    first="$output"

    echo "more work" > later && git add later && git commit --quiet -m later
    version

    [ "$output" = "$first" ]
    [ "$output" = "$(cat VERSION)-SNAPSHOT" ]
}

@test "version: an exact tag on a clean tree is the release version itself" {
    git tag "v$(cat VERSION)"
    version
    [ "$output" = "$(cat VERSION)" ]
}

@test "version: uncommitted work is a snapshot even at a release tag" {
    git tag "v$(cat VERSION)"
    echo "not committed" > loose-end

    version
    [ "$output" = "$(cat VERSION)-SNAPSHOT" ]
}

# The failure this whole scheme exists to prevent: a build that is not a release
# must not be able to call itself one.
@test "version: work after a release does not inherit that release's version" {
    git tag v0.0.1
    echo "later" > later && git add later && git commit --quiet -m later

    version
    [ "$output" != "0.0.1" ]
    [ "$output" = "$(cat VERSION)-SNAPSHOT" ]
}

# --- the guard --------------------------------------------------------------

@test "version-check: passes on an ordinary working tree" {
    run make -s version-check
    [ "$status" -eq 0 ]
}

@test "version-check: refuses a release tag that disagrees with the VERSION file" {
    git tag v9.9.9

    run make -s version-check
    [ "$status" -ne 0 ]
    [[ "$output" == *"tagged v9.9.9"* ]]
    [[ "$output" == *"$(cat VERSION)"* ]]
}

@test "version-check: a dirty tree at a mismatched tag is not a release, so it passes" {
    git tag v9.9.9
    echo "not committed" > loose-end

    run make -s version-check
    [ "$status" -eq 0 ]
}

@test "version-check: refuses a VERSION file that already carries a suffix" {
    echo "0.2.0-SNAPSHOT" > VERSION

    run make -s version-check
    [ "$status" -ne 0 ]
    [[ "$output" == *"not a plain X.Y.Z version"* ]]
}

@test "version-check: refuses an empty VERSION file" {
    : > VERSION

    run make -s version-check
    [ "$status" -ne 0 ]
    [[ "$output" == *"missing or empty"* ]]
}

# --- tagging ----------------------------------------------------------------

@test "tag: names the version being developed" {
    run make -s tag
    [ "$status" -eq 0 ]

    run git tag --list
    [ "$output" = "v$(cat VERSION)" ]
}

@test "tag: what it creates is what the next build calls itself" {
    make -s tag

    version
    [ "$output" = "$(cat VERSION)" ]
}

@test "tag: refuses a dirty tree, because a tag names a commit" {
    echo "not committed" > loose-end

    run make -s tag
    [ "$status" -ne 0 ]
    [[ "$output" == *"uncommitted changes"* ]]

    run git tag --list
    [ -z "$output" ]
}

@test "tag: refuses to tag a version that is already tagged, and says what to do" {
    make -s tag

    run make -s tag
    [ "$status" -ne 0 ]
    # git would refuse this on its own; what the guard adds is the way out.
    [[ "$output" == *"Bump VERSION"* ]]
}

# The tag is what a release is named after, so everything version-check refuses
# to build, it must refuse to name.
@test "tag: refuses a VERSION file it would not build from" {
    # Committed, so the only thing left to refuse it is the shape of the version
    # itself - an uncommitted change would be caught as a dirty tree instead.
    echo "0.2" > VERSION
    git add VERSION && git commit --quiet -m "a version that is not one"

    run make -s tag
    [ "$status" -ne 0 ]
    [[ "$output" == *"not a plain X.Y.Z version"* ]]

    run git tag --list
    [ -z "$output" ]
}

@test "tag: does not push, because that is the irreversible half" {
    run make -s tag
    [[ "$output" == *"git push origin v$(cat VERSION)"* ]]
}
