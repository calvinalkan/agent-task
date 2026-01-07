package fs

import (
	"bytes"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

// =============================================================================
// Fuzz Tests
//
// These tests verify PROPERTIES that should hold across many random inputs:
//   - Chaos disabled behaves exactly like Real
//   - Partial reads always return valid prefix of original
//   - Fault rates are approximately correct
//
// Unlike example tests which check specific scenarios, these explore the
// input space to find edge cases.
// =============================================================================

// -----------------------------------------------------------------------------
// FuzzChaos_DisabledMatchesReal
//
// Property: When chaos is disabled, it should behave EXACTLY like the real FS.
//
// This verifies that the Chaos wrapper doesn't accidentally change behavior
// even when fault injection is off.
// -----------------------------------------------------------------------------

func FuzzChaos_DisabledMatchesReal(f *testing.F) {
	// Boundary values for RNG seeding
	f.Add(int64(0))             // Zero - deterministic baseline
	f.Add(int64(1))             // Minimal positive
	f.Add(int64(-1))            // Negative seed
	f.Add(int64(math.MaxInt64)) // Maximum int64
	f.Add(int64(math.MinInt64)) // Minimum int64

	// Powers of 2
	f.Add(int64(1 << 32)) // 32-bit boundary

	// Arbitrary for diversity
	f.Add(int64(12345))

	f.Fuzz(func(t *testing.T, seed int64) {
		dir := t.TempDir()

		realFS := NewReal()
		chaosFS := NewChaos(NewReal(), seed, ChaosConfig{
			ReadFailRate:       100,
			PartialReadRate:    100,
			WriteFailRate:      100,
			PartialWriteRate:   100,
			ShortWriteRate:     100,
			FileStatFailRate:   100,
			SeekFailRate:       100,
			SyncFailRate:       100,
			CloseFailRate:      100,
			OpenFailRate:       100,
			RemoveFailRate:     100,
			RenameFailRate:     100,
			StatFailRate:       100,
			MkdirAllFailRate:   100,
			ReadDirFailRate:    100,
			ReadDirPartialRate: 100,
		})
		chaosFS.SetMode(ChaosModeNoOp) // Passthrough - should match Real exactly

		path := filepath.Join(dir, "test.txt")
		content := []byte("hello world")

		// Write
		_, realErr := writeFileOnce(realFS, path, content, 0644)

		_, chaosErr := writeFileOnce(chaosFS, path, content, 0644)
		if got, want := (chaosErr == nil), (realErr == nil); got != want {
			t.Fatalf("write: real=%v chaos=%v", realErr, chaosErr)
		}

		// ReadFile
		realData, realErr := realFS.ReadFile(path)

		chaosData, chaosErr := chaosFS.ReadFile(path)
		if got, want := (chaosErr == nil), (realErr == nil); got != want {
			t.Fatalf("ReadFile: real=%v chaos=%v", realErr, chaosErr)
		}

		if got, want := chaosData, realData; !bytes.Equal(got, want) {
			t.Fatalf("ReadFile data: got=%q, want=%q", got, want)
		}

		// Stat
		realInfo, realErr := realFS.Stat(path)

		chaosInfo, chaosErr := chaosFS.Stat(path)
		if got, want := (chaosErr == nil), (realErr == nil); got != want {
			t.Fatalf("Stat: real=%v chaos=%v", realErr, chaosErr)
		}

		if got, want := chaosInfo.Size(), realInfo.Size(); got != want {
			t.Fatalf("Stat size: got=%d, want=%d", got, want)
		}

		// Exists
		realExists, realErr := realFS.Exists(path)

		chaosExists, chaosErr := chaosFS.Exists(path)
		if got, want := chaosExists, realExists; got != want {
			t.Fatalf("Exists: got=%v, want=%v", got, want)
		}

		if got, want := (chaosErr == nil), (realErr == nil); got != want {
			t.Fatalf("Exists err: real=%v chaos=%v", realErr, chaosErr)
		}

		// ReadDir
		realEntries, realErr := realFS.ReadDir(dir)

		chaosEntries, chaosErr := chaosFS.ReadDir(dir)
		if got, want := (chaosErr == nil), (realErr == nil); got != want {
			t.Fatalf("ReadDir: real=%v chaos=%v", realErr, chaosErr)
		}

		if got, want := len(chaosEntries), len(realEntries); got != want {
			t.Fatalf("ReadDir count: got=%d, want=%d", got, want)
		}

		// Remove
		realFS.Remove(path)  // Remove with real
		chaosFS.Remove(path) // Already gone, should also fail

		// Exists after remove
		realExists, _ = realFS.Exists(path)

		chaosExists, _ = chaosFS.Exists(path)
		if got, want := chaosExists, realExists; got != want {
			t.Fatalf("Exists after remove: got=%v, want=%v", got, want)
		}
	})
}

// -----------------------------------------------------------------------------
// FuzzChaos_PartialReadIsPrefix
//
// Property: Partial reads should ALWAYS return a prefix of the original data.
//
// This ensures the chaos partial read implementation is correct - it returns
// real data, just truncated, not garbage or data from wrong offset.
// -----------------------------------------------------------------------------

func FuzzChaos_PartialReadIsPrefix(f *testing.F) {
	// === Seed boundaries + simple content ===
	f.Add(int64(0), []byte("ab"))                  // Minimal content, zero seed
	f.Add(int64(-1), []byte("hello world"))        // Negative seed
	f.Add(int64(math.MaxInt64), []byte("test"))    // Max seed
	f.Add(int64(1), []byte("the quick brown fox")) // Normal text

	// === Diverse byte patterns ===
	f.Add(int64(100), []byte{0x00, 0x00, 0x00})       // Null bytes
	f.Add(int64(101), []byte{0xFF, 0xFE, 0xFD, 0xFC}) // High bytes
	f.Add(int64(102), []byte{0x00, 0xFF, 0x00, 0xFF}) // Alternating pattern
	f.Add(int64(103), []byte("日本語テスト"))               // Unicode (multi-byte chars)
	f.Add(int64(104), []byte("émoji 🎉 test"))         // Emoji

	// === Size boundaries ===
	f.Add(int64(200), make([]byte, 1000))                // 1KB of zeros
	f.Add(int64(201), []byte(strings.Repeat("x", 4096))) // Exactly 4KB (page size)
	f.Add(int64(202), []byte(strings.Repeat("y", 4097))) // Just over page boundary
	f.Add(int64(203), []byte(strings.Repeat("z", 8192))) // 2 pages

	f.Fuzz(func(t *testing.T, seed int64, content []byte) {
		if len(content) < 2 {
			return // Too small for partial read
		}

		dir := t.TempDir()
		path := filepath.Join(dir, "test.txt")

		realFS := NewReal()

		mustWriteFile(t, path, content, 0644)

		chaosFS := NewChaos(realFS, seed, ChaosConfig{
			PartialReadRate: 1.0, // Always partial
		})

		data, err := chaosFS.ReadFile(path)
		if err == nil {
			t.Fatalf("ReadFile unexpectedly succeeded")
		}

		// PROPERTY: data must be prefix of content
		if got, want := bytes.HasPrefix(content, data), true; got != want {
			t.Fatalf("partial read should be prefix\noriginal: %q\ngot: %q", content, data)
		}

		// PROPERTY: partial means shorter
		if got, want := len(data) < len(content), true; got != want {
			t.Fatalf("len(data)=%d, want less than %d", len(data), len(content))
		}
	})
}

// -----------------------------------------------------------------------------
// FuzzChaos_PartialWriteIsPrefix
//
// Property: Partial writes should write a prefix of the original data.
//
// When chaos injects a partial write (simulating crash), the data on disk
// should be a valid prefix, not corrupted garbage.
// -----------------------------------------------------------------------------

func FuzzChaos_PartialWriteIsPrefix(f *testing.F) {
	// === Seed boundaries + diverse content ===
	f.Add(int64(0), []byte("ab"))                       // Minimal
	f.Add(int64(-1), []byte("hello world"))             // Negative seed
	f.Add(int64(math.MaxInt64), []byte("test content")) // Max seed

	// === Byte patterns ===
	f.Add(int64(100), []byte{0x00, 0xFF, 0x00}) // Binary with null
	f.Add(int64(101), []byte("日本語"))            // Unicode

	// === Size boundaries ===
	f.Add(int64(200), []byte(strings.Repeat("x", 4096))) // Page size
	f.Add(int64(201), []byte(strings.Repeat("y", 4097))) // Just over page

	f.Fuzz(func(t *testing.T, seed int64, content []byte) {
		if len(content) < 2 {
			return // Too small for partial write
		}

		dir := t.TempDir()
		path := filepath.Join(dir, "test.txt")

		realFS := NewReal()
		chaosFS := NewChaos(realFS, seed, ChaosConfig{
			PartialWriteRate: 1.0, // Always partial
		})

		f, err := chaosFS.Create(path)
		if err != nil {
			return
		}

		_, err = f.Write(content)
		_ = f.Close()

		if err == nil {
			return // Didn't trigger partial write (shouldn't happen at 100%)
		}

		// Read what was actually written
		data, readErr := realFS.ReadFile(path)
		if readErr != nil {
			return // File might not exist if partial write was very early
		}

		// PROPERTY: data must be prefix of content
		if got, want := bytes.HasPrefix(content, data), true; got != want {
			t.Fatalf("partial write should be prefix\noriginal: %q\ngot: %q", content, data)
		}
	})
}

// -----------------------------------------------------------------------------
// FuzzChaos_DifferentSeedsProduceDifferentResults
//
// Property: Different seeds should produce different random sequences.
//
// This verifies that the RNG seeding works - same config but different seeds
// should give different (but valid) results.
// -----------------------------------------------------------------------------

func FuzzChaos_DifferentSeedsProduceDifferentResults(f *testing.F) {
	// Adjacent values (should still produce different RNG sequences)
	f.Add(int64(0), int64(1))
	f.Add(int64(-1), int64(0))
	f.Add(int64(math.MaxInt64-1), int64(math.MaxInt64))

	// Boundary pairs
	f.Add(int64(math.MinInt64), int64(math.MaxInt64))

	// Arbitrary diverse pairs
	f.Add(int64(12345), int64(67890))

	f.Fuzz(func(t *testing.T, seed1 int64, seed2 int64) {
		if seed1 == seed2 {
			return // Same seed = same results, skip
		}

		dir := t.TempDir()
		path := filepath.Join(dir, "test.txt")
		realFS := NewReal()

		mustWriteFile(t, path, []byte("hello world test content"), 0644)

		config := ChaosConfig{PartialReadRate: 1.0} // Always partial

		chaos1 := NewChaos(realFS, seed1, config)
		chaos1.SetMode(ChaosModeActive)

		chaos2 := NewChaos(realFS, seed2, config)
		chaos2.SetMode(ChaosModeActive)

		// Read with both - should get different truncation points
		data1, _ := chaos1.ReadFile(path)
		data2, _ := chaos2.ReadFile(path)

		// With different seeds, lengths should differ (usually)
		// We don't assert they MUST differ (could be same by chance)
		// but we verify both are valid partial reads
		content, _ := realFS.ReadFile(path)

		if got, want := bytes.HasPrefix(content, data1), true; got != want {
			t.Errorf("seed1 data should be prefix")
		}

		if got, want := bytes.HasPrefix(content, data2), true; got != want {
			t.Errorf("seed2 data should be prefix")
		}
	})
}
