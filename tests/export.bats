#!/usr/bin/env bats

load helpers/load

setup() { setup_common; }

# --- child index files (regression: bare IDs appended with >>) ---------------

@test "export: child index files are valid JSON arrays" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    run jq -e 'type == "array" and all(type == "string")' "$OUT/1_child_asset.json"
    assert_success
    assert_equal "$(jq -c '.' "$OUT/1_child_asset.json")" '["2","3"]'
}

@test "export: rerunning over the same directory is idempotent" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    local first
    first=$(export_refs "$OUT")
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    assert_equal "$(export_refs "$OUT")" "$first"
    assert_equal "$(jq -c '.' "$OUT/1_child_asset.json")" '["2","3"]'
}

# --- traversal integrity ----------------------------------------------------

@test "export: a parent with two children exports both" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    # 1 -> asset 2,3   2 -> asset 4,5   3 -> device 6,7
    assert_equal "$(jq -c '.' "$OUT/1_child_asset.json")" '["2","3"]'
    assert_equal "$(jq -c '.' "$OUT/2_child_asset.json")" '["4","5"]'
    assert_equal "$(jq -c '.' "$OUT/3_child_device.json")" '["6","7"]'
}

@test "export: no c8y call consumes stdin" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    assert_no_call_drained_stdin
}

@test "export: every c8y call passes --nullInput" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    assert_flag_on_all_calls "--nullInput" "inventory get"
    assert_flag_on_all_calls "--nullInput" "inventory children list"
}

@test "export: a cycle in the hierarchy terminates without refetching" {
    # 1 -> 2 -> 5 -> 2 closes a loop
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    # the cycle edge was genuinely traversed
    assert_equal "$(jq -c '.' "$OUT/5_child_asset.json")" '["2"]'
    # yet every object was fetched exactly once, so the guard did the work
    # rather than the test simply not reaching the loop
    assert_equal "$(count_calls "inventory get")" "9"
    assert_equal "$(exported_ids "$OUT")" "1 2 3 4 5 6 7 10 11"
}

@test "export: a device with two parents is exported once" {
    # 4 is an asset child of both 2 and 3
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    assert_equal "$(ls "$OUT" | grep -c '^exported_device_4\.json$')" "1"
    # fetched once, not once per parent
    assert_equal "$(count_gets_for_id 4)" "1"
    # but both references are still recorded
    assert_equal "$(jq -c '.' "$OUT/3_child_asset.json")" '["4"]'
    assert_equal "$(jq -c '.' "$OUT/2_child_asset.json")" '["4","5"]'
}

@test "export: default traversal covers the whole asset+device tree" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    assert_equal "$(exported_ids "$OUT")" "1 2 3 4 5 6 7 10 11"
    assert_equal "$(count_refs "$OUT")" "10"
}

# --- --childTypes -----------------------------------------------------------

@test "export: default childTypes excludes additions transitively" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    # 8 is an addition of 1, and 9 hangs off 8; neither may appear
    refute [ -e "$OUT/exported_device_8.json" ]
    refute [ -e "$OUT/exported_device_9.json" ]
    refute [ -e "$OUT/1_child_addition.json" ]
}

@test "export: --childTypes with addition includes the addition branch" {
    run_export --rootId 1 --output-directory "$OUT" --childTypes asset,device,addition
    assert_success
    assert_equal "$(exported_ids "$OUT")" "1 2 3 4 5 6 7 8 9 10 11"
    assert_equal "$(count_refs "$OUT")" "12"
}

@test "export: --childTypes device narrows the traversal" {
    run_export --rootId 1 --output-directory "$OUT" --childTypes device
    assert_success
    assert_equal "$(exported_ids "$OUT")" "1 10 11"
}

@test "export: --childTypes tolerates spaces and empty entries" {
    run_export --rootId 1 --output-directory "$OUT" --childTypes "asset, device"
    assert_success
    assert_equal "$(exported_ids "$OUT")" "1 2 3 4 5 6 7 10 11"
}

@test "export: an empty --childTypes falls back to the default" {
    # docopt substitutes the default for an empty option value
    run_export --rootId 1 --output-directory "$OUT" --childTypes ""
    assert_success
    assert_equal "$(exported_ids "$OUT")" "1 2 3 4 5 6 7 10 11"
}

@test "export: an unsupported child type is rejected and names the value" {
    run_export --rootId 1 --output-directory "$OUT" --childTypes asset,devices
    assert_failure
    assert_output --partial "devices"
    assert_equal "$(count_calls "inventory")" "0"
}

