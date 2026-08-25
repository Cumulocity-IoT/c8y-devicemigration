#!/usr/bin/env bats

load helpers/load

setup() {
    setup_common
    # read straight out of the command, so a test can never assert against a
    # stale copy of a template
    ALARM_STRIP_FOR_TEST=$(sed -n 's/^ALARM_STRIP="\(.*\)"$/\1/p' "$REPO_DIR/commands/import")
    EVENT_STRIP_FOR_TEST=$(sed -n 's/^EVENT_STRIP="\(.*\)"$/\1/p' "$REPO_DIR/commands/import")
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
    # A private TMPDIR, so the assertion is exact rather than "nothing new
    # appeared in a directory the whole machine shares" - and so the test stays
    # correct when the suite runs in parallel.
    local privateTmp="$BATS_TEST_TMPDIR/tmp"
    mkdir -p "$privateTmp"

    # exported, not a command prefix: a prefix assignment on a function call is
    # not passed to the child process, which would leave the command using the
    # real TMPDIR and make the assertion below vacuous
    export TMPDIR="$privateTmp"
    run_import --input-compressed "$WORK/mig.tgz" --force
    assert_success

    # the command stages its work in a mktemp directory and must clean it up
    assert_equal "$(ls "$privateTmp" | grep -c '' || true)" "0"
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

# --- device data -------------------------------------------------------------
#
# The data creates are the only calls in this extension that take their records
# on stdin: --device is the pipeline target, so piping is how throughput is
# achieved. That is why the stdin invariant is scoped rather than absolute.

setup_data_export() {
    shared_export --rootId 1 --output-directory "$OUT" --dataTypes measurements,events,alarms
    [ "$status" -eq 0 ]
}

# The export plus its import, both memoised. Replaces the very common pair of
# setup_data_export followed by an identical `run_import ... --force`, which
# together cost about 3.5 s per test.
do_data_migration() {
    shared_migration --rootId 1 --output-directory "$OUT" \
        --dataTypes measurements,events,alarms
}

@test "import: measurements go through the bulk collection command" {
    do_data_migration
    assert_success
    assert_equal "$(count_calls "measurements createBulk")" "2"
    # and never one request per record
    assert_equal "$(count_calls "measurements create ")" "0"
}

@test "import: measurement batches are deliberately small" {
    do_data_migration
    assert_success
    # <device> <recordCount> <batchSize>
    run awk '{print $3}' "$REC/measurements.log"
    assert_output --partial "50"
    refute_output --partial "none"
}

@test "import: every measurement is delivered" {
    do_data_migration
    assert_success
    assert_equal "$(awk '{s+=$2} END {print s+0}' "$REC/measurements.log")" "4"
    assert_line --partial "Imported 3 measurements for Line A"
}

@test "import: the data commands really are fed on stdin" {
    do_data_migration
    assert_success
    # only the measurement bulk command still pipes; events and alarms are
    # handed one record each via --data, which cannot be corrupted by escaping
    assert_data_command_received_stdin "measurements createBulk"
    assert_flag_on_all_calls "--nullInput" "events create "
    assert_flag_on_all_calls "--nullInput" "alarms create "
}

@test "import: nothing else consumes stdin by accident" {
    do_data_migration
    assert_success
    assert_no_call_drained_stdin
}

@test "import: measurements are sent oldest first" {
    do_data_migration
    assert_success
    # c8y_LatestMeasurements keeps the last value received, not the newest
    run bash -c "jq -r '.time' '$OUT/data/2/measurements.jsonl' | LC_ALL=C sort -c && echo ordered"
    assert_output --partial "ordered"
}

@test "import: alarms are sent oldest first, cleared before still-open" {
    do_data_migration
    assert_success
    run bash -c "cut -f3 '$REC/alarms.log' | jq -r .status | tr '\n' ','"
    assert_output "CLEARED,CLEARED,ACTIVE,"
}

@test "import: no server managed property reaches the alarm body" {
    do_data_migration
    assert_success

    # the fake records the exact --data value, so this is the real body
    local body
    body=$(head -1 "$REC/alarms.log" | cut -f3)
    for field in id self creationTime lastUpdated count firstOccurrenceTime history source; do
        assert_equal "$(printf '%s' "$body" | jq -r --arg f "$field" 'has($f)')" "false"
    done
    # and what must survive, does
    assert_equal "$(printf '%s' "$body" | jq -r '.status')" "CLEARED"
    assert_equal "$(printf '%s' "$body" | jq -r '.severity')" "MAJOR"
    assert_equal "$(printf '%s' "$body" | jq -r '.time')" "2026-01-01T00:00:00.000Z"
}

@test "import: no server managed property reaches the event body" {
    do_data_migration
    assert_success

    local body
    body=$(head -1 "$REC/events.log" | cut -f3)
    for field in id self creationTime lastUpdated c8y_IsBinary source; do
        assert_equal "$(printf '%s' "$body" | jq -r --arg f "$field" 'has($f)')" "false"
    done
    assert [ -n "$(printf '%s' "$body" | jq -r '.type')" ]
}

@test "import: an event attachment is uploaded against the new event id" {
    do_data_migration
    assert_success
    # one upload, and the id must not be the source event id
    assert_equal "$(grep -c '' "$REC/binaries-uploaded.log")" "1"
    # assert on the import output before `run` replaces it
    assert_line --regexp '^Attached e2 to event [0-9]+ of Line A \([0-9]+\)$'

    local uploadedId
    uploadedId=$(awk '{print $1}' "$REC/binaries-uploaded.log")
    refute [ "$uploadedId" = "e2" ]
    assert_equal "$(printf '%s' "$uploadedId" | grep -c '^[0-9][0-9]*$')" "1"
}

@test "import: measurements and events use QUIESCENT, alarms cannot" {
    do_data_migration
    assert_success
    assert_flag_on_all_calls "QUIESCENT" "measurements createBulk"
    # trailing space: "events create" would also match "events createBinary"
    assert_flag_on_all_calls "QUIESCENT" "events create "
    # QUIESCENT is documented as measurements and events only
    assert_flag_on_no_calls "--processingMode" "alarms create"
}

@test "import: createBinary passes --nullInput because it runs inside a loop" {
    do_data_migration
    assert_success
    assert_flag_on_all_calls "--nullInput" "events createBinary"
}

@test "import: --force reaches the data creates" {
    do_data_migration
    assert_success
    assert_flag_on_all_calls "--force" "measurements createBulk"
    assert_flag_on_all_calls "--force" "alarms create"
}

@test "import: an export without data imports none" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    run_import --input-directory "$OUT" --force
    assert_success
    assert_equal "$(count_calls "measurements createBulk")" "0"
    refute_output --partial "measurements,"
}

