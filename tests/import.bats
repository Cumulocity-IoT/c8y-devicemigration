#!/usr/bin/env bats

load helpers/load

setup() {
    setup_common
    FIXTURE="$TESTS_DIR/fixtures/export-9object"
    # the mapping file defaults to the working directory
    cd "$WORK"
}

# --- relationship completeness (regression: stdin consumed by c8y) ----------
#
# The production failure: `inventory children assign --child` accepts pipeline
# input, so it drained the pipe feeding the while-read loop and only the first
# child of each parent was assigned. Invisible unless a parent has 2+ children.

@test "import: assigns every relationship, not just the first per parent" {
    run_import --input-directory "$FIXTURE" --force
    assert_success
    # the fixture has two parents with two children each
    assert_equal "$(count_links)" "8"
    assert_equal "$(count_refs "$FIXTURE")" "8"
}

@test "import: the reconstructed link set matches the exported reference set" {
    run_import --input-directory "$FIXTURE" --force
    assert_success

    # translate the exported references through the id mapping, then compare
    local expected
    expected=$(export_refs "$FIXTURE" | while read -r parent childType child; do
        printf '%s %s %s\n' \
            "$(jq -r --arg k "$parent" '.[$k]' id_mapping.json)" \
            "$childType" \
            "$(jq -r --arg k "$child" '.[$k]' id_mapping.json)"
    done | sort)
    assert_equal "$(assigned_links)" "$expected"
}

@test "import: no c8y call consumes stdin" {
    run_import --input-directory "$FIXTURE" --force
    assert_success
    assert_no_call_drained_stdin
}

@test "import: every create and assign passes --nullInput" {
    run_import --input-directory "$FIXTURE" --force
    assert_success
    assert_flag_on_all_calls "--nullInput" "inventory create"
    assert_flag_on_all_calls "--nullInput" "inventory children assign"
}

# --- request bodies ---------------------------------------------------------