@test "export: --childTypes with no valid entries is rejected" {
    run_export --rootId 1 --output-directory "$OUT" --childTypes ","
    assert_failure
    assert_output --partial "No child types given"
}

# --- --no-children ----------------------------------------------------------

@test "export: --no-children exports only the root" {
    run_export --rootId 1 --output-directory "$OUT" --no-children
    assert_success
    assert_equal "$(exported_ids "$OUT")" "1"
    assert_equal "$(ls "$OUT" | grep -c '_child_' || true)" "0"
    assert_equal "$(count_calls "inventory children list")" "0"
}

@test "export: --no-children and --childTypes are mutually exclusive" {
    run_export --rootId 1 --output-directory "$OUT" --childTypes asset --no-children
    assert_failure
}

# --- output selection -------------------------------------------------------

@test "export: an output destination is required" {
    run_export --rootId 1
    assert_failure
}

@test "export: the two output destinations are mutually exclusive" {
    run_export --rootId 1 --output-directory "$OUT" --output-compressed "$WORK/a.tgz"
    assert_failure
}

@test "export: --rootId is required" {
    run_export --output-directory "$OUT"
    assert_failure
}

@test "export: --output-compressed produces an archive with the same members" {
    run_export --rootId 1 --output-compressed "$WORK/mig.tgz"
    assert_success
    assert [ -s "$WORK/mig.tgz" ]

    run_export --rootId 1 --output-directory "$OUT"
    assert_success

    local from_archive from_directory
    from_archive=$(tar -tzf "$WORK/mig.tgz" | sed 's|^\./||' | grep '\.json$' | sort)
    from_directory=$(ls "$OUT" | sort)
    assert_equal "$from_archive" "$from_directory"
}

@test "export: --output-compressed leaves no staging directory behind" {
    local before after added
    before=$(ls "${TMPDIR:-/tmp}" 2>/dev/null | sort)
    run_export --rootId 1 --output-compressed "$WORK/mig.tgz"
    assert_success
    after=$(ls "${TMPDIR:-/tmp}" 2>/dev/null | sort)
    # only additions matter: unrelated processes remove entries too
    added=$(comm -13 <(printf '%s\n' "$before") <(printf '%s\n' "$after"))
    assert_equal "$added" ""
}

# --- generated parser -------------------------------------------------------

@test "export: --help succeeds, proving the committed parser matches its DOC" {
    # a stale docopt parser exits 70 on every invocation
    run_export --help
    assert_success
    assert_output --partial "Export devices and their data from Cumulocity"
}

# --- external identities ----------------------------------------------------

@test "export: identities are written as a valid JSON array" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    run jq -e 'type == "array" and all(has("type") and has("externalId"))' "$OUT/1_identities.json"
    assert_success
    assert_equal "$(jq -c '.' "$OUT/1_identities.json")" \
        '[{"type":"c8y_Serial","externalId":"PLANT-BERLIN-01"}]'
}

@test "export: a device with two identities exports both" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    assert_equal "$(jq 'length' "$OUT/2_identities.json")" "2"
    assert_equal "$(jq -r '.[].externalId' "$OUT/2_identities.json" | sort | tr '\n' ',')" \
        "LINE-A-SN,line-a@example,"
}

@test "export: an external id containing a space is preserved" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    assert_equal "$(jq -r '.[0].externalId' "$OUT/4_identities.json")" "PUMP WEST 001"
}

@test "export: devices without identities get no identity file" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    # only 1, 2 and 4 carry identities in the fixture
    assert_equal "$(ls "$OUT" | grep -c '_identities\.json$' || true)" "3"
    refute [ -e "$OUT/3_identities.json" ]
}

@test "export: identities are collected for every visited device" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    # one identity list call per exported object, and never more
    assert_equal "$(count_calls "identity list")" "9"
    assert_flag_on_all_calls "--nullInput" "identity list"
}

@test "export: --no-children still exports the root's identities" {
    run_export --rootId 1 --output-directory "$OUT" --no-children
    assert_success
    assert_equal "$(exported_ids "$OUT")" "1"
    assert [ -e "$OUT/1_identities.json" ]
    assert_equal "$(count_calls "identity list")" "1"
}

# --- progress reporting -----------------------------------------------------

@test "export: raw managed objects are not printed" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    # the whole object goes to the file, not to the operator
    refute_output --partial '"lastUpdated"'
    refute_output --partial '"my_Custom-Fragment"'
    refute_output --partial '"c8y_Availability"'
}

@test "export: each device is reported by name and id" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    assert_line "Exported Plant Berlin (1), 2 asset children, 1 device child, 1 identity"
    assert_line "Exported Edge Node (11)"
}