@test "import: the summary totals the data" {
    do_data_migration
    assert_success
    assert_line "Imported 4 measurements, 2 events and 3 alarms, including 1 attachment"
}

@test "import: a claimed attachment missing from the export is reported" {
    setup_data_export
    rm "$OUT/data/2/binaries/e2"
    run_import --input-directory "$OUT" --force
    assert_success
    assert_output --partial "claimed an attachment but none was exported"
    assert_equal "$(ls "$REC" | grep -c 'binaries-uploaded' || true)" "0"
}

@test "import: the data templates keep source so --device can overwrite it" {
    do_data_migration
    assert_success

    # --device maps onto source.id, so hiding source removes the very field it
    # would overwrite and the request fails with
    # "Body is missing required properties: source.id"
    local alarmTemplate eventTemplate
    alarmTemplate=$(head -1 "$REC/alarms.log" | cut -f4)
    eventTemplate=$(head -1 "$REC/events.log" | cut -f4)
    assert_equal "$(printf '%s' "$alarmTemplate" | grep -c "source'*::" || true)" "0"
    assert_equal "$(printf '%s' "$eventTemplate" | grep -c "source'*::" || true)" "0"

    # and the new device id is supplied on every data create
    assert_flag_on_all_calls "--device" "alarms create "
    assert_flag_on_all_calls "--device" "events create "
    assert_flag_on_all_calls "--device" "measurements createBulk "
}


