#!/usr/bin/env bats
#
# revert deletes what an import created, working from the id mapping alone.
# Confirmation is c8y's own dialog, so what these tests pin down is whether
# revert hands the decision over correctly: it must not pass --force unless
# asked, and it must batch the deletions into one call so c8y's "Yes to All"
# covers the whole set instead of asking once per object.

load helpers/load

setup() {
    setup_common
    cd "$WORK"
}

run_revert() { run bash "$REPO_DIR/commands/revert" "$@"; }

# Import the whole fixture tree, leaving a mapping and a populated tenant.
do_full_import() {
    # Memoised: the export and import are identical for every test that calls
    # this, and each caller gets a private copy of the result, which matters
    # because these tests then delete the objects the import created.
    shared_migration --rootId 1 --output-directory "$OUT"
    [ "$status" -eq 0 ]
}

live_objects() { ls "$REC/objects" 2>/dev/null | grep -c '' | tr -d ' '; }

# --- the plan ---------------------------------------------------------------

@test "revert: lists every object before deleting anything" {
    do_full_import
    FAKE_C8Y_CONFIRM=no run_revert --mapping id_mapping.json
    assert_output --partial "The following managed objects will be deleted"
    assert_output --partial "9 managed objects will be deleted, 0 are already absent."
    assert_output --regexp 'delete Plant Berlin \([0-9]+, imported from 1\)'
}

@test "revert: the plan warns that group deletion can reach other objects" {
    do_full_import
    run_revert --mapping id_mapping.json --force
    assert_success
    assert_output --partial "Deleting a group also propagates to its subgroups"
}

# --- confirmation is c8y's ---------------------------------------------------

@test "revert: without --force the confirmation is left to c8y" {
    do_full_import
    FAKE_C8Y_CONFIRM=a run_revert --mapping id_mapping.json
    assert_success
    # passing --force here would rob c8y of the chance to ask
    assert_flag_on_no_calls "--force" "inventory delete"
    assert_equal "$(live_objects)" "0"
}

@test "revert: the confirmation dialog reaches the terminal" {
    do_full_import
    # c8y writes its prompt to stderr. If revert redirects stderr the prompt is
    # invisible and c8y sits waiting on the terminal, which looks like a hang.
    FAKE_C8Y_CONFIRM=a run_revert --mapping id_mapping.json
    assert_success
    assert_output --partial "Yes to All"
}

@test "revert: --force does not render a dialog" {
    do_full_import
    run_revert --mapping id_mapping.json --force
    assert_success
    refute_output --partial "Yes to All"
}

@test "revert: declining c8y's confirmation deletes nothing" {
    do_full_import
    local before
    before=$(live_objects)
    FAKE_C8Y_CONFIRM=no run_revert --mapping id_mapping.json
    assert_failure
    assert_output --partial "Nothing was deleted"
    assert_output --partial "The revert is incomplete"
    assert_equal "$(live_objects)" "$before"
    refute [ -e "$REC/deletes.log" ]
}

@test "revert: answering yes to all removes every object" {
    do_full_import
    FAKE_C8Y_CONFIRM=a run_revert --mapping id_mapping.json
    assert_success
    assert_output --partial "Deleted 9 of 9 managed objects, 0 are still present"
    assert_equal "$(live_objects)" "0"
}

@test "revert: every deletion goes to one c8y call, so yes-to-all covers them" {
    do_full_import
    FAKE_C8Y_CONFIRM=a run_revert --mapping id_mapping.json
    assert_success
    assert_equal "$(count_calls "inventory delete")" "1"
    # that single call carries all nine ids
    local idCount
    idCount=$(grep '^inventory delete' "$REC/calls.log" | head -1 | tr ' ' '\n' | grep -c '^--id$')
    assert_equal "$idCount" "9"
}

@test "revert: --force passes through so c8y does not ask" {
    do_full_import
    # no FAKE_C8Y_CONFIRM: --force must make the dialog moot
    run_revert --mapping id_mapping.json --force
    assert_success
    assert_flag_on_all_calls "--force" "inventory delete"
    assert_equal "$(live_objects)" "0"
}

# --- partial imports, the case this exists for ------------------------------

