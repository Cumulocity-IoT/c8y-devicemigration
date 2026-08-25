#!/usr/bin/env bats
#
# One command, two tenants. The pre-flight is the important part: --session
# fails soft, so a mistyped target silently writes to whatever tenant is in
# scope, which for a command that creates objects is the worst thing that can
# happen here.

load helpers/load

setup() {
    setup_common
    cd "$WORK"
}

run_migrate() { run bash "$REPO_DIR/commands/migrate" "$@"; }

migrate_both() {
    run_migrate --rootId 1 --sourceSession source.json --targetSession target.json --force "$@"
}

live_objects() { ls "$REC/objects" 2>/dev/null | grep -c '' | tr -d ' '; }

# --- pre-flight --------------------------------------------------------------

@test "migrate: reports both tenants before doing anything" {
    migrate_both
    assert_success
    # hosts are shown normalised, so the two sides are comparable at a glance
    assert_output --partial "Migrating from source.example.com (t_source)"
    assert_output --partial "to target.example.com (t_target)"
    # and which file each argument actually matched, since a glob resolving to
    # the wrong tenant is the failure this exists to prevent
    assert_output --partial "source session: source.json"
    assert_output --partial "target session: target.json"
}

@test "migrate: an unresolvable source session is refused" {
    run_migrate --rootId 1 --sourceSession absent.json --targetSession target.json --force
    assert_failure
    assert_output --partial "Could not resolve the source session: absent.json"
    assert_output --partial "does not resolve"
    assert_equal "$(live_objects)" "0"
}

@test "migrate: an unresolvable target session is refused" {
    run_migrate --rootId 1 --sourceSession source.json --targetSession absent.json --force
    assert_failure
    assert_output --partial "Could not resolve the target session: absent.json"
    assert_equal "$(live_objects)" "0"
}

@test "migrate: a session name matching several files is refused, not guessed" {
    write_session other.eu-latest.json "https://other.example.com" "t_other"
    write_session third.eu-latest.json "https://third.example.com" "t_third"
    run_migrate --rootId 1 --sourceSession '*eu-latest*' --targetSession target.json --force
    assert_failure
    assert_output --partial "matches 2 files"
    assert_output --partial "other.eu-latest.json"
    assert_output --partial "third.eu-latest.json"
    # c8y itself would silently take the first
    assert_output --partial "Name one exactly"
    assert_equal "$(live_objects)" "0"
}

@test "migrate: a session file naming no tenant is refused" {
    printf '{"settings":{}}\n' > "$SESSION_HOME/empty.json"
    run_migrate --rootId 1 --sourceSession empty.json --targetSession target.json --force
    assert_failure
    assert_output --partial "names no host or tenant"
    assert_equal "$(live_objects)" "0"
}

@test "migrate: the same tenant on both sides is refused" {
    run_migrate --rootId 1 --sourceSession source.json --targetSession source.json --force
    assert_failure
    assert_output --partial "are the same tenant (t_source at source.example.com)"
    assert_output --partial "duplicate the hierarchy into the tenant it came from"
    assert_equal "$(live_objects)" "0"
}

@test "migrate: two session files for the same tenant are refused" {
    # a different file name is not a different tenant, and neither is a
    # different way of writing the same host: real session files disagree about
    # whether the scheme is included
    write_session copy.json "source.example.com/" "t_source"
    run_migrate --rootId 1 --sourceSession source.json --targetSession copy.json --force
    assert_failure
    assert_output --partial "are the same tenant"
    assert_equal "$(live_objects)" "0"
}

@test "migrate: the same tenant id on different hosts is allowed" {
    # a tenant id only identifies a tenant within one host, so refusing this
    # would block a perfectly good migration
    write_session other-host.json "https://elsewhere.example.com" "t_source"
    run_migrate --rootId 1 --sourceSession source.json --targetSession other-host.json --force
    assert_success
    refute_output --partial "same tenant"
}

@test "migrate: a session that resolves but does not authenticate is refused" {
    # an expired token or an encrypted session file with no passphrase
    FAKE_C8Y_UNAUTH_SESSION=target.json migrate_both
    assert_failure
    assert_output --partial "target session does not authenticate"
    assert_output --partial "C8Y_PASSPHRASE"
    assert_equal "$(live_objects)" "0"
}