# --- record integrity through the ordering step -------------------------------
#
# Records are sorted by time before being sent. jq's @tsv escapes backslashes in
# the field it emits, so a record whose text holds escaped JSON came back out
# with every backslash doubled and was no longer valid JSON. That corrupted the
# record before c8y ever saw it and killed the whole import with
# "Could not lex the character". Alarm a3 in the fixture has that shape.

@test "import: records with escaped JSON survive the chronological sort" {
    setup_data_export
    local file="$OUT/data/2/alarms.jsonl"
    assert [ -f "$file" ]

    # every record the sort emits must still be valid JSON
    local line bad=0 total=0
    while IFS= read -r line; do
        total=$((total + 1))
        printf '%s' "$line" | jq -e . > /dev/null 2>&1 || bad=$((bad + 1))
    done < <(sed -n '/^sort_by_time()/,/^}/p' "$REPO_DIR/commands/import" \
        | { cat; printf 'sort_by_time "%s"\n' "$file"; } | bash)

    assert_equal "$bad" "0"
    assert_equal "$total" "3"
}

@test "import: an awkward alarm text reaches the create body unchanged" {
    do_data_migration
    assert_success

    # the record carrying escaped JSON was sent, and its text is intact
    local sent expected
    expected=$(jq -r 'select(.id=="a3") | .text' "$OUT/data/2/alarms.jsonl")
    sent=$(cut -f3 "$REC/alarms.log" | jq -r 'select(.type=="c8y_RuleFailed") | .text')
    assert_equal "$sent" "$expected"
    assert [ -n "$sent" ]
}

@test "import: every alarm is created even when one holds escaped JSON" {
    do_data_migration
    assert_success
    assert_equal "$(grep -c '' "$REC/alarms.log")" "3"
    assert_line --partial "Imported 3 alarms for Line A"
}

# --- a data failure is not a success ----------------------------------------
#
# The data commands deliberately keep going when one record is refused, so that
# a single bad record does not cost a whole migration. The exit code has to say
# so anyway: a caller that reads zero here treats a part-delivered device as
# finished, which is how a migration silently loses records.

@test "import: a refused data record makes the import fail" {
    run_export --rootId 1 --output-directory "$OUT" --dataTypes measurements,events,alarms
    assert_success
    FAKE_C8Y_FAIL_DATA_AFTER=1 run_import --input-directory "$OUT" --force
    assert_failure
    assert_output --partial "could not be imported, so this import is incomplete"
}

@test "import: what did arrive is still reported and still mapped" {
    run_export --rootId 1 --output-directory "$OUT" --dataTypes measurements,events,alarms
    assert_success
    FAKE_C8Y_FAIL_DATA_AFTER=1 run_import --input-directory "$OUT" --force
    assert_failure
    # the objects were created, so the mapping is complete and revert works
    assert_equal "$(jq 'length' id_mapping.json)" "9"
    assert_output --partial "Imported Plant Berlin"
}

@test "import: the failure count covers every kind of record" {
    run_export --rootId 1 --output-directory "$OUT" --dataTypes measurements,events,alarms
    assert_success
    FAKE_C8Y_FAIL_DATA_AFTER=0 run_import --input-directory "$OUT" --force
    assert_failure
    # 4 measurements, 2 events, 3 alarms in the fixture, and the attachment
    # belongs to an event that never got created
    assert_output --partial "9 records could not be imported"
}

@test "import: a clean data import still exits zero" {
    run_export --rootId 1 --output-directory "$OUT" --dataTypes measurements,events,alarms
    assert_success
    run_import --input-directory "$OUT" --force
    assert_success
    refute_output --partial "could not be imported"
}

# --- announcing the run id ---------------------------------------------------
#
# Announced by whatever applies it, so the id ends up in the log of the run that
# used it. Otherwise the only way to recover it is to read it back off a
# migrated object's fragments, which is a poor thing to discover during an
# incident.

