#!/usr/bin/env bats
#
# Level 2: the hierarchy moves in one pass, the data one device at a time.
#
# Metadata is small however large the migration is; the data is the part that is
# not bounded. So the disk this needs is one device's data, not the whole tree's.
# The properties worth defending are that the result is indistinguishable from
# the chained migration, and that the staged data really is discarded as it goes
# rather than merely reported as such.

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

migrate_chained() {
    run_migrate --rootId 1 --sourceSession source.json --targetSession target.json \
        --force "$@"
}

live_objects() { ls "$REC/objects" 2>/dev/null | grep -c '' | tr -d ' '; }

# Object bodies with the volatile parts removed, so two runs can be compared.
# The run marker carries a timestamp, so it can only be compared by shape.
normalised_objects() {
    local f
    for f in "$REC"/objects/*; do
        [[ -e "$f" ]] || continue
        jq -S 'del(.id, .self)
            | with_entries(select(.key | startswith("c8y_DeviceMigration_") | not))' "$f"
    done | jq -S -s 'sort_by(tojson)'
}

# Relationships named by their SOURCE ids. The target ids cannot be compared
# directly: the tenant allocates them from one sequence shared with identities
# and data records, so two runs that create the same hierarchy in a different
# order legitimately end up with different ids for the same object.
source_links() {
    jq -r 'to_entries[] | "\(.value) \(.key)"' id_mapping.json > "$BATS_TEST_TMPDIR/inverse.txt"
    awk 'NR == FNR { m[$1] = $2; next }
         { print ($1 in m ? m[$1] : $1), $2, ($3 in m ? m[$3] : $3) }' \
        "$BATS_TEST_TMPDIR/inverse.txt" "$REC/links.log" | sort
}

# What the import said it delivered, per source device, with the ids it happened
# to allocate normalised away. This is the per-device record count, so it fails
# if a device's data is dropped, duplicated or attached to the wrong device.
data_report() {
    # only the per-device lines: the aggregate totals line is deliberately
    # absent when each device is imported on its own, and says nothing about
    # what was delivered that these lines do not
    printf '%s\n' "$output" \
        | grep -E '^Imported .* for |^Attached ' \
        | sed 's/[0-9]\{4,\}/<id>/g' \
        | sort
}

# Everything the target tenant ended up holding, as one comparable blob
target_state() {
    normalised_objects
    source_links
    jq -S 'to_entries | map(.key) | sort' id_mapping.json
    data_report
}

# --- equivalence with the chained migration ---------------------------------

@test "incremental: the target ends up in the same state as a chained migration" {
    migrate_chained --dataTypes measurements,events,alarms
    assert_success
    local chained
    chained=$(target_state)

    # a second migration from scratch, incremental this time
    find "$REC" -mindepth 1 -delete
    rm -f id_mapping.json
    migrate_incremental --dataTypes measurements,events,alarms
    assert_success

    assert_equal "$(target_state)" "$chained"
}

@test "incremental: the same objects, identities and relationships are created" {
    migrate_incremental --dataTypes measurements,events,alarms
    assert_success
    assert_equal "$(jq 'length' id_mapping.json)" "9"
    assert_equal "$(live_objects)" "9"
    assert_equal "$(count_links)" "10"
    assert [ "$(count_calls "identity create")" -eq 4 ]
}

@test "incremental: every device's data is delivered" {
    migrate_incremental --dataTypes measurements,events,alarms
    assert_success
    # the fixture puts data on two devices only. The device is named, not just
    # numbered: the mapping holds no names, so this is only right because the
    # name is recovered from the exported object.
    assert_output --partial "Imported 3 measurements for Line A"
    assert_output --partial "Imported 2 events for Line A"
    assert_output --partial "Imported 3 alarms for Line A"
    assert_output --partial 'Imported 1 measurement for Pump "West"' 
}

# --- the bounded-disk property ------------------------------------------------

@test "incremental: a device's data is imported before the next one is exported" {
    # This is what actually bounds the disk. If the export of device N+1 ran
    # before the import of device N, the staging directory would grow with the
    # hierarchy however promptly it was cleaned afterwards. The chained
    # migration reads everything first, so the two orders are distinguishable.
    migrate_incremental --dataTypes measurements
    assert_success

    local order
    order=$(awk -F'\t' '$1 ~ /^measurements list/ { print "read" }
                        $1 ~ /^measurements createBulk/ { print "write" }' \
        "$REC/calls.log" | uniq)
    # reads and writes interleave rather than forming one read block then one
    # write block, which is all a chained run could produce
    assert [ "$(printf '%s\n' "$order" | grep -c '^write$')" -gt 1 ]
}

@test "incremental: no device data is left staged at the end" {
    local privateTmp="$BATS_TEST_TMPDIR/tmp"
    mkdir -p "$privateTmp"
    export TMPDIR="$privateTmp"

    migrate_incremental --dataTypes measurements,events,alarms
    assert_success
    assert_equal "$(ls "$privateTmp" | grep -c '' || true)" "0"
}

@test "incremental: --keepStaging keeps the metadata but not a device's data" {
    migrate_incremental --dataTypes measurements,events,alarms --keepStaging
    assert_success
    local kept
    kept=$(printf '%s\n' "$output" | sed -n 's/.*Staging directory kept at //p')
    assert [ -d "$kept/metadata" ]
    assert [ -f "$kept/metadata/exported_device_1.json" ]
    # the per-device data directory is discarded even when staging is kept: it
    # is reused for every device, so what survived would be misleading
    assert [ ! -d "$kept/data" ]
    rm -rf "$kept"
}

# --- limits are still checked before anything is created ---------------------

@test "incremental: an oversized hierarchy is refused before any object exists" {
    FAKE_C8Y_HIERARCHY="$TESTS_DIR/fixtures/hierarchy-wide.json" \
        migrate_incremental --dataTypes measurements
    assert_failure
    assert_output --partial "child objects"
    # the metadata export checks the limits, so this fails before the import
    assert_equal "$(live_objects)" "0"
}

@test "incremental: a per-device data limit is refused before any object exists" {
    FAKE_C8Y_TOTAL_MEASUREMENTS=2000000 migrate_incremental --dataTypes measurements
    assert_failure
    assert_output --partial "measurements"
    assert_equal "$(live_objects)" "0"
}

# --- the marker fragment survives the different path ------------------------

@test "incremental: created objects still carry the run marker" {
    migrate_incremental
    assert_success
    local markers
    markers=$(jq -r '[keys[] | select(startswith("c8y_DeviceMigration_"))][0]' \
        "$REC"/payloads/*.json | sort -u | grep -c '')
    assert_equal "$markers" "1"
}

# --- no data requested -------------------------------------------------------

@test "incremental: with no data types it migrates the hierarchy and stops" {
    migrate_incremental
    assert_success
    assert_equal "$(live_objects)" "9"
    refute_output --partial "Migrating data for"
    assert_equal "$(count_calls "measurements list")" "0"
}

# --- export --metadataOnly ---------------------------------------------------

@test "metadataOnly: objects and identities are written but no data" {
    run_export --rootId 1 --output-directory "$OUT" \
        --dataTypes measurements,events,alarms --metadataOnly
    assert_success
    assert [ -f "$OUT/exported_device_1.json" ]
    assert [ -f "$OUT/1_identities.json" ]
    assert [ -f "$OUT/1_child_asset.json" ]
    refute [ -d "$OUT/data" ]
}

@test "metadataOnly: no data is even read" {
    run_export --rootId 1 --output-directory "$OUT" \
        --dataTypes measurements,events,alarms --metadataOnly
    assert_success
    # the count queries behind the limit check are the only data traffic
    assert_equal "$(count_calls "measurements list --device")" "9"
    refute_output --partial "measurements,"
}

@test "metadataOnly: the totals line does not claim data was exported" {
    run_export --rootId 1 --output-directory "$OUT" \
        --dataTypes measurements,events,alarms --metadataOnly
    assert_success
    assert_output --partial "Exported 9 managed objects and 4 external identities to"
    refute_output --partial "with 0 measurements"
}

@test "metadataOnly: the limits still apply to the data that was asked for" {
    # the point of checking here rather than per device: an oversized migration
    # has to be refused before the hierarchy is created in the target
    FAKE_C8Y_TOTAL_MEASUREMENTS=2000000 \
        run_export --rootId 1 --output-directory "$OUT" \
            --dataTypes measurements --metadataOnly
    assert_failure
    assert_output --regexp "measurements +[0-9]+ +2000000 +1000000"
    refute [ -d "$OUT" ]
}

# --- export --dataOnly -------------------------------------------------------

@test "dataOnly export: data is written but no identities or child indexes" {
    run_export --rootId 1 --output-directory "$OUT" \
        --dataTypes measurements,events,alarms --dataOnly --no-children
    assert_success
    refute [ -f "$OUT/1_identities.json" ]
    refute [ -f "$OUT/1_child_asset.json" ]
    assert_equal "$(count_calls "identity list")" "0"
}

@test "dataOnly export: a device's data is written" {
    run_export --rootId 2 --output-directory "$OUT" \
        --dataTypes measurements,events,alarms --dataOnly --no-children
    assert_success
    assert_equal "$(grep -c '' "$OUT/data/2/measurements.jsonl")" "3"
    assert_equal "$(grep -c '' "$OUT/data/2/events.jsonl")" "2"
    assert_equal "$(grep -c '' "$OUT/data/2/alarms.jsonl")" "3"
}

@test "dataOnly export: one device reports no totals line of its own" {
    run_export --rootId 2 --output-directory "$OUT" \
        --dataTypes measurements --dataOnly --no-children
    assert_success
    assert_output --partial "Exported Line A (2), 3 measurements"
    # it could only restate the line above, with a staging path appended
    refute_output --partial "Exported 3 measurements to"
}

# --- import --dataOnly -------------------------------------------------------

# An export plus a mapping standing in for a hierarchy imported earlier
setup_existing_hierarchy() {
    run_export --rootId 1 --output-directory "$OUT" \
        --dataTypes measurements,events,alarms
    assert_success
    cat > mapping.json <<'JSON'
{"1":"5001","2":"5002","3":"5003","4":"5004","5":"5005","6":"5006","7":"5007","10":"5010","11":"5011"}
JSON
}

@test "dataOnly import: data is created and no objects or relationships are" {
    setup_existing_hierarchy
    run_import --input-directory "$OUT" --mapping mapping.json --force --dataOnly
    assert_success
    assert_equal "$(count_calls "inventory create")" "0"
    assert_equal "$(count_calls "inventory children assign")" "0"
    assert_equal "$(count_calls "identity create")" "0"
    assert [ "$(count_calls "measurements createBulk")" -gt 0 ]
}

@test "dataOnly import: data goes to the mapped device, not the source id" {
    setup_existing_hierarchy
    run_import --input-directory "$OUT" --mapping mapping.json --force --dataOnly
    assert_success
    assert_output --partial "Imported 3 measurements for Line A (5002)"
    assert_output --partial 'Imported 1 measurement for Pump "West" (5004)'
}

@test "dataOnly import: the mapping is left exactly as it was" {
    # This is not a cosmetic property. run_import writes an empty mapping at
    # startup so an immediate failure still leaves a file, and in --dataOnly
    # that overwrote the mapping being read - taking the revert record with it.
    setup_existing_hierarchy
    local before
    before=$(cat mapping.json)
    run_import --input-directory "$OUT" --mapping mapping.json --force --dataOnly
    assert_success
    assert_equal "$(cat mapping.json)" "$before"
}

@test "dataOnly import: a missing mapping is refused" {
    setup_existing_hierarchy
    run_import --input-directory "$OUT" --mapping absent.json --force --dataOnly
    assert_failure
    assert_output --partial "needs an existing mapping file"
    assert_equal "$(count_calls "measurements createBulk")" "0"
}

@test "dataOnly import: a mapping that is not a JSON object is refused" {
    setup_existing_hierarchy
    printf '[1,2,3]\n' > mapping.json
    run_import --input-directory "$OUT" --mapping mapping.json --force --dataOnly
    assert_failure
    assert_output --partial "not a JSON object"
}

@test "dataOnly import: a device missing from the mapping is skipped and named" {
    setup_existing_hierarchy
    # device 2 is the one carrying most of the data
    jq 'del(."2")' mapping.json > m2.json && mv m2.json mapping.json
    run_import --input-directory "$OUT" --mapping mapping.json --force --dataOnly
    assert_success
    assert_output --partial "Skipping data for (2), it is not in the mapping"
    # the rest still arrives
    assert_output --partial 'Imported 1 measurement for Pump "West" (5004)'
}

@test "dataOnly import: totals are reported across several devices" {
    setup_existing_hierarchy
    run_import --input-directory "$OUT" --mapping mapping.json --force --dataOnly
    assert_success
    # two devices carry data here, so the aggregate says something the
    # per-device lines do not
    assert_output --partial "Imported 4 measurements, 2 events and 3 alarms"
}