@test "migrate: credentials are checked before any object is created" {
    FAKE_C8Y_UNAUTH_SESSION=source.json migrate_both
    assert_failure
    # nothing was even read from the source
    assert_equal "$(count_calls "inventory get")" "0"
}

# --- confirmation ------------------------------------------------------------

@test "migrate: declining the confirmation migrates nothing" {
    run bash -c "echo no | bash '$REPO_DIR/commands/migrate' \\
        --rootId 1 --sourceSession source.json --targetSession target.json 2>&1"
    assert_failure
    assert_output --partial "Aborted, nothing was migrated"
    assert_equal "$(live_objects)" "0"
}

@test "migrate: a closed stdin is a refusal, not consent" {
    run bash -c "bash '$REPO_DIR/commands/migrate' \\
        --rootId 1 --sourceSession source.json --targetSession target.json < /dev/null 2>&1"
    assert_failure
    assert_output --partial "pass --force to migrate without being asked"
    assert_equal "$(live_objects)" "0"
}

@test "migrate: --force skips the confirmation" {
    migrate_both
    assert_success
    refute_output --partial 'Type "yes"'
}

# --- the migration itself ----------------------------------------------------

@test "migrate: one command produces the same result as export then import" {
    migrate_both
    assert_success
    assert_equal "$(jq 'length' id_mapping.json)" "9"
    assert_equal "$(live_objects)" "9"
    # relationships rebuilt, as the two-command path does
    assert_equal "$(count_links)" "10"
}

@test "migrate: each side talks to its own tenant" {
    migrate_both
    assert_success
    # reads go to the source tenant, writes to the target tenant
    assert_session_on_calls "source.json" "inventory get"
    assert_session_on_calls "target.json" "inventory create"
    assert_session_on_calls "source.json" "inventory children list"
    assert_session_on_calls "target.json" "inventory children assign"
}

@test "migrate: data types are passed through to the export" {
    migrate_both --dataTypes measurements,events,alarms
    assert_success
    assert [ "$(count_calls "measurements list")" -gt 0 ]
    assert [ "$(count_calls "measurements createBulk")" -gt 0 ]
}

@test "migrate: --no-children migrates only the root" {
    migrate_both --no-children
    assert_success
    assert_equal "$(jq 'length' id_mapping.json)" "1"
    assert_equal "$(count_links)" "0"
}

# --- the marker fragment -----------------------------------------------------

@test "migrate: created objects carry the run marker by default" {
    migrate_both
    assert_success
    assert_output --regexp 'Run id [0-9]{8}T[0-9]{6}Z_[0-9a-f]{6}'

    local marker
    marker=$(jq -r '[keys[] | select(startswith("c8y_DeviceMigration_"))][0]' \
        "$REC"/payloads/1001.json)
    assert [ -n "$marker" ]
    # the identity is in the NAME and the body is empty, so one --fragmentType
    # query finds a whole run
    assert_equal "$(jq -c --arg m "$marker" '.[$m]' "$REC"/payloads/1001.json)" "{}"
}

@test "migrate: the marker name uses only fragment-safe characters" {
    migrate_both
    assert_success
    local marker
    marker=$(jq -r '[keys[] | select(startswith("c8y_DeviceMigration_"))][0]' \
        "$REC"/payloads/1001.json)
    # Cumulocity rejects whitespace and special characters in fragment names
    assert_equal "$(printf '%s' "$marker" | grep -c '^[A-Za-z0-9_]*$')" "1"
}

