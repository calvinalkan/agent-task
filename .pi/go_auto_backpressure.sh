#!/bin/bash
# Backpressure checks for Go files
#
# Usage: ./go_backpressure.sh file1.go file2.go ...
#
# Exit codes:
#   0 - No issues found
#   1 - Lint issues found (fixable)
#   2 - Script/tool error (missing deps, config error, etc.)
#
# Why we extract package directories instead of passing files directly:
#
#   Go's type checker needs the full package context. Running golangci-lint
#   on individual files fails with "undefined" errors because symbols from
#   other files in the same package aren't visible.
#
#   Example:
#     golangci-lint run ./internal/ticket/config.go
#     → "undefined: ErrConfigFileNotFound" (defined in errors.go)
#
#     golangci-lint run ./internal/ticket/
#     → Works correctly, full package context available
#
#   Each .go file's dirname IS its package directory (Go enforces this),
#   so we extract unique dirnames and lint those as packages.
#
# The --new --whole-files flags:
#
#   These flags tell golangci-lint to only report issues in files that have
#   uncommitted changes (unstaged or untracked). This ensures backpressure
#   only catches issues YOU introduced, not pre-existing technical debt.
#
#   - --new: Only check unstaged/untracked files
#   - --whole-files: Report issues anywhere in changed files (not just changed lines)
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Check dependencies upfront
for cmd in golangci-lint jq; do
  if ! command -v "$cmd" &>/dev/null; then
    echo "Error: $cmd is not installed" >&2
    exit 2
  fi
done

if [ $# -eq 0 ]; then
  echo "Usage: $0 <file.go> ..." >&2
  exit 2
fi

# Run custom lint scripts on specific files (these exit non-zero on failure)
./backpressure/no-lint-suppress.sh "$@"
./backpressure/test-naming.sh "$@"
./backpressure/test-helper-order.sh "$@"

# Convert files to unique package directories
packages=$(for f in "$@"; do dirname "$f"; done | sort -u)

# Run golangci-lint, capturing stdout and stderr separately
# We need stderr for error detection, but don't want it polluting output
stderr_file=$(mktemp)
trap 'rm -f "$stderr_file"' EXIT

# shellcheck disable=SC2086
json_output=$(golangci-lint run --new --whole-files --fix \
  --output.json.path stdout --show-stats=false \
  $packages 2>"$stderr_file") || true

# Check for empty output (tool crashed or missing)
if [ -z "$json_output" ]; then
  echo "Error: golangci-lint produced no output" >&2
  if [ -s "$stderr_file" ]; then
    echo "stderr:" >&2
    cat "$stderr_file" >&2
  fi
  exit 2
fi

# Validate JSON and check for Report.Error
if ! echo "$json_output" | jq -e . >/dev/null 2>&1; then
  echo "Error: golangci-lint produced invalid JSON" >&2
  echo "$json_output" | head -5 >&2
  exit 2
fi

# Check for analysis errors (e.g., typecheck failures)
report_error=$(echo "$json_output" | jq -r '.Report.Error // empty')
if [ -n "$report_error" ]; then
  echo "Error: golangci-lint analysis failed: $report_error" >&2
  exit 2
fi

# Format and output issues
formatted=$("$SCRIPT_DIR/golangci-format.sh" <<< "$json_output")
echo "$formatted"

# Exit 1 if issues were found
if [ "$formatted" != "0 issues." ]; then
  exit 1
fi