@test "revert: cleans up after an import that died part way through" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success

    FAKE_C8Y_FAIL_CREATE_NAME="Line B" run_import --input-directory "$OUT" --force
    assert_failure

    local created
    created=$(jq 'length' id_mapping.json)
    assert [ "$created" -gt 0 ]
    assert [ "$created" -lt 9 ]
    assert_equal "$(live_objects)" "$created"

    run_revert --mapping id_mapping.json --force
    assert_success
    assert_equal "$(live_objects)" "0"
    assert_output --partial "Deleted $created of $created managed objects"
}

@test "revert: an import that failed on its first object leaves an empty mapping" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    FAKE_C8Y_FAIL_CREATE=1 run_import --input-directory "$OUT" --force
    assert_failure
    assert_equal "$(jq -c '.' id_mapping.json)" "{}"

    run_revert --mapping id_mapping.json --force
    assert_success
    assert_output --partial "nothing to revert"
}

# --- objects that are already gone -----------------------------------------

@test "revert: objects deleted outside this run are reported as absent" {
    do_full_import
    local gone
    gone=$(jq -r '.["6"]' id_mapping.json)
    rm "$REC/objects/$gone.json"

    run_revert --mapping id_mapping.json --force
    assert_success
    assert_output --partial "8 managed objects will be deleted, 1 are already absent."
    assert_output --partial "Deleted 8 of 8 managed objects"
    assert_equal "$(live_objects)" "0"
}

@test "revert: objects taken by propagation still count as deleted" {
    do_full_import
    local root childA childB
    root=$(jq -r '.["1"]' id_mapping.json)
    childA=$(jq -r '.["6"]' id_mapping.json)
    childB=$(jq -r '.["7"]' id_mapping.json)

    FAKE_C8Y_CASCADE="$root:$childA,$childB" run_revert --mapping id_mapping.json --force
    assert_success
    assert_output --partial "Deleted 9 of 9 managed objects, 0 are still present"
    assert_equal "$(live_objects)" "0"
}

# --- expected 404s ----------------------------------------------------------
#
# Cumulocity propagates a group deletion to its subgroups, so by the time an
# object's own delete request is sent it may already be gone. That produces a
# 404 the operator has no use for.

@test "revert: tells c8y that a 404 on delete is not worth reporting" {
    do_full_import
    run_revert --mapping id_mapping.json --force
    assert_success
    assert_flag_on_all_calls "--silentStatusCodes" "inventory delete"
    assert_flag_on_all_calls "--silentExit" "inventory delete"
}

@test "revert: propagation produces no error noise" {
    do_full_import
    local root childA childB
    root=$(jq -r '.["1"]' id_mapping.json)
    childA=$(jq -r '.["6"]' id_mapping.json)
    childB=$(jq -r '.["7"]' id_mapping.json)

    FAKE_C8Y_CASCADE="$root:$childA,$childB" run_revert --mapping id_mapping.json --force
    assert_success
    refute_output --partial "404"
    refute_output --partial "serverError"
    refute_output --partial "Not Found"
    assert_output --partial "Deleted 9 of 9 managed objects, 0 are still present"
}

@test "revert: c8y's own errors are surfaced when an object survives" {
    do_full_import
    local stubborn
    stubborn=$(jq -r '.["4"]' id_mapping.json)

    FAKE_C8Y_FAIL_DELETE="$stubborn" run_revert --mapping id_mapping.json --force
    assert_failure
    # the reason has to reach the operator, not just the symptom
    assert_output --partial "refusing to delete"
    assert_output --partial "Still present:"
}

# --- failures --------------------------------------------------------------

@test "revert: an object that cannot be deleted is reported and fails the run" {
    do_full_import
    local stubborn
    stubborn=$(jq -r '.["4"]' id_mapping.json)

    FAKE_C8Y_FAIL_DELETE="$stubborn" run_revert --mapping id_mapping.json --force
    assert_failure
    assert_output --partial "Still present:"
    assert_output --partial "Deleted 8 of 9 managed objects, 1 are still present"
    assert_output --partial "rerun it to retry"
    assert_equal "$(live_objects)" "1"
}

# --- input validation -----------------------------------------------------

@test "revert: --mapping is required" {
    run_revert --force
    assert_failure
}

@test "revert: a missing mapping file fails cleanly" {
    run_revert --mapping "$WORK/nope.json" --force
    assert_failure
    assert_output --partial "Mapping file does not exist"
}