@test "export: an unnamed device is reported as unnamed" {
    run_export --rootId 1 --output-directory "$OUT" --childTypes asset,device,addition
    assert_success
    assert_line "Exported <unnamed> (8), 1 device child"
}

@test "export: counts are singular or plural as appropriate" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    assert_line --partial "2 asset children"
    assert_line --partial "1 device child"
    assert_line --partial "2 identities"
    assert_line --partial "1 identity"
}

@test "export: the closing line totals objects and identities" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    assert_line "Exported 9 managed objects and 4 external identities to $OUT"
}

@test "export: the closing line names the archive in compressed mode" {
    run_export --rootId 1 --output-compressed "$WORK/mig.tgz"
    assert_success
    assert_line --partial "to $WORK/mig.tgz"
}

@test "export: --no-children reports just the root" {
    run_export --rootId 1 --output-directory "$OUT" --no-children
    assert_success
    assert_line "Exported Plant Berlin (1), 1 identity"
    assert_line --partial "Exported 1 managed object and 1 external identity"
}

# --- device data -------------------------------------------------------------

@test "export: no data is collected unless asked for" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    refute [ -d "$OUT/data" ]
    assert_equal "$(count_calls "measurements list")" "0"
    assert_equal "$(count_calls "events list")" "0"
    assert_equal "$(count_calls "alarms list")" "0"
}

@test "export: --dataTypes selects only the requested types" {
    run_export --rootId 1 --output-directory "$OUT" --dataTypes measurements
    assert_success
    assert [ -f "$OUT/data/2/measurements.jsonl" ]
    refute [ -f "$OUT/data/2/events.jsonl" ]
    refute [ -f "$OUT/data/2/alarms.jsonl" ]
    assert_equal "$(count_calls "events list")" "0"
    assert_equal "$(count_calls "alarms list")" "0"
}

@test "export: all three types land in per-device folders" {
    run_export --rootId 1 --output-directory "$OUT" --dataTypes measurements,events,alarms
    assert_success
    assert_equal "$(jq -c '.' <(jq -s 'length' "$OUT/data/2/measurements.jsonl"))" "3"
    assert_equal "$(jq -s 'length' "$OUT/data/2/events.jsonl")" "2"
    assert_equal "$(jq -s 'length' "$OUT/data/2/alarms.jsonl")" "3"
    # device 4 has measurements only
    assert [ -f "$OUT/data/4/measurements.jsonl" ]
    refute [ -f "$OUT/data/4/events.jsonl" ]
}

@test "export: devices without data get no data folder" {
    run_export --rootId 1 --output-directory "$OUT" --dataTypes measurements,events,alarms
    assert_success
    # 6 and 7 are leaf sensors with no data in the fixture
    refute [ -d "$OUT/data/6" ]
    refute [ -d "$OUT/data/7" ]
}

@test "export: data records are written verbatim for import to strip" {
    run_export --rootId 1 --output-directory "$OUT" --dataTypes alarms
    assert_success
    # the server-managed properties are still present in the export
    run jq -e 'has("id") and has("self") and has("count") and has("history")' \
        <(head -1 "$OUT/data/2/alarms.jsonl")
    assert_success
}

@test "export: event attachments are downloaded and named by the source event id" {
    run_export --rootId 1 --output-directory "$OUT" --dataTypes events
    assert_success
    # e2 is the fixture event carrying c8y_IsBinary
    assert [ -f "$OUT/data/2/binaries/e2" ]
    assert_equal "$(cat "$OUT/data/2/binaries/e2")" "attachment of event e2"
    # only the event with an attachment cost a download
    assert_equal "$(grep -c '' "$REC/binaries-downloaded.log")" "1"
}

@test "export: events without attachments cost no download" {
    run_export --rootId 1 --output-directory "$OUT" --dataTypes events
    assert_success
    refute [ -e "$OUT/data/2/binaries/e1" ]
}

@test "export: --dateFrom and --dateTo reach the list commands" {
    run_export --rootId 1 --output-directory "$OUT" --dataTypes measurements \
        --dateFrom "-30d" --dateTo "-1d"
    assert_success
    assert_flag_on_all_calls "--dateFrom" "measurements list"
    assert_flag_on_all_calls "--dateTo" "measurements list"
}

