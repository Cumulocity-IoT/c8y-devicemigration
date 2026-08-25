#!/usr/bin/env bats
#
# Level 3: an interrupted migration is finished by running it again.
#
# The mapping is already the record for objects, since it is written after every
# create. The gap resume had to fill is data: nothing otherwise says whether a
# device's measurements, events and alarms arrived. That record is the journal,
# and the properties worth defending are that a resume creates no duplicate
# objects, no duplicate relationships, and loses no data - and that it refuses
# rather than guesses when it cannot know where a run stopped.

load helpers/load

setup() {
    setup_common
    cd "$WORK"
}

run_migrate() { run bash "$REPO_DIR/commands/migrate" "$@"; }

migrate_incremental() {
    run_migrate --rootId 1 --sourceSession source.json --targetSession target.json \
        --force --incremental "$@"
}

live_objects() { ls "$REC/objects" 2>/dev/null | grep -c '' | tr -d ' '; }
journal() { cat id_mapping.json.journal; }
run_id() { sed -n 's/^runId //p' id_mapping.json.journal; }
duplicate_links() { sort "$REC/links.log" | uniq -d | grep -c '' | tr -d ' '; }

# A run that dies while creating the hierarchy
interrupted_hierarchy() {
    FAKE_C8Y_FAIL_CREATE_NAME="Line B" migrate_incremental "$@"
    assert_failure
}

# A run that dies part way through one device's data. The fake lets a number of
# data creates through and then refuses, which lands the failure inside a device
# rather than neatly between two of them.
interrupted_data() {
    FAKE_C8Y_FAIL_DATA_AFTER="$1" migrate_incremental --dataTypes measurements,events,alarms
    assert_failure
}

# --- the journal -------------------------------------------------------------

@test "resume: an incremental run records its id in a journal" {
    migrate_incremental
    assert_success
    assert [ -f id_mapping.json.journal ]
    assert_regex "$(run_id)" '^[0-9]{8}T[0-9]{6}Z_[0-9a-f]{6}$'
}

@test "resume: a chained run writes no journal" {
    # it imports all the data in one step, so there is no point at which it
    # could honestly claim a device was finished
    run_migrate --rootId 1 --sourceSession source.json --targetSession target.json --force
    assert_success
    refute [ -f id_mapping.json.journal ]
}

@test "resume: every device that finished is journalled" {
    migrate_incremental --dataTypes measurements,events,alarms
    assert_success
    assert_equal "$(journal | grep -c '^data ')" "9"
}

@test "resume: a device is journalled only after its data is in the target" {
    # the failure lands inside device 2's data, so device 2 must NOT be recorded
    interrupted_data 2
    assert_equal "$(journal | grep -c '^data 2$')" "0"
    # the devices that finished before it are
    assert_equal "$(journal | grep -c '^data 1$')" "1"
    # and it is known to have been the one in flight
    assert_equal "$(journal | grep -c '^started 2$')" "1"
}

# --- resuming ----------------------------------------------------------------

@test "resume: a run interrupted in the hierarchy is completed" {
    interrupted_hierarchy
    assert [ "$(live_objects)" -lt 9 ]

    run_migrate --rootId 1 --sourceSession source.json --targetSession target.json \
        --force --resume "$(run_id)"
    assert_success
    assert_equal "$(live_objects)" "9"
    assert_equal "$(jq 'length' id_mapping.json)" "9"
    assert_equal "$(count_links)" "10"
    assert_equal "$(duplicate_links)" "0"
}

@test "resume: the objects that already existed are not created again" {
    interrupted_hierarchy
    local before
    before=$(live_objects)

    run_migrate --rootId 1 --sourceSession source.json --targetSession target.json \
        --force --resume "$(run_id)"
    assert_success
    assert_output --partial "Already imported Plant Berlin"
    # exactly the missing ones were created, no second copy of anything
    assert_equal "$(live_objects)" "9"
    assert [ "$before" -lt 9 ]
}

@test "resume: the run id is kept, so one marker still covers the whole run" {
    interrupted_hierarchy
    local id
    id=$(run_id)

    run_migrate --rootId 1 --sourceSession source.json --targetSession target.json \
        --force --resume "$id"
    assert_success
    # every object carries the same marker, whichever attempt created it
    assert_equal "$(grep -l "c8y_DeviceMigration_$id" "$REC"/objects/*.json | grep -c '')" "9"
}

@test "resume: a device whose data already arrived is skipped" {
    interrupted_data 2
    run_migrate --rootId 1 --sourceSession source.json --targetSession target.json \
        --force --dataTypes measurements,events,alarms --resume "$(run_id)"
    assert_success
    assert_output --partial "data already migrated for (1)"
}

@test "resume: the device that was in flight is named" {
    interrupted_data 2
    run_migrate --rootId 1 --sourceSession source.json --targetSession target.json \
        --force --dataTypes measurements,events,alarms --resume "$(run_id)"
    assert_success
    # its records are delivered again, so saying which device that is beats
    # leaving it as a general caveat
    assert_output --partial "Run stopped while migrating data for (2)"
    assert_output --partial "can end up with duplicates"
}

