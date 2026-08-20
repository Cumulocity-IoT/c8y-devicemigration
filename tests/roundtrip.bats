#!/usr/bin/env bats
#
# Export then import against the fake tenant. This is the test that fails if
# either side regresses, and the one that would have caught the production bug
# where only the first child of each parent was assigned.

load helpers/load

setup() {
    setup_common
    cd "$WORK"
}

# Translate the exported references through the id mapping so they can be
# compared against the links the import actually created.
expected_links() {
    local dir="$1" mapping="$2"
    export_refs "$dir" | while read -r parent childType child; do
        printf '%s %s %s\n' \
            "$(jq -r --arg k "$parent" '.[$k]' "$mapping")" \
            "$childType" \
            "$(jq -r --arg k "$child" '.[$k]' "$mapping")"
    done | sort
}

@test "roundtrip: the imported tree matches the exported tree" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success

    run_import --input-directory "$OUT" --force
    assert_success

    # every exported object was recreated
    assert_equal "$(jq 'length' id_mapping.json)" "9"
    # and every relationship was rebuilt, under the new ids
    assert_equal "$(assigned_links)" "$(expected_links "$OUT" id_mapping.json)"
    assert_equal "$(count_links)" "10"
}

@test "roundtrip: including additions carries the whole tree across" {
    run_export --rootId 1 --output-directory "$OUT" --childTypes asset,device,addition
    assert_success

    run_import --input-directory "$OUT" --force
    assert_success

    assert_equal "$(jq 'length' id_mapping.json)" "11"
    assert_equal "$(assigned_links)" "$(expected_links "$OUT" id_mapping.json)"
    assert_equal "$(count_links)" "12"
}

@test "roundtrip: through a compressed archive" {
    run_export --rootId 1 --output-compressed "$WORK/mig.tgz"
    assert_success

    run_import --input-compressed "$WORK/mig.tgz" --force
    assert_success

    # compare against the same tree exported uncompressed
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    assert_equal "$(assigned_links)" "$(expected_links "$OUT" id_mapping.json)"
}

@test "roundtrip: --no-children migrates a single object and no links" {
    run_export --rootId 1 --output-directory "$OUT" --no-children
    assert_success

    run_import --input-directory "$OUT" --force
    assert_success

    assert_equal "$(jq 'length' id_mapping.json)" "1"
    assert_equal "$(count_links)" "0"
}

@test "roundtrip: a cycle does not produce duplicate objects or links" {
    # 1 -> 2 -> 5 -> 2 closes a loop, and 4 has two parents
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    run_import --input-directory "$OUT" --force
    assert_success

    # each object created exactly once
    assert_equal "$(count_calls "inventory create")" "9"
    # each link assigned exactly once
    assert_equal "$(assigned_links | sort -u | grep -c '')" "10"
}

@test "roundtrip: no c8y call anywhere consumed stdin" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    run_import --input-directory "$OUT" --force
    assert_success
    assert_no_call_drained_stdin
}

@test "roundtrip: external identities survive export and import" {
    run_export --rootId 1 --output-directory "$OUT"
    assert_success
    run_import --input-directory "$OUT" --force
    assert_success

    # every identity in the export was recreated, bound to the new object id
    local expected
    expected=$(for f in "$OUT"/*_identities.json; do
        local old newId
        old=$(basename "$f" _identities.json)
        newId=$(jq -r --arg k "$old" '.[$k]' id_mapping.json)
        jq -r --arg n "$newId" '.[] | "\($n) \(.type) \(.externalId)"' "$f"
    done | sort)
    assert_equal "$(sort "$REC/identities.log")" "$expected"
    assert_equal "$(printf '%s\n' "$expected" | grep -c '')" "4"
}
