#!/usr/bin/env bash
# Shared setup for every .bats file.

TESTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_DIR="$(cd "$TESTS_DIR/.." && pwd)"

# The real c8y, resolved before any test shadows it with the fake. `c8y template
# execute` evaluates a jsonnet template with no session and no network, which is
# the only way to check what a --template actually PRODUCES rather than what it
# says. Tests using it skip when c8y is not installed, so the suite stays
# hermetic in CI.
REAL_C8Y="$(command -v c8y 2>/dev/null || true)"

# Evaluates a template against one record and prints the resulting body.
evaluate_template() {
    local template="$1" record="$2"
    printf '%s\n' "$record" | "$REAL_C8Y" template execute --template "$template"
}

require_real_c8y() {
    if [[ -z "$REAL_C8Y" ]]; then
        skip "c8y is not installed, so templates cannot be evaluated"
    fi
}

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

# --- memoised migrations ------------------------------------------------------
#
# Most tests re-run an identical export or import purely to assert a different
# property of the result. bats runs the tests of one file sequentially in one
# process, so the first test needing a given invocation can run it and cache the
# result under $BATS_FILE_TMPDIR for the rest of the file.
#
# A caller gets a private copy, so a test that mutates its output cannot affect
# any other. $output, $status and $lines are populated exactly as `run` leaves
# them, so no assertion has to change.

# The identity of a run: its arguments AND the environment the fake reads.
#
# Two mistakes are easy here and both were made once:
#
#   * per-test paths must be normalised out. $OUT and $WORK live under
#     $BATS_TEST_TMPDIR, which is unique per test, so leaving them in makes
#     every key unique and the cache never hits at all.
#   * the FAKE_C8Y_* environment must be included. Two invocations with
#     identical arguments but a different hierarchy fixture or a different
#     injected failure are NOT the same run, and sharing between them returns a
#     confidently wrong result. FAKE_C8Y_REC is excluded because it is per test
#     by construction.
_cache_key() {
    local raw="$*" env

    raw=${raw//$BATS_TEST_TMPDIR/TESTTMP}
    env=$(printenv | grep '^FAKE_C8Y_' | grep -v '^FAKE_C8Y_REC=' \
        | LC_ALL=C sort | tr '\n' ' ')
    env=${env//$BATS_TEST_TMPDIR/TESTTMP}

    printf '%s %s' "$raw" "$env" | shasum -a 256 | cut -c1-16
}

# Rebuilds $output/$status/$lines from a cache entry, rewriting the paths the
# cached run used so assertions against $OUT and $WORK still match.
_restore_from_cache() {
    local cache="$1" cachedWork line

    # Clear the CONTENTS, do not remove the directories. Several tests cd into
    # $WORK, and deleting it out from under the shell leaves it with an invalid
    # working directory - "getcwd: cannot access parent directories" - after
    # which every relative path in the test fails.
    find "$WORK" -mindepth 1 -delete 2>/dev/null || true
    find "$REC" -mindepth 1 -delete 2>/dev/null || true
    cp -R "$cache/work/." "$WORK/"
    cp -R "$cache/rec/." "$REC/"

    cachedWork=$(cat "$cache/workpath")
    status=$(cat "$cache/status")
    output=$(cat "$cache/output")
    output=${output//$cachedWork/$WORK}

    lines=()
    while IFS= read -r line; do
        lines+=("$line")
    done <<< "$output"
}

# Built aside and moved into place, so a reader never sees a half written entry.
# Under `bats -j` two tests can miss the same key at once: both do the work and
# one wins the rename, which costs a duplicated run but can never corrupt.
_save_to_cache() {
    local cache="$1" staging="$1.incomplete.$$"

    mkdir -p "$staging"
    cp -R "$WORK" "$staging/work"
    cp -R "$REC" "$staging/rec"
    printf '%s' "$WORK" > "$staging/workpath"
    printf '%s' "$status" > "$staging/status"
    printf '%s' "$output" > "$staging/output"

    if ! mv "$staging" "$cache" 2>/dev/null; then
        # another test got there first, which is fine
        rm -rf "$staging"
    fi
}

# Same contract as run_export, but only actually exports once per argument list.
shared_export() {
    local cache
    cache="$BATS_FILE_TMPDIR/export.$(_cache_key "$@")"

    if [[ -d "$cache" ]]; then
        _restore_from_cache "$cache"
        return 0
    fi
    run_export "$@"
    _save_to_cache "$cache"
}

# Same, for an export followed by an import of it. Callers that then mutate the
# target tenant (every revert test) get their own copy of the fake's state.
shared_migration() {
    local cache
    cache="$BATS_FILE_TMPDIR/migration.$(_cache_key "$@")"

    if [[ -d "$cache" ]]; then
        _restore_from_cache "$cache"
        return 0
    fi
    run_export "$@"
    [[ "$status" -eq 0 ]] || return 0
    run_import --input-directory "$OUT" --force
    _save_to_cache "$cache"
}

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

# Commands whose records legitimately arrive on stdin. --device is the pipeline
# target on every create command, so piping is how the data import achieves
# throughput. Everything else reading stdin is the bug this guards against.
DATA_PIPELINE_SUBCOMMANDS='^measurements createBulk '

# The invariant that guards the stdin-consumption bug generically: no call that
# runs inside a loop may ever have read stdin. Scoped, not weakened - the data
# create commands are exempt by name and everything else still fails.
assert_no_call_drained_stdin() {
    local drained
    drained=$(awk -F'\t' -v ok="$DATA_PIPELINE_SUBCOMMANDS" \
        '$2 == "yes" && $1 !~ ok { print $1 }' "$REC/calls.log" 2>/dev/null || true)
    if [[ -n "$drained" ]]; then
        printf 'these c8y calls consumed stdin (missing --nullInput):\n%s\n' "$drained" >&2
        return 1
    fi
}

# The other half of that invariant: the data commands must actually be fed.
assert_data_command_received_stdin() {
    local subcommand="$1" fed
    fed=$(awk -F'\t' -v s="$subcommand" 'index($1, s) == 1 && $2 == "yes"' \
        "$REC/calls.log" | grep -c '' || true)
    if [[ "$fed" -eq 0 ]]; then
        printf '%s was never given records on stdin\n' "$subcommand" >&2
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

# jsonnet field definitions must be quoted. `self` is a jsonnet keyword, so a
# bare `self::` is a parse error and the whole import fails at the first record;
# `error`, `super`, `local`, `function`, `import` and `assert` are keywords too.
# Quoting every field name is what makes that unreachable, so assert it.
assert_template_fields_quoted() {
    local template="$1" bare
    bare=$(printf '%s' "$template" | grep -oE "[{,] *[A-Za-z_][A-Za-z0-9_]*::" || true)
    if [[ -n "$bare" ]]; then
        printf 'template has unquoted field names, which breaks on jsonnet keywords:\n%s\nin: %s\n' \
            "$bare" "$template" >&2
        return 1
    fi
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
