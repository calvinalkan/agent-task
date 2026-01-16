0a. Study `@pkg/slotcache/specs/*` to learn the application specifications.
0b. Study @IMPLEMENTATION_PLAN.md (if present) to understand the plan so far.
0c. Study `@pkg/slotcache/internal/testutil/*` to understand shared utilities & test helpers.
0d. For reference, the application source code is in `@pkg/slotcache/*`.

1. Study @IMPLEMENTATION_PLAN.md (if present; it may be incorrect) and study existing source code in `@pkg/slotcache/*` and compare it against `@pkg/slotcache/specs/*`. Analyze findings, prioritize tasks, and create/update @IMPLEMENTATION_PLAN.md as a bullet point list sorted in priority of items yet to be implemented. Ultrathink. Consider searching for minimal implementations, placeholders, skipped/flaky tests, and inconsistent patterns. Study @IMPLEMENTATION_PLAN.md to determine starting point for research and keep it up to date with items considered complete/incomplete.

IMPORTANT: Plan only. Do NOT implement anything. Do NOT assume functionality is missing; confirm with code search first. Treat `@pkg/slotcache/internal/testutil` as the project's standard library for shared utilities and test helpers. Prefer consolidated, idiomatic implementations there over ad-hoc copies.

ULTIMATE GOAL: Implement slot cache according to the specifications. `@pkg/slotcache/api.go` defines the final API shape for the Go implementation. The current codebase contains a stub/placeholder implementation (file-based, minimal) that exists only to make the test harness pass—do NOT treat it as canonical. The test harness should be sufficient to verify correctness. The goal is to replace the stub with a proper implementation using correct file formats, memory-mapped caching, and all spec-defined behaviors.
