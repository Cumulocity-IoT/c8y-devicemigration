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
    local before after
    before=$(ls "${TMPDIR:-/tmp}" | wc -l | tr -d ' ')
    run_export --rootId 1 --output-compressed "$WORK/mig.tgz"
    assert_success
    after=$(ls "${TMPDIR:-/tmp}" | wc -l | tr -d ' ')
    assert_equal "$before" "$after"
}

# --- generated parser -------------------------------------------------------

@test "export: --help succeeds, proving the committed parser matches its DOC" {
    # a stale docopt parser exits 70 on every invocation
    run_export --help
    assert_success
    assert_output --partial "Export devices and their data from Cumulocity"
}
