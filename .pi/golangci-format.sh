#!/bin/bash
# Formats golangci-lint JSON output for agent consumption
#
# Usage: golangci-lint run --output.json.path stdout ... | ./golangci-format.sh
#
# Exit codes:
#   0 - Successfully formatted (regardless of issue count)
#   1 - Invalid or empty input
#
set -euo pipefail

input=$(cat)

# Validate we got something
if [ -z "$input" ]; then
  echo "Error: empty input" >&2
  exit 1
fi

# Validate it's JSON
if ! echo "$input" | jq -e . >/dev/null 2>&1; then
  echo "Error: invalid JSON input" >&2
  exit 1
fi

# Format the output
echo "$input" | jq -r '
  def clean: gsub("\n"; " ") | gsub("  +"; " ") | gsub("^ +| +$"; "");

  if .Issues == null or (.Issues | length) == 0 then
    "0 issues."
  else
    # Top-level stats
    (.Issues | length | tostring) + " issues: " +
    (.Issues | group_by(.FromLinter) | map({l: .[0].FromLinter, c: length}) | sort_by(-.c) | map("\(.l)(\(.c))") | join(", ")) +
    "\n\n" +
    # Per-file, sorted by line
    (
      .Issues
      | group_by(.Pos.Filename)
      | map(
          "## \(.[0].Pos.Filename)\n" +
          (sort_by(.Pos.Line) | map("  ->\(.Pos.Line):\(.Pos.Column) [\(.FromLinter)] \(.Text | clean)") | join("\n"))
        )
      | join("\n\n")
    )
  end
'
