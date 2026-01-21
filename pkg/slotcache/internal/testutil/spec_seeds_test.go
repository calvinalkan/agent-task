package testutil_test

import (
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

// Test_SpecSeedBuilder_Produces_Correct_ActionByte_When_BeginWrite verifies BeginWrite protocol.
func Test_SpecSeedBuilder_Produces_Correct_ActionByte_When_BeginWrite(t *testing.T) {
	t.Parallel()

	seed := testutil.NewSpecSeedBuilder(8, 4).BeginWrite().Build()
	if len(seed) != 1 {
		t.Fatalf("expected 1 byte, got %d", len(seed))
	}
	// BeginWrite: actionByte % 100 must be in [3, 23)
	action := seed[0] % 100
	if action < 3 || action >= 23 {
		t.Errorf("BeginWrite action %d not in [3, 23)", action)
	}
}

// Test_SpecSeedBuilder_Produces_Correct_ActionByte_When_Commit verifies Commit protocol.
func Test_SpecSeedBuilder_Produces_Correct_ActionByte_When_Commit(t *testing.T) {
	t.Parallel()

	seed := testutil.NewSpecSeedBuilder(8, 4).BeginWrite().Commit().Build()
	// BeginWrite (1 byte) + Commit (1 byte)
	if len(seed) != 2 {
		t.Fatalf("expected 2 bytes, got %d", len(seed))
	}
	// Commit: actionByte % 100 in [61, 75)
	action := seed[1] % 100
	if action < 61 || action >= 75 {
		t.Errorf("Commit action %d not in [61, 75)", action)
	}
}

// Test_SpecSeedBuilder_Produces_Correct_ActionByte_When_WriterClose verifies WriterClose protocol.
func Test_SpecSeedBuilder_Produces_Correct_ActionByte_When_WriterClose(t *testing.T) {
	t.Parallel()

	seed := testutil.NewSpecSeedBuilder(8, 4).BeginWrite().WriterClose().Build()
	if len(seed) != 2 {
		t.Fatalf("expected 2 bytes, got %d", len(seed))
	}
	// Writer.Close: actionByte % 100 in [75, 83)
	action := seed[1] % 100
	if action < 75 || action >= 83 {
		t.Errorf("WriterClose action %d not in [75, 83)", action)
	}
}

// Test_SpecSeedBuilder_Produces_Correct_ActionByte_When_Invalidate verifies Invalidate protocol.
func Test_SpecSeedBuilder_Produces_Correct_ActionByte_When_Invalidate(t *testing.T) {
	t.Parallel()

	seed := testutil.NewSpecSeedBuilder(8, 4).Invalidate().Build()
	if len(seed) != 1 {
		t.Fatalf("expected 1 byte, got %d", len(seed))
	}
	// Invalidate: actionByte % 100 in [23, 26)
	action := seed[0] % 100
	if action < 23 || action >= 26 {
		t.Errorf("Invalidate action %d not in [23, 26)", action)
	}
}

// Test_SpecSeedBuilder_Produces_Correct_ActionByte_When_SetUserHeaderFlags verifies SetUserHeaderFlags protocol.
func Test_SpecSeedBuilder_Produces_Correct_ActionByte_When_SetUserHeaderFlags(t *testing.T) {
	t.Parallel()

	seed := testutil.NewSpecSeedBuilder(8, 4).BeginWrite().SetUserHeaderFlags(0x1234).Build()
	// BeginWrite (1) + SetUserHeaderFlags (1 + 8 bytes for uint64)
	if len(seed) != 10 {
		t.Fatalf("expected 10 bytes, got %d", len(seed))
	}
	// SetUserHeaderFlags: actionByte % 100 in [83, 87)
	action := seed[1] % 100
	if action < 83 || action >= 87 {
		t.Errorf("SetUserHeaderFlags action %d not in [83, 87)", action)
	}
}

// Test_SpecSeedBuilder_Produces_Correct_ActionByte_When_SetUserHeaderData verifies SetUserHeaderData protocol.
func Test_SpecSeedBuilder_Produces_Correct_ActionByte_When_SetUserHeaderData(t *testing.T) {
	t.Parallel()

	seed := testutil.NewSpecSeedBuilder(8, 4).BeginWrite().SetUserHeaderData([]byte("test")).Build()
	// BeginWrite (1) + SetUserHeaderData (1 + slotcache.UserDataSize bytes)
	expectedLen := 1 + 1 + slotcache.UserDataSize
	if len(seed) != expectedLen {
		t.Fatalf("expected %d bytes, got %d", expectedLen, len(seed))
	}
	// SetUserHeaderData: actionByte % 100 in [87, 91)
	action := seed[1] % 100
	if action < 87 || action >= 91 {
		t.Errorf("SetUserHeaderData action %d not in [87, 91)", action)
	}
}

// Test_SpecSeedBuilder_Produces_Correct_ActionByte_When_Put verifies Put protocol.
func Test_SpecSeedBuilder_Produces_Correct_ActionByte_When_Put(t *testing.T) {
	t.Parallel()

	seed := testutil.NewSpecSeedBuilder(8, 4).
		BeginWrite().
		Put([]byte{1, 2, 3, 4, 5, 6, 7, 8}, 42, []byte{0xA, 0xB, 0xC, 0xD}).
		Build()
	// BeginWrite (1) + Put (1 + 1 mode + 8 key + 8 rev + 1 mode + 4 index)
	expectedLen := 1 + 1 + 1 + 8 + 8 + 1 + 4
	if len(seed) != expectedLen {
		t.Fatalf("expected %d bytes, got %d", expectedLen, len(seed))
	}
	// Put: actionByte % 100 in [0, 46) but not [0,3)
	action := seed[1] % 100
	if action < 3 || action >= 46 {
		t.Errorf("Put action %d not in [3, 46)", action)
	}
}

// Test_SpecSeedBuilder_Produces_Correct_ActionByte_When_CloseReopen verifies CloseReopen protocol.
func Test_SpecSeedBuilder_Produces_Correct_ActionByte_When_CloseReopen(t *testing.T) {
	t.Parallel()

	seed := testutil.NewSpecSeedBuilder(8, 4).CloseReopen().Build()
	if len(seed) != 1 {
		t.Fatalf("expected 1 byte, got %d", len(seed))
	}
	// CloseReopen: actionByte % 100 in [0, 3)
	action := seed[0] % 100
	if action >= 3 {
		t.Errorf("CloseReopen action %d not in [0, 3)", action)
	}
}

// Test_SpecSeedBuilder_Produces_Correct_ActionByte_When_Len_NoWriter verifies Len protocol without writer.
func Test_SpecSeedBuilder_Produces_Correct_ActionByte_When_Len_NoWriter(t *testing.T) {
	t.Parallel()

	seed := testutil.NewSpecSeedBuilder(8, 4).Len().Build()
	if len(seed) != 1 {
		t.Fatalf("expected 1 byte, got %d", len(seed))
	}
	// Len (no writer): actionByte % 100 in [26, 35)
	action := seed[0] % 100
	if action < 26 || action >= 35 {
		t.Errorf("Len (no writer) action %d not in [26, 35)", action)
	}
}

// Test_SpecSeedBuilder_Produces_Correct_ActionByte_When_Len_WriterActive verifies Len protocol with writer.
func Test_SpecSeedBuilder_Produces_Correct_ActionByte_When_Len_WriterActive(t *testing.T) {
	t.Parallel()

	seed := testutil.NewSpecSeedBuilder(8, 4).BeginWrite().Len().Build()
	if len(seed) != 2 {
		t.Fatalf("expected 2 bytes, got %d", len(seed))
	}
	// Len (writer active): actionByte % 100 in [91, 100)
	action := seed[1] % 100
	if action < 91 || action >= 100 {
		t.Errorf("Len (writer active) action %d not in [91, 100)", action)
	}
}

// Test_SpecSeedBuilder_Produces_Correct_ActionByte_When_Scan verifies Scan protocol.
func Test_SpecSeedBuilder_Produces_Correct_ActionByte_When_Scan(t *testing.T) {
	t.Parallel()

	seed := testutil.NewSpecSeedBuilder(8, 4).Scan().Build()
	if len(seed) != 1 {
		t.Fatalf("expected 1 byte, got %d", len(seed))
	}
	// Scan: actionByte % 100 in [45, 55)
	action := seed[0] % 100
	if action < 45 || action >= 55 {
		t.Errorf("Scan action %d not in [45, 55)", action)
	}
}

// Test_SpecSeeds_Have_NonEmpty_Data_When_Built verifies all curated seeds are non-empty.
func Test_SpecSeeds_Have_NonEmpty_Data_When_Built(t *testing.T) {
	t.Parallel()

	for _, seed := range testutil.SpecFuzzSeeds() {
		t.Run(seed.Name, func(t *testing.T) {
			t.Parallel()

			if len(seed.Data) == 0 {
				t.Errorf("seed %s has empty data", seed.Name)
			}
		})
	}
}

// Test_SpecSeeds_Start_With_BeginWrite_When_Required verifies seeds start with expected operation.
func Test_SpecSeeds_Start_With_BeginWrite_When_Required(t *testing.T) {
	t.Parallel()

	seeds := testutil.SpecFuzzSeeds()

	for _, seed := range seeds {
		t.Run(seed.Name, func(t *testing.T) {
			t.Parallel()

			if len(seed.Data) == 0 {
				t.Fatal("empty seed")
			}

			action := seed.Data[0] % 100

			switch seed.Name {
			case "Invalidate", "UserHeaderFlags", "UserHeaderData", "UserHeaderBoth", "InvalidateAfterReopen":
				// These start with BeginWrite (action in [3, 23))
				if action < 3 || action >= 23 {
					t.Errorf("seed %s first action %d not BeginWrite [3, 23)", seed.Name, action)
				}
			}
		})
	}
}

// Test_SpecSeedInvalidate_Contains_Invalidate_Action_When_Parsed verifies the Invalidate seed
// contains an invalidate action byte.
func Test_SpecSeedInvalidate_Contains_Invalidate_Action_When_Parsed(t *testing.T) {
	t.Parallel()

	seed := testutil.SpecSeedInvalidate

	// The seed should be: BeginWrite + Put(...) + Commit + Invalidate
	// We need to verify an invalidate action (23-25) appears after commit

	found := false
	// Invalidate action should be near the end
	for i := len(seed) - 3; i < len(seed); i++ {
		action := seed[i] % 100
		if action >= 23 && action < 26 {
			found = true

			break
		}
	}

	if !found {
		t.Error("SpecSeedInvalidate does not contain an Invalidate action in expected position")
	}
}

// Test_SpecSeedUserHeaderFlags_Contains_Flags_Action_When_Parsed verifies the seed
// contains a SetUserHeaderFlags action.
func Test_SpecSeedUserHeaderFlags_Contains_Flags_Action_When_Parsed(t *testing.T) {
	t.Parallel()

	seed := testutil.SpecSeedUserHeaderFlags

	// After BeginWrite, the next action should be SetUserHeaderFlags (83-86)
	if len(seed) < 2 {
		t.Fatal("seed too short")
	}

	action := seed[1] % 100
	if action < 83 || action >= 87 {
		t.Errorf("expected SetUserHeaderFlags action (83-86), got %d", action)
	}
}

// Test_SpecSeedUserHeaderData_Contains_Data_Action_When_Parsed verifies the seed
// contains a SetUserHeaderData action.
func Test_SpecSeedUserHeaderData_Contains_Data_Action_When_Parsed(t *testing.T) {
	t.Parallel()

	seed := testutil.SpecSeedUserHeaderData

	// After BeginWrite, the next action should be SetUserHeaderData (87-90)
	if len(seed) < 2 {
		t.Fatal("seed too short")
	}

	action := seed[1] % 100
	if action < 87 || action >= 91 {
		t.Errorf("expected SetUserHeaderData action (87-90), got %d", action)
	}
}

// Test_SpecSeedUserHeaderBoth_Contains_Both_Actions_When_Parsed verifies the seed
// contains both SetUserHeaderFlags and SetUserHeaderData actions.
func Test_SpecSeedUserHeaderBoth_Contains_Both_Actions_When_Parsed(t *testing.T) {
	t.Parallel()

	seed := testutil.SpecSeedUserHeaderBoth

	if len(seed) < 3 {
		t.Fatal("seed too short")
	}

	// After BeginWrite (byte 0), byte 1 should be SetUserHeaderFlags
	action1 := seed[1] % 100
	if action1 < 83 || action1 >= 87 {
		t.Errorf("expected SetUserHeaderFlags action (83-86) at byte 1, got %d", action1)
	}

	// After SetUserHeaderFlags (8 bytes for uint64), byte 10 should be SetUserHeaderData
	// BeginWrite (1) + SetUserHeaderFlags action (1) + flags (8) = byte 10
	if len(seed) < 11 {
		t.Fatal("seed too short for second action")
	}

	action2 := seed[10] % 100
	if action2 < 87 || action2 >= 91 {
		t.Errorf("expected SetUserHeaderData action (87-90) at byte 10, got %d", action2)
	}
}