@test "resume: no data is lost across an interruption" {
    interrupted_data 2
    run_migrate --rootId 1 --sourceSession source.json --targetSession target.json \
        --force --dataTypes measurements,events,alarms --resume "$(run_id)"
    assert_success
    # the fixture's whole dataset is present, whatever the interruption cost in
    # duplicates
    assert_output --partial "Imported 3 measurements for Line A"
    assert_output --partial "Imported 2 events for Line A"
    assert_output --partial "Imported 3 alarms for Line A"
    assert_equal "$(journal | grep -c '^data ')" "9"
}

@test "resume: resuming a finished run changes nothing" {
    migrate_incremental --dataTypes measurements,events,alarms
    assert_success
    local objects links
    objects=$(live_objects)
    links=$(count_links)

    run_migrate --rootId 1 --sourceSession source.json --targetSession target.json \
        --force --dataTypes measurements,events,alarms --resume "$(run_id)"
    assert_success
    assert_equal "$(live_objects)" "$objects"
    assert_equal "$(count_links)" "$links"
    assert_equal "$(duplicate_links)" "0"
    refute_output --partial "Run stopped while migrating"
}

# --- an object deleted behind our back --------------------------------------

@test "resume: an object deleted from the target is created again" {
    migrate_incremental
    assert_success
    local gone
    gone=$(jq -r '."4"' id_mapping.json)
    rm "$REC/objects/$gone.json"

    run_migrate --rootId 1 --sourceSession source.json --targetSession target.json \
        --force --resume "$(run_id)"
    assert_success
    assert_output --partial "is gone from the target, it will be created again"
    # the mapping now points at the new object rather than the deleted one
    assert [ "$(jq -r '."4"' id_mapping.json)" != "$gone" ]
    assert_equal "$(live_objects)" "9"
}

@test "resume: with no marker the mapping is taken at its word" {
    # --noFlag stamps nothing, so there is no --fragmentType query that could
    # tell a deleted object from a live one
    migrate_incremental --noFlag
    assert_success
    local gone
    gone=$(jq -r '."4"' id_mapping.json)
    rm "$REC/objects/$gone.json"

    run_migrate --rootId 1 --sourceSession source.json --targetSession target.json \
        --force --noFlag --resume "$(run_id)"
    assert_success
    refute_output --partial "is gone from the target"
    assert_equal "$(jq -r '."4"' id_mapping.json)" "$gone"
}

@test "resume: an unmarked incremental run still reports its id" {
    # otherwise the run could never be resumed at all
    migrate_incremental --noFlag
    assert_success
    assert_output --partial "created objects are not marked"
    assert_regex "$(run_id)" '^[0-9]{8}T[0-9]{6}Z_[0-9a-f]{6}$'
}

# --- refusing rather than guessing ------------------------------------------

@test "resume: a run id the journal does not know is refused" {
    migrate_incremental
    assert_success
    run_migrate --rootId 1 --sourceSession source.json --targetSession target.json \
        --force --resume DIFFERENT_RUN
    assert_failure
    assert_output --partial "does not belong to run DIFFERENT_RUN"
    assert_output --partial "It records: runId"
}

@test "resume: a missing journal is refused, and says why" {
    run_migrate --rootId 1 --sourceSession source.json --targetSession target.json \
        --force --resume SOME_RUN
    assert_failure
    assert_output --partial "there is nothing to resume"
    assert_output --partial "Only an --incremental migration writes one"
    assert_equal "$(live_objects)" "0"
}

@test "resume: a run id that is not fragment safe is refused" {
    run_migrate --rootId 1 --sourceSession source.json --targetSession target.json \
        --force --resume 'bad id'
    assert_failure
    assert_output --partial "letters, digits and underscores"
}

@test "resume: a refused resume contacts no tenant and stages nothing" {
    local privateTmp="$BATS_TEST_TMPDIR/tmp"
    mkdir -p "$privateTmp"
    export TMPDIR="$privateTmp"

    run_migrate --rootId 1 --sourceSession source.json --targetSession target.json \
        --force --resume SOME_RUN
    assert_failure
    assert_equal "$(ls "$privateTmp" | grep -c '' || true)" "0"
    refute [ -f "$REC/calls.log" ]
}

# --- what a failure says ----------------------------------------------------

@test "resume: a failure that created nothing does not claim otherwise" {
    migrate_incremental
    assert_success

    # the limit check fails during the export, after the previous run's objects
    # already exist
    FAKE_C8Y_TOTAL_MEASUREMENTS=2000000 run_migrate --rootId 1 \
        --sourceSession source.json --targetSession target.json --force \
        --dataTypes measurements --resume "$(run_id)"
    assert_failure
    assert_output --partial "This attempt created nothing"
    refute_output --partial "stopped part way through"
}

@test "resume: the live-object query asks for every page" {
    # The pruning query decides which mapping entries are still real. Truncated
    # at c8y's default page size of 5, every entry past the first five looks
    # deleted and a resume recreates them - duplicating most of the hierarchy
    # instead of repairing it.
    migrate_incremental
    assert_success
    local objects
    objects=$(live_objects)

    run_migrate --rootId 1 --sourceSession source.json --targetSession target.json \
        --force --resume "$(run_id)"
    assert_success
    assert_flag_on_all_calls "--includeAll" "inventory find"
    assert_equal "$(live_objects)" "$objects"
    assert_equal "$(duplicate_links)" "0"
}
