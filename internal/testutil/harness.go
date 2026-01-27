package testutil

import (
	"testing"

	"github.com/calvinalkan/agent-task/internal/cli"
	"github.com/calvinalkan/agent-task/internal/testutil/spec"
)

// Harness wires together the real CLI and the spec model.
//
// This is intentionally small: it exists to share setup and provide
// a single place to hang helper methods for behavior tests.
type Harness struct {
	TB    testing.TB
	CLI   *cli.CLI
	Model *spec.Model
	Clock *Clock
}

// NewHarness creates a new behavior test harness.
//
// Note: cli.NewCLI requires *testing.T, so this will fail if called
// with a non-*testing.T implementation.
func NewHarness(tb testing.TB) *Harness {
	tb.Helper()

	t, ok := tb.(*testing.T)
	if !ok {
		tb.Fatalf("testutil.NewHarness requires *testing.T, got %T", tb)
	}

	return &Harness{
		TB:    tb,
		CLI:   cli.NewCLI(t),
		Model: spec.New(),
		Clock: NewClock(),
	}
}

// TicketDir returns the ticket directory path used by the real CLI.
func (h *Harness) TicketDir() string {
	return h.CLI.TicketDir()
}

// Apply runs the operation against the real CLI first (so it can capture
// CLI-generated IDs/timestamps), then applies it to the model.
func (h *Harness) Apply(op Op) (Result, Result) {
	realRes := op.ApplyReal(h)
	modelRes := op.ApplyModel(h)

	return modelRes, realRes
}
