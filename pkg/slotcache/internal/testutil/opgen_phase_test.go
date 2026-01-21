package testutil_test

import (
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

func Test_OpGenerator_Returns_Fill_Phase_When_Cache_Is_Empty(t *testing.T) {
	t.Parallel()

	cfg := testutil.PhasedOpGenConfig()
	opts := slotcache.Options{
		Path:         t.TempDir() + "/test.slc",
		KeySize:      8,
		SlotCapacity: 100,
	}

	gen := testutil.NewOpGenerator([]byte{0, 0, 0, 0}, opts, &cfg)
	got := gen.CurrentPhase(0)

	if got != testutil.PhaseFill {
		t.Errorf("CurrentPhase(0) = %v, want PhaseFill", got)
	}
}

func Test_OpGenerator_Returns_Fill_Phase_When_Cache_Is_Partially_Filled(t *testing.T) {
	t.Parallel()

	cfg := testutil.PhasedOpGenConfig()
	opts := slotcache.Options{
		Path:         t.TempDir() + "/test.slc",
		KeySize:      8,
		SlotCapacity: 100,
	}

	gen := testutil.NewOpGenerator([]byte{0, 0, 0, 0}, opts, &cfg)

	// 30% filled should still be in Fill phase (threshold is 60%)
	got := gen.CurrentPhase(30)

	if got != testutil.PhaseFill {
		t.Errorf("CurrentPhase(30) = %v, want PhaseFill", got)
	}
}

func Test_OpGenerator_Returns_Churn_Phase_When_Cache_Reaches_Fill_Threshold(t *testing.T) {
	t.Parallel()

	cfg := testutil.PhasedOpGenConfig()
	opts := slotcache.Options{
		Path:         t.TempDir() + "/test.slc",
		KeySize:      8,
		SlotCapacity: 100,
	}

	gen := testutil.NewOpGenerator([]byte{0, 0, 0, 0}, opts, &cfg)

	// 60% filled is the boundary - should be Churn phase
	got := gen.CurrentPhase(60)

	if got != testutil.PhaseChurn {
		t.Errorf("CurrentPhase(60) = %v, want PhaseChurn", got)
	}
}

func Test_OpGenerator_Returns_Churn_Phase_When_Cache_Is_Moderately_Filled(t *testing.T) {
	t.Parallel()

	cfg := testutil.PhasedOpGenConfig()
	opts := slotcache.Options{
		Path:         t.TempDir() + "/test.slc",
		KeySize:      8,
		SlotCapacity: 100,
	}

	gen := testutil.NewOpGenerator([]byte{0, 0, 0, 0}, opts, &cfg)

	// 70% filled should be in Churn phase
	got := gen.CurrentPhase(70)

	if got != testutil.PhaseChurn {
		t.Errorf("CurrentPhase(70) = %v, want PhaseChurn", got)
	}
}

func Test_OpGenerator_Returns_Read_Phase_When_Cache_Reaches_Churn_Threshold(t *testing.T) {
	t.Parallel()

	cfg := testutil.PhasedOpGenConfig()
	opts := slotcache.Options{
		Path:         t.TempDir() + "/test.slc",
		KeySize:      8,
		SlotCapacity: 100,
	}

	gen := testutil.NewOpGenerator([]byte{0, 0, 0, 0}, opts, &cfg)

	// 85% filled is the boundary - should be Read phase
	got := gen.CurrentPhase(85)

	if got != testutil.PhaseRead {
		t.Errorf("CurrentPhase(85) = %v, want PhaseRead", got)
	}
}

func Test_OpGenerator_Returns_Read_Phase_When_Cache_Is_Nearly_Full(t *testing.T) {
	t.Parallel()

	cfg := testutil.PhasedOpGenConfig()
	opts := slotcache.Options{
		Path:         t.TempDir() + "/test.slc",
		KeySize:      8,
		SlotCapacity: 100,
	}

	gen := testutil.NewOpGenerator([]byte{0, 0, 0, 0}, opts, &cfg)

	// 95% filled should be in Read phase
	got := gen.CurrentPhase(95)

	if got != testutil.PhaseRead {
		t.Errorf("CurrentPhase(95) = %v, want PhaseRead", got)
	}
}

func Test_OpGenerator_Returns_Read_Phase_When_Cache_Is_Overfilled(t *testing.T) {
	t.Parallel()

	cfg := testutil.PhasedOpGenConfig()
	opts := slotcache.Options{
		Path:         t.TempDir() + "/test.slc",
		KeySize:      8,
		SlotCapacity: 100,
	}

	gen := testutil.NewOpGenerator([]byte{0, 0, 0, 0}, opts, &cfg)

	// 150% filled (overfilled) should be in Read phase
	got := gen.CurrentPhase(150)

	if got != testutil.PhaseRead {
		t.Errorf("CurrentPhase(150) = %v, want PhaseRead", got)
	}
}

func Test_OpGenerator_Handles_Small_Capacity_When_Calculating_Phase(t *testing.T) {
	t.Parallel()

	cfg := testutil.PhasedOpGenConfig()
	opts := slotcache.Options{
		Path:         t.TempDir() + "/test.slc",
		KeySize:      8,
		SlotCapacity: 4,
	}

	gen := testutil.NewOpGenerator([]byte{0, 0, 0, 0}, opts, &cfg)

	// 2/4 = 50% < 60% -> Fill
	if got := gen.CurrentPhase(2); got != testutil.PhaseFill {
		t.Errorf("CurrentPhase(2) with cap=4 = %v, want PhaseFill", got)
	}

	// 3/4 = 75% >= 60%, < 85% -> Churn
	if got := gen.CurrentPhase(3); got != testutil.PhaseChurn {
		t.Errorf("CurrentPhase(3) with cap=4 = %v, want PhaseChurn", got)
	}

	// 4/4 = 100% >= 85% -> Read
	if got := gen.CurrentPhase(4); got != testutil.PhaseRead {
		t.Errorf("CurrentPhase(4) with cap=4 = %v, want PhaseRead", got)
	}
}

func Test_OpGenerator_Returns_Churn_Phase_When_Phased_Generation_Is_Disabled(t *testing.T) {
	t.Parallel()

	// DefaultOpGenConfig has phased generation disabled
	cfg := testutil.DefaultOpGenConfig()

	opts := slotcache.Options{
		Path:         t.TempDir() + "/test.slc",
		KeySize:      8,
		SlotCapacity: 100,
	}

	gen := testutil.NewOpGenerator([]byte{0, 0, 0, 0}, opts, &cfg)

	// Regardless of fill level, disabled phased generation returns PhaseChurn.
	for _, seenCount := range []int{0, 30, 60, 85, 100, 150} {
		got := gen.CurrentPhase(seenCount)
		if got != testutil.PhaseChurn {
			t.Errorf("CurrentPhase(%d) with disabled phasing = %v, want PhaseChurn", seenCount, got)
		}
	}
}

func Test_Phase_Returns_Correct_String_When_Converted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		phase testutil.Phase
		want  string
	}{
		{testutil.PhaseFill, "Fill"},
		{testutil.PhaseChurn, "Churn"},
		{testutil.PhaseRead, "Read"},
		{testutil.Phase(99), "Unknown"},
	}

	for _, tt := range tests {
		if got := tt.phase.String(); got != tt.want {
			t.Errorf("Phase(%d).String() = %q, want %q", tt.phase, got, tt.want)
		}
	}
}

