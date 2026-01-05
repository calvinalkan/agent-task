---
schema_version: 1
id: d5dxpvr
status: open
blocked-by: [d5dwga0]
created: 2026-01-05T15:40:31Z
type: task
priority: 3
assignee: Calvin Alkan
---
# Re-enable testpackage linter after test cleanup

The `testpackage` linter is currently disabled in `.golangci.yml` as a temporary workaround. This linter enforces that test files use external test packages (`package foo_test` instead of `package foo`).

## Current state
- `testpackage` is excluded for all `_test.go` files in `.golangci.yml`
- Some tests legitimately need internal access (testing private functions)
- Some tests just share helper functions between test files

## After test cleanup (d5dwga0)
Once the TestRunner helper is in place and tests are refactored:
1. Remove `testpackage` from the exclusions in `.golangci.yml`
2. Convert remaining test files to use `_test` package suffix where possible
3. Use `export_test.go` pattern for tests that need internal access
4. Keep internal package tests only for tests that specifically test private implementation details
