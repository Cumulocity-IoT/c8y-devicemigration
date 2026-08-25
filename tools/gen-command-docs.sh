#!/usr/bin/env bash
#
# Regenerates the command reference in docs/extension.md from the commands
# themselves.
#
# The option lists live in the DOC string of each command, which is also what
# --help prints and what the generated parser is checked against. Copying them
# into the README by hand is how a README starts lying: it has happened twice
# already in this repo's short life.
#
# Usage:
#   tools/gen-command-docs.sh          rewrite docs/extension.md in place
#   tools/gen-command-docs.sh --check  exit 1 if it is out of date

set -e

repoDirectory="$(cd "$(dirname "$0")/.." && pwd)"
# The reference documents the bash commands, so it lives with them: --help is
# what it prints, and docs/extension.md is what documents that.
readme="$repoDirectory/docs/extension.md"
beginMarker="<!-- BEGIN COMMAND REFERENCE -->"
endMarker="<!-- END COMMAND REFERENCE -->"

# The order the reference reads in: the one-command path first, then the pieces
# it is built from, then the way back out.
COMMANDS="migrate export import revert"

generate() {
    printf '%s\n' "$beginMarker"
    echo
    echo "Generated from the commands themselves by \`tools/gen-command-docs.sh\`."
    echo "Run \`make docs\` after changing an option, or \`make check\` will say so."

    local name
    for name in $COMMANDS; do
        echo
        echo "### \`$name\`"
        echo
        echo '```'
        bash "$repoDirectory/commands/$name" --help
        echo '```'
    done

    echo
    printf '%s\n' "$endMarker"
}

if [[ ! -f "$readme" ]]; then
    echo "No such file: $readme" >&2
    exit 1
fi

for marker in "$beginMarker" "$endMarker"; do
    if ! grep -qxF "$marker" "$readme"; then
        echo "$readme is missing the line: $marker" >&2
        exit 1
    fi
done

# Everything before the begin marker, the generated block, everything after the
# end marker.
rebuilt=$(mktemp "${TMPDIR:-/tmp}/c8y-devicemigration-docs.XXXXXXXX")
trap 'rm -f "$rebuilt"' EXIT

awk -v begin="$beginMarker" '$0 == begin { exit } { print }' "$readme" > "$rebuilt"
generate >> "$rebuilt"
awk -v end="$endMarker" 'found { print } $0 == end { found = 1 }' "$readme" >> "$rebuilt"

if [[ "${1:-}" == "--check" ]]; then
    if cmp -s "$rebuilt" "$readme"; then
        echo "command reference: up to date"
        exit 0
    fi
    echo "The command reference in $readme is out of date, run 'make docs'" >&2
    diff "$readme" "$rebuilt" | head -40 >&2
    exit 1
fi

cat "$rebuilt" > "$readme"
echo "command reference regenerated in $readme"
