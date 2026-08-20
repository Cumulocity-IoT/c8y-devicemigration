#!/usr/bin/env bash
# Shared setup for every .bats file.

TESTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_DIR="$(cd "$TESTS_DIR/.." && pwd)"

load "$TESTS_DIR/vendor/bats-support/load.bash"
load "$TESTS_DIR/vendor/bats-assert/load.bash"

setup_common() {
    WORK="$BATS_TEST_TMPDIR/work"
    REC="$BATS_TEST_TMPDIR/rec"
    # shellcheck disable=SC2034  # read by the .bats files that load this helper
    OUT="$WORK/out"
    mkdir -p "$WORK" "$REC"

    export FAKE_C8Y_REC="$REC"
    export FAKE_C8Y_HIERARCHY="$TESTS_DIR/fixtures/hierarchy.json"
    # the fake shadows any real c8y for the duration of the test
    PATH="$TESTS_DIR/fake:$PATH"
    export PATH
}

# Run the commands under test. Kept as functions so every test goes through the
# same entry point and no test accidentally invokes an installed extension.
run_export() { run bash "$REPO_DIR/commands/export" "$@"; }
run_import() { run bash "$REPO_DIR/commands/import" "$@"; }

# --- assertions over the export output --------------------------------------

# ids of every exported managed object, sorted numerically
exported_ids() {
    local dir="$1" f base
    for f in "$dir"/exported_device_*.json; do
        [[ -e "$f" ]] || continue
        base=$(basename "$f" .json)
        printf '%s\n' "${base#exported_device_}"
    done | sort -n | tr '\n' ' ' | sed 's/ $//'
}

# every parent->child reference in an export directory, as "parent type child"
export_refs() {
    local dir="$1" f base parent childType child
    for f in "$dir"/*_child_*.json; do
        [[ -e "$f" ]] || continue
        base=$(basename "$f" .json)
        parent=${base%%_child_*}
        childType=${base##*_child_}
        while IFS= read -r child; do
            [[ -n "$child" ]] || continue
            printf '%s %s %s\n' "$parent" "$childType" "$child"
        done < <(jq -r '.[]' "$f")
    done | sort
}

count_refs() { export_refs "$1" | grep -c '' | tr -d ' '; }

# --- assertions over the recorded c8y calls ---------------------------------

# links actually assigned, as "parent type child", sorted
assigned_links() {
    sort "$REC/links.log" 2>/dev/null || true
}

count_links() { assigned_links | grep -c '' | tr -d ' '; }

# The invariant that guards the stdin-consumption bug generically: no call that
# runs inside a loop may ever have read stdin.
assert_no_call_drained_stdin() {
    local drained
    drained=$(awk -F'\t' '$2 == "yes" { print $1 }' "$REC/calls.log" 2>/dev/null || true)
    if [[ -n "$drained" ]]; then
        printf 'these c8y calls consumed stdin (missing --nullInput):\n%s\n' "$drained" >&2
        return 1
    fi
}

# Number of recorded calls to a given subcommand prefix.
count_calls() {
    if [[ ! -f "$REC/calls.log" ]]; then
        echo 0
        return 0
    fi
    grep -c "^$1" "$REC/calls.log" | tr -d ' '
}

# How many times a single managed object was fetched. A device with several
# parents, or one on a cycle, must be fetched exactly once.
count_gets_for_id() {
    awk -F'\t' -v id="$1" \
        '$1 ~ /^inventory get/ && $3 ~ (" --id " id " ") { n++ } END { print n+0 }' \
        "$REC/calls.log"
}

assert_flag_on_all_calls() {
    local flag="$1" subcommand="$2" missing
    missing=$(awk -F'\t' -v s="$subcommand" -v f="$flag" \
        'index($1, s) == 1 && index($3, f) == 0 { print $1 }' "$REC/calls.log")
    if [[ -n "$missing" ]]; then
        printf '%s calls missing %s:\n%s\n' "$subcommand" "$flag" "$missing" >&2
        return 1
    fi
}

assert_flag_on_no_calls() {
    local flag="$1" subcommand="$2" present
    present=$(awk -F'\t' -v s="$subcommand" -v f="$flag" \
        'index($1, s) == 1 && index($3, f) > 0 { print $1 }' "$REC/calls.log")
    if [[ -n "$present" ]]; then
        printf '%s calls unexpectedly carry %s:\n%s\n' "$subcommand" "$flag" "$present" >&2
        return 1
    fi
}