@test "export: data fetches page through everything, count queries do not" {
    run_export --rootId 1 --output-directory "$OUT" --dataTypes measurements,events,alarms
    assert_success

    # A list call is either a pre-flight count (--withTotalElements, one page)
    # or a fetch (--includeAll, every page). Neither may be the other.
    local unpagedFetch pagedCount
    unpagedFetch=$(awk -F'\t' \
        '$1 ~ /^(measurements|events|alarms) list/ && $3 !~ /--withTotalElements/ \
         && $3 !~ /--includeAll/ { print $3 }' "$REC/calls.log")
    assert_equal "$unpagedFetch" ""

    pagedCount=$(awk -F'\t' \
        '$3 ~ /--withTotalElements/ && $3 ~ /--includeAll/ { print $3 }' "$REC/calls.log")
    assert_equal "$pagedCount" ""

    # and both kinds actually happened
    assert [ "$(grep -c -- '--withTotalElements' "$REC/calls.log")" -gt 0 ]
    assert [ "$(grep -c -- '--includeAll' "$REC/calls.log")" -gt 0 ]
}

@test "export: operations are rejected with the reason" {
    run_export --rootId 1 --output-directory "$OUT" --dataTypes operations
    assert_failure
    assert_output --partial "Operations are not migrated"
    assert_output --partial "PENDING"
    assert_equal "$(count_calls "measurements list")" "0"
}

@test "export: an unsupported data type is rejected and names the value" {
    run_export --rootId 1 --output-directory "$OUT" --dataTypes measurements,bogus
    assert_failure
    assert_output --partial "bogus"
}

@test "export: --dataTypes with no valid entries is rejected" {
    run_export --rootId 1 --output-directory "$OUT" --dataTypes ","
    assert_failure
    assert_output --partial "No data types given"
}

@test "export: progress lines report data counts per device" {
    run_export --rootId 1 --output-directory "$OUT" --dataTypes measurements,events,alarms
    assert_success
    assert_line --partial "Exported Line A (2), 2 asset children, 2 identities, 3 measurements, 2 events, 1 attachment, 3 alarms"
    assert_line --partial 'Exported Pump "West" (4), 1 identity, 1 measurement'
}

@test "export: the closing line totals the data" {
    run_export --rootId 1 --output-directory "$OUT" --dataTypes measurements,events,alarms
    assert_success
    assert_line --partial "with 4 measurements, 2 events and 3 alarms including 1 attachment"
}

@test "export: the closing line omits data when none was requested" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    refute_output --partial "measurements"
}

@test "export: no c8y call consumes stdin while collecting data" {
    run_export --rootId 1 --output-directory "$OUT" --dataTypes measurements,events,alarms
    assert_success
    assert_no_call_drained_stdin
}

@test "export: data files travel in the compressed archive" {
    run_export --rootId 1 --output-compressed "$WORK/mig.tgz" --dataTypes measurements
    assert_success
    run tar -tzf "$WORK/mig.tgz"
    assert_output --partial "data/2/measurements.jsonl"
}

# --- limits ------------------------------------------------------------------
#
# Counted before anything is written, so a refusal leaves nothing behind. The
# during-export check is the backstop for data that grew in between.

@test "export: the hierarchy child limit is enforced" {
    FAKE_C8Y_HIERARCHY="$TESTS_DIR/fixtures/hierarchy-wide.json" \
        run_export --rootId 1 --output-directory "$OUT"
    assert_failure
    assert_output --partial "the hierarchy has 30 child objects, more than the limit of 25"
    assert_output --partial "Nothing was exported"
    refute [ -d "$OUT" ]
}

@test "export: a hierarchy inside the limit is fine" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    refute_output --partial "limits exceeded"
}

@test "export: the per-device measurement limit is enforced" {
    FAKE_C8Y_TOTAL_MEASUREMENTS=2000000 \
        run_export --rootId 1 --output-directory "$OUT" --dataTypes measurements
    assert_failure
    assert_output --partial "has 2000000 measurements, more than the limit of 1000000"
    refute [ -d "$OUT" ]
}

@test "export: the per-device event limit is enforced" {
    FAKE_C8Y_TOTAL_EVENTS=5000 \
        run_export --rootId 1 --output-directory "$OUT" --dataTypes events
    assert_failure
    assert_output --partial "has 5000 events, more than the limit of 1000"
    refute [ -d "$OUT" ]
}

@test "export: the per-device alarm limit is enforced" {
    FAKE_C8Y_TOTAL_ALARMS=5000 \
        run_export --rootId 1 --output-directory "$OUT" --dataTypes alarms
    assert_failure
    assert_output --partial "has 5000 alarms, more than the limit of 1000"
    refute [ -d "$OUT" ]
}