@test "revert: a mapping file that is not a JSON object is rejected" {
    printf '["not", "an", "object"]\n' > "$WORK/bad.json"
    run_revert --mapping "$WORK/bad.json" --force
    assert_failure
    assert_output --partial "not a JSON object"
}

@test "revert: an empty mapping has nothing to do" {
    printf '{}\n' > "$WORK/empty.json"
    run_revert --mapping "$WORK/empty.json" --force
    assert_success
    assert_output --partial "nothing to revert"
}

@test "revert: no c8y call consumes stdin" {
    do_full_import
    run_revert --mapping id_mapping.json --force
    assert_success
    assert_no_call_drained_stdin
}

@test "revert: every delete passes --nullInput" {
    do_full_import
    run_revert --mapping id_mapping.json --force
    assert_success
    assert_flag_on_all_calls "--nullInput" "inventory delete"
}

@test "revert: --help succeeds, proving the committed parser matches its DOC" {
    run_revert --help
    assert_success
    assert_output --partial "Revert an import by deleting the managed objects it created"
}

@test "revert: raw managed objects are not printed while planning" {
    do_full_import
    FAKE_C8Y_CONFIRM=no run_revert --mapping id_mapping.json
    assert_failure
    refute_output --partial '"lastUpdated"'
    refute_output --partial '"my_Custom-Fragment"'
}

# --- reverting by run marker -------------------------------------------------
#
# migrate tells the operator that a run can be reverted with --runId if the
# mapping is lost, so this has to actually work. The marker body is empty by
# design, so the fragment says which run created an object and nothing else.

# An import carrying a run marker, as migrate does
do_marked_import() {
    run_export --rootId 1 --output-directory "$OUT"
    [ "$status" -eq 0 ]
    run_import --input-directory "$OUT" --force --runId TESTRUN_1
    [ "$status" -eq 0 ]
}

@test "revert: --runId plans the objects that carry the marker" {
    do_marked_import
    FAKE_C8Y_CONFIRM=no run_revert --runId TESTRUN_1
    assert_output --partial "The following managed objects will be deleted"
    assert_output --partial "9 managed objects will be deleted, 0 are already absent."
    # no source id to report: the marker records the run, not the origin.
    # assert_line, not assert_output: the latter matches the whole output as one
    # string, so the end-of-line anchor would not mean what it looks like.
    assert_line --regexp '^  delete Plant Berlin \([0-9]+\)$'
}

@test "revert: --runId needs no mapping file at all" {
    do_marked_import
    rm -f id_mapping.json
    run_revert --runId TESTRUN_1 --force
    assert_success
    assert_equal "$(live_objects)" "0"
}

@test "revert: --runId deletes that run's objects and leaves another run's alone" {
    do_marked_import
    # the same export imported again, marked as a different run
    run_import --input-directory "$OUT" --force --runId TESTRUN_2 --mapping second.json
    [ "$status" -eq 0 ]
    assert_equal "$(live_objects)" "18"

    run_revert --runId TESTRUN_1 --force
    assert_success
    assert_output --partial "Deleted 9 of 9 managed objects"
    assert_equal "$(live_objects)" "9"
    # what survived is the second run, still marked as such
    assert_equal "$(grep -l 'c8y_DeviceMigration_TESTRUN_2' "$REC"/objects/*.json | grep -c '')" "9"
}

@test "revert: an unknown runId has nothing to do and deletes nothing" {
    do_marked_import
    run_revert --runId TESTRUN_ABSENT --force
    assert_success
    assert_output --partial "No managed object carries c8y_DeviceMigration_TESTRUN_ABSENT"
    assert_equal "$(live_objects)" "9"
}

@test "revert: --runId cannot reach objects imported without a marker" {
    # an import run with --noFlag leaves nothing to find, which is worth saying
    # out loud rather than discovering during an incident
    do_full_import
    run_revert --runId TESTRUN_1 --force
    assert_success
    assert_output --partial "nothing to revert"
    assert_equal "$(live_objects)" "9"
}

@test "revert: a runId that is not fragment safe is refused" {
    do_marked_import
    run_revert --runId 'bad id' --force
    assert_failure
    assert_output --partial "letters, digits and underscores"
    assert_equal "$(live_objects)" "9"
}

@test "revert: neither --mapping nor --runId is refused" {
    run_revert --force
    assert_failure
}