func Test_PhasedOpGenConfig_Has_Expected_Defaults_When_Created(t *testing.T) {
	t.Parallel()

	cfg := testutil.PhasedOpGenConfig()

	if !cfg.PhasedEnabled {
		t.Error("PhasedOpGenConfig should have PhasedEnabled=true")
	}

	if cfg.FillPhaseEnd != 60 {
		t.Errorf("FillPhaseEnd = %d, want 60", cfg.FillPhaseEnd)
	}

	if cfg.ChurnPhaseEnd != 85 {
		t.Errorf("ChurnPhaseEnd = %d, want 85", cfg.ChurnPhaseEnd)
	}

	if cfg.FillPhaseBeginWriteRate != 50 {
		t.Errorf("FillPhaseBeginWriteRate = %d, want 50", cfg.FillPhaseBeginWriteRate)
	}

	if cfg.FillPhaseCommitRate != 8 {
		t.Errorf("FillPhaseCommitRate = %d, want 8", cfg.FillPhaseCommitRate)
	}

	if cfg.ChurnPhaseDeleteRate != 35 {
		t.Errorf("ChurnPhaseDeleteRate = %d, want 35", cfg.ChurnPhaseDeleteRate)
	}

	if cfg.ReadPhaseBeginWriteRate != 5 {
		t.Errorf("ReadPhaseBeginWriteRate = %d, want 5", cfg.ReadPhaseBeginWriteRate)
	}
}
