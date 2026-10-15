#!/usr/bin/env bash
#
# Regenerates the command reference in docs/cli.md from the binary itself.
#
# The option lists live in the commands' own help text, which is what --help
# prints. Copying them into the docs by hand is how documentation starts lying,
# so the reference is generated between two markers and `make check` fails when
# it is out of date.
#
# Usage:
#   tools/gen-command-docs.sh          rewrite docs/cli.md in place
#   tools/gen-command-docs.sh --check  exit 1 if it is out of date

set -e

repoDirectory="$(cd "$(dirname "$0")/.." && pwd)"
doc="$repoDirectory/docs/cli.md"
beginMarker="<!-- BEGIN COMMAND REFERENCE -->"
endMarker="<!-- END COMMAND REFERENCE -->"

# The order the reference reads in: the one-command path first, then the pieces
# it is built from, then the way back out.
COMMANDS="migrate preflight export register import revert"

work=$(mktemp -d "${TMPDIR:-/tmp}/c8y-devicemigration-docs.XXXXXXXX")
trap 'rm -rf "$work"' EXIT

# Built rather than run with `go run`, so the reference documents exactly what a
# build of this tree prints, and the toolchain is asked once rather than per
# command.
binary="$work/c8y-devicemigration"
(cd "$repoDirectory" && go build -o "$binary" ./cmd/c8y-devicemigration)

generate() {
    printf '%s\n' "$beginMarker"
    echo
    echo "Generated from the binary's own help by \`tools/gen-command-docs.sh\`."
    echo "Run \`make docs\` after changing a command or an option, or \`make check\` will say so."
    echo
    echo '```'
    "$binary" --help
    echo '```'

    local name
    for name in $COMMANDS; do
        echo
        echo "### \`$name\`"
        echo
        echo '```'
        "$binary" "$name" --help
        echo '```'
    done

    echo
    printf '%s\n' "$endMarker"
}

for marker in "$beginMarker" "$endMarker"; do
    if ! grep -qxF "$marker" "$doc"; then
        echo "$doc is missing the line: $marker" >&2
        exit 1
    fi
done

# Everything before the begin marker, the generated block, everything after the
# end marker.
rebuilt="$work/cli.md"
awk -v begin="$beginMarker" '$0 == begin { exit } { print }' "$doc" > "$rebuilt"
generate >> "$rebuilt"
awk -v end="$endMarker" 'found { print } $0 == end { found = 1 }' "$doc" >> "$rebuilt"

if [[ "${1:-}" == "--check" ]]; then
    if cmp -s "$rebuilt" "$doc"; then
        echo "command reference: up to date"
        exit 0
    fi
    echo "The command reference in $doc is out of date, run 'make docs'" >&2
    diff "$doc" "$rebuilt" | head -40 >&2
    exit 1
fi

cat "$rebuilt" > "$doc"
echo "command reference regenerated in $doc"