@test "export: every violation is reported, not just the first" {
    FAKE_C8Y_TOTAL_MEASUREMENTS=2000000 \
        run_export --rootId 1 --output-directory "$OUT" --dataTypes measurements
    assert_failure
    # one line per offending device, not a single early exit
    assert [ "$(printf '%s\n' "$output" | grep -c 'more than the limit')" -gt 1 ]
}

@test "export: limits are counted before anything is written" {
    FAKE_C8Y_TOTAL_MEASUREMENTS=2000000 \
        run_export --rootId 1 --output-directory "$OUT" --dataTypes measurements
    assert_failure
    # no object was even fetched, only counted
    assert_equal "$(count_calls "inventory get")" "0"
}

@test "export: --ignoreLimits exports anyway" {
    FAKE_C8Y_TOTAL_MEASUREMENTS=2000000 \
        run_export --rootId 1 --output-directory "$OUT" --dataTypes measurements --ignoreLimits
    assert_success
    assert [ -d "$OUT" ]
    assert_output --partial "Exporting anyway because --ignoreLimits was given"
}

@test "export: --ignoreLimits warns even when no limit is exceeded" {
    run_export --rootId 1 --output-directory "$OUT" --ignoreLimits
    assert_success
    assert_output --partial "WARNING: --ignoreLimits is set"
    assert_output --partial "put avoidable load on the source tenant"
}

@test "export: no warning when limits are enforced" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    refute_output --partial "WARNING"
}

@test "export: measurements default to the last 14 days" {
    run_export --rootId 1 --output-directory "$OUT" --dataTypes measurements
    assert_success
    assert_flag_on_all_calls "--dateFrom" "measurements list"
    run grep '^measurements list' "$REC/calls.log"
    assert_output --partial "--dateFrom -14d"
}

@test "export: events and alarms default to the whole history" {
    run_export --rootId 1 --output-directory "$OUT" --dataTypes events,alarms
    assert_success
    assert_flag_on_no_calls "--dateFrom" "events list"
    assert_flag_on_no_calls "--dateFrom" "alarms list"
}

@test "export: an explicit --dateFrom overrides the measurement default" {
    run_export --rootId 1 --output-directory "$OUT" --dataTypes measurements,events --dateFrom "-90d"
    assert_success
    run grep '^measurements list' "$REC/calls.log"
    refute_output --partial "--dateFrom -14d"
    assert_output --partial "--dateFrom -90d"
    # and now applies to events too
    assert_flag_on_all_calls "--dateFrom" "events list"
}

@test "export: a limit breached during the export removes the partial result" {
    # the count query reports nothing, so the pre-flight passes, but the fetch
    # returns more alarms than a limit of one allows
    FAKE_C8Y_TOTAL_ALARMS=0 MAX_ALARMS_OVERRIDE=1 run bash -c "
        sed 's/^MAX_ALARMS_PER_DEVICE=1000\$/MAX_ALARMS_PER_DEVICE=1/' '$REPO_DIR/commands/export' > '$WORK/export-tight'
        FAKE_C8Y_TOTAL_ALARMS=0 bash '$WORK/export-tight' --rootId 1 --output-directory '$OUT' --dataTypes alarms 2>&1"
    assert_failure
    assert_output --partial "more than the limit of 1"
    assert_output --partial "The partial export was removed"
    refute [ -d "$OUT" ]
}

@test "export: reverting a partial export leaves a pre-existing directory alone" {
    mkdir -p "$OUT/other"
    echo "keep me" > "$OUT/important.txt"
    touch "$OUT/other/keep"

    run bash -c "
        sed 's/^MAX_ALARMS_PER_DEVICE=1000\$/MAX_ALARMS_PER_DEVICE=1/' '$REPO_DIR/commands/export' > '$WORK/export-tight'
        FAKE_C8Y_TOTAL_ALARMS=0 bash '$WORK/export-tight' --rootId 1 --output-directory '$OUT' --dataTypes alarms 2>&1"
    assert_failure
    assert_output --partial "The partial export was removed"

    # the caller's own files are not ours to delete
    assert [ -f "$OUT/important.txt" ]
    assert_equal "$(cat "$OUT/important.txt")" "keep me"
    assert [ -f "$OUT/other/keep" ]
    # but nothing of ours is left
    assert_equal "$(ls "$OUT" | grep -cE 'exported_device_|_child_|_identities|^data$' || true)" "0"
}

@test "export: --help documents the limits" {
    run_export --help
    assert_success
    assert_output --partial "at most 25 child objects in the hierarchy"
    assert_output --partial "at most 1000000 measurements per device"
    assert_output --partial "at most 1000 events per device"
    assert_output --partial "at most 1000 alarms per device"
}
