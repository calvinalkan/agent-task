package testutil

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/calvinalkan/agent-task/internal/testutil/spec"
)

// RunConfig configures a behavior test run.
type RunConfig struct {
	// MaxOps is the maximum number of operations to execute.
	MaxOps int

	// CompareStateEveryN runs full state comparison every N operations.
	// Set to 0 to disable periodic checks (only check at end).
	CompareStateEveryN int

	// CompareOutputs enables output comparison for ls/ready operations.
	// When false, only success/failure is compared.
	CompareOutputs bool
}

// DefaultRunConfig returns a balanced configuration for behavior tests.
func DefaultRunConfig() RunConfig {
	return RunConfig{
		MaxOps:             100,
		CompareStateEveryN: 10,
		CompareOutputs:     true,
	}
}

// RunBehavior executes a deterministic stream of operations and compares
// the behavior between the spec model and the real CLI.
func RunBehavior(tb testing.TB, cfg RunConfig, gen *OpGenerator) {
	tb.Helper()

	if cfg.MaxOps <= 0 {
		tb.Fatalf("RunBehavior requires MaxOps > 0")
	}

	h := NewHarness(tb)
	history := make([]string, 0, cfg.MaxOps)

	for opIndex := 1; opIndex <= cfg.MaxOps && gen.HasMore(); opIndex++ {
		op := gen.NextOp()
		history = append(history, op.String())

		realRes := op.ApplyReal(h)
		modelRes := op.ApplyModel(h)

		err := compareResults(op, &modelRes, &realRes, cfg.CompareOutputs)
		if err != nil {
			tb.Fatalf("%v\n%s", err, FormatOps(history))
		}

		if cfg.CompareStateEveryN > 0 && opIndex%cfg.CompareStateEveryN == 0 {
			err := CompareState(h, history)
			if err != nil {
				tb.Fatal(err)
			}
		}
	}

	err := CompareState(h, history)
	if err != nil {
		tb.Fatal(err)
	}
}

// RunBehaviorWithSeed runs behavior tests with a specific byte seed.
func RunBehaviorWithSeed(tb testing.TB, seed []byte, cfg RunConfig) {
	tb.Helper()

	h := NewHarness(tb)
	genCfg := DefaultOpGenConfig()
	gen := NewOpGenerator(seed, h.Model, &genCfg)
	history := make([]string, 0, cfg.MaxOps)

	for opIndex := 1; opIndex <= cfg.MaxOps && gen.HasMore(); opIndex++ {
		op := gen.NextOp()
		history = append(history, op.String())

		realRes := op.ApplyReal(h)
		modelRes := op.ApplyModel(h)

		err := compareResults(op, &modelRes, &realRes, cfg.CompareOutputs)
		if err != nil {
			tb.Fatalf("%v\n%s", err, FormatOps(history))
		}

		if cfg.CompareStateEveryN > 0 && opIndex%cfg.CompareStateEveryN == 0 {
			err := CompareState(h, history)
			if err != nil {
				tb.Fatal(err)
			}
		}
	}

	err := CompareState(h, history)
	if err != nil {
		tb.Fatal(err)
	}
}

// compareResults compares model and real results.
func compareResults(op Op, modelRes, realRes *Result, compareOutputs bool) error {
	if modelRes.OK != realRes.OK {
		if modelRes.OK {
			return fmt.Errorf("model succeeded but CLI failed: %s, stderr: %s", op.String(), realRes.Stderr)
		}

		return fmt.Errorf("model failed but CLI succeeded: %s, model error: %w", op.String(), modelRes.Err)
	}

	if !modelRes.OK {
		var specErr *spec.Error

		_ = errors.As(modelRes.Err, &specErr)
		if !MatchesErrorBucket(specErr, realRes.Stderr) {
			return fmt.Errorf("error bucket mismatch: %s, spec error: %w, stderr: %s",
				op.String(), modelRes.Err, realRes.Stderr)
		}

		return nil
	}

	if compareOutputs && modelRes.Value != nil && realRes.Value != nil {
		if !valuesEqual(modelRes.Value, realRes.Value) {
			return fmt.Errorf("output mismatch: %s, model: %v, real: %v",
				op.String(), modelRes.Value, realRes.Value)
		}
	}

	return nil
}

// valuesEqual compares two result values.
func valuesEqual(a, b any) bool {
	if aStr, ok := a.(string); ok {
		if bStr, ok := b.(string); ok {
			return aStr == bStr
		}
	}

	if aSlice, ok := a.([]string); ok {
		if bSlice, ok := b.([]string); ok {
			return slices.Equal(aSlice, bSlice)
		}
	}

	return true
}
