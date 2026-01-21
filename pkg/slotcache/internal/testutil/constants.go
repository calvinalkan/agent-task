package testutil

// DefaultMaxFuzzOperations is the default maximum number of operations
// to run in a single fuzz iteration or deterministic behavior test.
//
// This value is shared across behavior fuzz tests, guard tests, and seed
// helpers to ensure consistent operation counts when validating seeds.
//
// The value of 200 provides enough depth to exercise multi-operation
// sequences (writer sessions, scan iterations, close/reopen cycles) while
// keeping individual fuzz iterations fast enough for good throughput.
const DefaultMaxFuzzOperations = 200