@test "migrate: every created object carries the same marker" {
    migrate_both
    assert_success
    local markers
    markers=$(jq -r '[keys[] | select(startswith("c8y_DeviceMigration_"))][0]' \
        "$REC"/payloads/*.json | sort -u | grep -c '')
    assert_equal "$markers" "1"
}

@test "migrate: --noFlag leaves the objects unmarked" {
    migrate_both --noFlag
    assert_success
    refute_output --partial "Run id"
    assert_equal "$(jq -r '[keys[] | select(startswith("c8y_DeviceMigration_"))] | length' \
        "$REC"/payloads/1001.json)" "0"
}

# --- staging -----------------------------------------------------------------

@test "migrate: the staging directory is removed on success" {
    # A private TMPDIR: scanning the shared one would see staging directories
    # belonging to other tests running in parallel. migrate stages with an
    # explicit mktemp template, so it honours this.
    local privateTmp="$BATS_TEST_TMPDIR/tmp"
    mkdir -p "$privateTmp"
    export TMPDIR="$privateTmp"

    migrate_both
    assert_success
    refute_output --partial "Staging directory kept"
    assert_equal "$(ls "$privateTmp" | grep -c '' || true)" "0"
}

@test "migrate: --keepStaging keeps it and says where" {
    migrate_both --keepStaging
    assert_success
    assert_output --partial "Staging directory kept at"
    local kept
    kept=$(printf '%s\n' "$output" | sed -n 's/.*Staging directory kept at //p')
    assert [ -d "$kept" ]
    assert [ -f "$kept/export/exported_device_1.json" ]
    rm -rf "$kept"
}

@test "migrate: the closing message says how to reverse it" {
    migrate_both
    assert_success
    assert_output --partial "Migration complete. Reverse it with:"
    assert_output --partial "revert --mapping id_mapping.json --session $SESSION_HOME/target.json"
    assert_output --regexp 'revert --runId [0-9]{8}T[0-9]{6}Z_[0-9a-f]{6}'
}

@test "migrate: --help documents the session forms" {
    run_migrate --help
    assert_success
    assert_output --partial "A bare name without the extension does not resolve"
}

# --- failing part way through ------------------------------------------------
#
# The case migrate exists to make survivable. A part-finished migration has
# created objects in the target tenant, so what the operator needs at that
# moment is how to undo them - which is exactly when the success path does not
# run.

@test "migrate: a failure part way through says how to revert" {
    # the fake refuses to create this one object, so the import stops with some
    # objects already created
    FAKE_C8Y_FAIL_CREATE_NAME="Line B" migrate_both
    assert_failure
    assert_output --partial "The migration stopped part way through"
    assert_output --partial "c8y devicemigration revert --mapping id_mapping.json --session $SESSION_HOME/target.json"
    # the mapping really does list what was created, so the hint is actionable
    assert [ "$(jq 'length' id_mapping.json)" -gt 0 ]
}

@test "migrate: the revert hint after a failure offers the run id too" {
    FAKE_C8Y_FAIL_CREATE_NAME="Line B" migrate_both
    assert_failure
    assert_output --partial "--session $SESSION_HOME/target.json"
    assert_output --regexp 'revert --runId [0-9]{8}T[0-9]{6}Z_[0-9a-f]{6}'
}

@test "migrate: a failure keeps the staging directory" {
    local privateTmp="$BATS_TEST_TMPDIR/tmp"
    mkdir -p "$privateTmp"
    export TMPDIR="$privateTmp"

    FAKE_C8Y_FAIL_CREATE_NAME="Line B" migrate_both
    assert_failure
    assert_output --partial "Staging directory kept at"
    # retrying should not have to read the source tenant again
    assert_equal "$(ls "$privateTmp" | grep -c '' || true)" "1"
}

@test "migrate: a failure before anything is created offers no revert" {
    # an unresolvable target session fails the pre-flight, so nothing exists to
    # undo and a revert hint would only mislead
    run_migrate --rootId 1 --sourceSession source.json --targetSession absent.json --force
    assert_failure
    refute_output --partial "stopped part way through"
}

@test "migrate: an incremental failure part way through says how to revert" {
    FAKE_C8Y_FAIL_CREATE_NAME="Line B" migrate_both --incremental
    assert_failure
    assert_output --partial "The migration stopped part way through"
}

# --- the session the pre-flight resolved is the one that gets used -----------

@test "migrate: every call uses the resolved session file, not the argument" {
    # A glob is resolved once, up front, and displayed. If the children resolved
    # it again independently they could land on a different file, which for the
    # target side means creating objects in the wrong tenant.
    write_session only-match.eu-latest.json "https://only.example.com" "t_only"
    run_migrate --rootId 1 --sourceSession source.json \
        --targetSession '*eu-latest*' --force
    assert_success
    assert_output --partial "target session: only-match.eu-latest.json"
    assert_session_on_calls "only-match.eu-latest.json" "inventory create"
}

@test "migrate: no c8y call consumes stdin" {
    # the confirmation is read from stdin, so a call without --nullInput would
    # swallow the answer - which is exactly what settings list did once
    migrate_both
    assert_success
    assert_no_call_drained_stdin
}