@test "import: the run id is announced when objects are marked" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    run_import --input-directory "$OUT" --force --runId RUN_ABC
    assert_success
    assert_line "Run id RUN_ABC, marking created objects with c8y_DeviceMigration_RUN_ABC"
}

@test "import: the run id is announced before the first object is created" {
    # so an import that dies part way still reported which run it was
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    run_import --input-directory "$OUT" --force --runId RUN_ABC
    assert_success

    local runIdLine firstImport
    runIdLine=$(printf '%s\n' "$output" | grep -n '^Run id ' | head -1 | cut -d: -f1)
    firstImport=$(printf '%s\n' "$output" | grep -n '^Imported ' | head -1 | cut -d: -f1)
    assert [ "$runIdLine" -lt "$firstImport" ]
}

@test "import: a run id is generated and reported when none is given" {
    # Without this the only record of an import is its mapping file, and a lost
    # mapping means a lost migration.
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    run_import --input-directory "$OUT" --force
    assert_success
    assert_line --regexp '^Run id [0-9]{8}T[0-9]{6}Z_[0-9a-f]{6}, marking created objects with c8y_DeviceMigration_[0-9]{8}T[0-9]{6}Z_[0-9a-f]{6}$'

    # and the reported id really is the one on the objects
    local reported
    reported=$(printf '%s\n' "$output" | sed -n 's/^Run id \([^,]*\),.*/\1/p')
    assert_equal "$(grep -l "c8y_DeviceMigration_$reported" "$REC"/payloads/*.json | grep -c '')" "9"
}

@test "import: --noFlag marks nothing and announces nothing" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    run_import --input-directory "$OUT" --force --noFlag
    assert_success
    refute_output --partial "Run id"
    assert_equal "$(jq -r '[keys[] | select(startswith("c8y_DeviceMigration_"))] | length' \
        "$REC"/payloads/1001.json)" "0"
}

@test "import: --dataOnly announces no run id, because it marks nothing" {
    run_export --rootId 1 --output-directory "$OUT" --dataTypes measurements
    assert_success
    run_import --input-directory "$OUT" --force --runId RUN_ABC
    assert_success

    run_import --input-directory "$OUT" --force --runId RUN_ABC --dataOnly
    assert_success
    refute_output --partial "Run id"
}

# --- iteration order must not depend on the locale --------------------------

# How a locale orders the pair that the child index file names run into: '10_a'
# first under C, because '0' sorts before '_', and '1_b' first wherever
# punctuation is ignored at the primary level.
collation_order() {
    printf '1_b\n10_a\n' | LC_ALL="$1" sort | head -1
}

@test "import: relationships are assigned in the same order under any locale" {
    # Bash orders a glob by the LOCALE's collation, and most locales ignore
    # punctuation at the primary level: '1_child_asset.json' sorts before
    # '10_child_device.json' under en_US and after it under C. That made the
    # output order depend on the user's environment, which is how it was found -
    # the same test passed locally and failed on a CI runner.
    run_export --rootId 1 --output-directory "$OUT"
    assert_success

    # A locale that is not installed falls back to C, and then this test
    # compares C against C three times and proves nothing. Keep only the
    # locales that actually order the ambiguous pair differently.
    local locales="C" candidate
    for candidate in en_US.UTF-8 de_DE.UTF-8; do
        if [[ "$(collation_order "$candidate")" != "$(collation_order C)" ]]; then
            locales="$locales $candidate"
        fi
    done
    if [[ "$locales" == "C" ]]; then
        skip "no locale on this machine collates 1_x and 10_x differently from C"
    fi

    local first=""
    for locale in $locales; do
        LC_ALL="$locale" run_import --input-directory "$OUT" --force --noFlag \
            --mapping "mapping-$locale.json"
        assert_success

        local order
        order=$(printf '%s\n' "$output" | grep '^Assigned ' | sed 's/([0-9]*)//g')
        if [[ -z "$first" ]]; then
            first="$order"
        else
            assert_equal "$order" "$first"
        fi
    done
}