@test "import: server managed properties are stripped from the created body" {
    run_import --input-directory "$FIXTURE" --force
    assert_success

    local leaked
    leaked=$(jq -r 'keys[]' "$REC"/payloads/*.json | sort -u | grep -xE \
        'id|self|lastUpdated|creationTime|owner|childDevices|childAssets|childAdditions|deviceParents|assetParents|additionParents|c8y_Availability|c8y_ActiveAlarmsStatus|c8y_Connection' || true)
    assert_equal "$leaked" ""
}

@test "import: custom fragments survive, including an @-prefixed key" {
    run_import --input-directory "$FIXTURE" --force
    assert_success
    # the fixture carries @com_adamos_oee_datamodel_MachineOEEConfiguration
    local hits
    hits=$(grep -l 'com_adamos_oee_datamodel_MachineOEEConfiguration' "$REC"/payloads/*.json | wc -l | tr -d ' ')
    assert_equal "$hits" "2"
}

@test "import: creates exactly one managed object per exported file" {
    run_import --input-directory "$FIXTURE" --force
    assert_success
    assert_equal "$(count_calls "inventory create")" "9"
    assert_equal "$(ls "$REC/payloads" | wc -l | tr -d ' ')" "9"
}

# --- --force ---------------------------------------------------------------

@test "import: --force reaches every modifying call" {
    run_import --input-directory "$FIXTURE" --force
    assert_success
    assert_flag_on_all_calls "--force" "inventory create"
    assert_flag_on_all_calls "--force" "inventory children assign"
}

@test "import: without --force no call carries it" {
    run_import --input-directory "$FIXTURE"
    assert_success
    assert_flag_on_no_calls "--force" "inventory create"
    assert_flag_on_no_calls "--force" "inventory children assign"
}

# --- logging ---------------------------------------------------------------

@test "import: log lines name the object with ids in brackets" {
    run_import --input-directory "$FIXTURE" --force
    assert_success
    assert_line --regexp '^Imported Customer 1 \(5174376875 -> [0-9]+\)$'
    assert_line --regexp '^Assigned F1 \([0-9]+\) as asset child of Customer 1 \([0-9]+\)$'
}

@test "import: names carry no stray whitespace" {
    run_import --input-directory "$FIXTURE" --force
    assert_success
    # a trailing space on the name, before the separator space and the bracket,
    # was a real defect; two or more spaces means the name was not trimmed
    refute_line --regexp '(Imported|Assigned) .*  +\('
}

@test "import: a name with a bracket and hash is reported verbatim" {
    run_import --input-directory "$FIXTURE" --force
    assert_success
    assert_output --partial "Simulator Normal #1 - 1[OEE calculation profile]"
}

# --- id mapping -----------------------------------------------------------

@test "import: the mapping covers every imported object" {
    run_import --input-directory "$FIXTURE" --force
    assert_success
    assert [ -f id_mapping.json ]
    assert_equal "$(jq 'length' id_mapping.json)" "9"
    assert_equal "$(jq -r 'to_entries | all(.value | test("^[0-9]+$"))' id_mapping.json)" "true"
}

@test "import: --mapping redirects the mapping file" {
    run_import --input-directory "$FIXTURE" --force --mapping "$WORK/ids.json"
    assert_success
    assert [ -f "$WORK/ids.json" ]
    refute [ -f id_mapping.json ]
}

@test "import: awkward names do not corrupt the tab separated id map" {
    local dir="$WORK/awkward"
    mkdir -p "$dir"
    printf '{"id":"10","name":"Tab\\tHere \\"quoted\\", comma"}\n' > "$dir/exported_device_10.json"
    printf '{"id":"11","name":"Line1\\nLine2"}\n'                 > "$dir/exported_device_11.json"
    printf '{"id":"12","name":"   "}\n'                           > "$dir/exported_device_12.json"
    printf '{"id":"13","name":"Anlage Köln – Süd ✓"}\n'           > "$dir/exported_device_13.json"
    printf '["11","12","13"]\n'                                   > "$dir/10_child_device.json"

    run_import --input-directory "$dir" --force
    assert_success
    assert_equal "$(jq -r 'keys | join(",")' id_mapping.json)" "10,11,12,13"
    assert_equal "$(count_links)" "3"
    assert_output --partial "Anlage Köln – Süd ✓"
}

@test "import: an object with no usable name is reported as unnamed" {
    local dir="$WORK/noname"
    mkdir -p "$dir"
    printf '{"id":"20","type":"c8y_Addition"}\n' > "$dir/exported_device_20.json"
    run_import --input-directory "$dir" --force
    assert_success
    assert_line --regexp '^Imported <unnamed> \(20 -> [0-9]+\)$'
}

# --- input handling -------------------------------------------------------

@test "import: an input source is required" {
    run_import --force
    assert_failure
}

@test "import: the two input sources are mutually exclusive" {
    run_import --input-directory "$FIXTURE" --input-compressed "$WORK/x.tgz"
    assert_failure
}

@test "import: a missing input directory fails cleanly" {
    run_import --input-directory "$WORK/does-not-exist" --force
    assert_failure
    assert_output --partial "Input directory does not exist"
}

@test "import: an empty export yields an empty mapping and succeeds" {
    mkdir -p "$WORK/empty"
    run_import --input-directory "$WORK/empty" --force
    assert_success
    assert_equal "$(jq -c '.' id_mapping.json)" "{}"
    assert_equal "$(count_calls "inventory create")" "0"
}

@test "import: --input-compressed extracts and imports an archive" {
    tar -czf "$WORK/mig.tgz" -C "$FIXTURE" .
    run_import --input-compressed "$WORK/mig.tgz" --force
    assert_success
    assert_equal "$(count_links)" "8"
    assert_equal "$(jq 'length' id_mapping.json)" "9"
}

@test "import: --input-compressed leaves no temporary directory behind" {
    tar -czf "$WORK/mig.tgz" -C "$FIXTURE" .
    local before after
    before=$(ls "${TMPDIR:-/tmp}" | wc -l | tr -d ' ')
    run_import --input-compressed "$WORK/mig.tgz" --force
    assert_success
    after=$(ls "${TMPDIR:-/tmp}" | wc -l | tr -d ' ')
    assert_equal "$before" "$after"
}

# --- partial and failing inputs -------------------------------------------

@test "import: a dangling child reference is skipped without failing" {
    local dir="$WORK/partial"
    cp -r "$FIXTURE" "$dir"
    rm "$dir/exported_device_4074377869.json"   # F2, a child of Customer 1
    run_import --input-directory "$dir" --force
    assert_success
    assert_output --partial "was not part of the import"
    assert_equal "$(jq 'length' id_mapping.json)" "8"
}

@test "import: a create that returns no id aborts with a message" {
    FAKE_C8Y_FAIL_CREATE=1 run_import --input-directory "$FIXTURE" --force
    assert_failure
    assert_output --partial "Failed to create a managed object"
}

# --- generated parser -----------------------------------------------------

@test "import: --help succeeds, proving the committed parser matches its DOC" {
    run_import --help
    assert_success
    assert_output --partial "Import devices and their data into Cumulocity"
}

# --- external identities ----------------------------------------------------

@test "import: identities are recreated against the new object id" {
    run_import --input-directory "$FIXTURE" --force
    assert_success
    local newId
    newId=$(jq -r '.["5174376875"]' id_mapping.json)
    assert_equal "$(grep -c "^$newId c8y_Serial CUSTOMER-1-SN\$" "$REC/identities.log")" "1"
}

@test "import: a device with two identities gets both" {
    run_import --input-directory "$FIXTURE" --force
    assert_success
    local newId
    newId=$(jq -r '.["7874377868"]' id_mapping.json)
    assert_equal "$(grep -c "^$newId " "$REC/identities.log")" "2"
}

@test "import: an external id containing a space survives the tab separated read" {
    run_import --input-directory "$FIXTURE" --force
    assert_success
    assert_equal "$(grep -c 'c8y_ExternalId f1 site north$' "$REC/identities.log")" "1"
}

@test "import: every identity create passes --nullInput" {
    run_import --input-directory "$FIXTURE" --force
    assert_success
    assert_flag_on_all_calls "--nullInput" "identity create"
    assert_no_call_drained_stdin
}

@test "import: --force reaches identity creation too" {
    run_import --input-directory "$FIXTURE" --force
    assert_success
    assert_flag_on_all_calls "--force" "identity create"
}

@test "import: the summary reports how many identities were created" {
    run_import --input-directory "$FIXTURE" --force
    assert_success
    assert_line --regexp '^Imported 9 devices and 3 external identities,'
}

@test "import: an identity the tenant already knows is reported, not fatal" {
    FAKE_C8Y_FAIL_IDENTITY="F1-SN-0001" run_import --input-directory "$FIXTURE" --force
    assert_success
    assert_output --partial "Could not add external identity c8y_Serial:F1-SN-0001"
    assert_output --partial "it may already exist in the target tenant"
    # the run still completes and the remaining identities are created
    assert_line --regexp '^Imported 9 devices and 2 external identities,'
    assert_line --regexp '^1 external identities could not be created$'
    assert_equal "$(count_links)" "8"
}

@test "import: an export without identity files creates none" {
    local dir="$WORK/noident"
    mkdir -p "$dir"
    printf '{"id":"30","name":"lonely"}\n' > "$dir/exported_device_30.json"
    run_import --input-directory "$dir" --force
    assert_success
    assert_equal "$(count_calls "identity create")" "0"
    assert_line --regexp '^Imported 1 devices and 0 external identities,'
}
