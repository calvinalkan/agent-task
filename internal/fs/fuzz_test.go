package fs

import (
	"bytes"
	"fmt"
	"math"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"
)

// =============================================================================
// Fuzz Tests
//
// These tests verify PROPERTIES that should hold across many random inputs:
//   - Same seeds produce identical fault sequences (determinism)
//   - Partial reads always return valid prefix of original
//   - Partial writes always write valid prefix of original
//
// Unlike example tests which check specific scenarios, these explore the
// input space to find edge cases.
// =============================================================================

// -----------------------------------------------------------------------------
// Fuzz_Chaos_Produces_Identical_Results_When_Seeds_Are_Same
//
// Property: Given the same opSeed and chaosSeed, running the same sequence of
// random operations twice must produce identical results.
//
// This verifies that seed-based determinism works across all seed values,
// not just the hardcoded ones in unit tests.
// -----------------------------------------------------------------------------

func Fuzz_Chaos_Produces_Identical_Results_When_Seeds_Are_Same(f *testing.F) {
	// Boundary seeds
	f.Add(int64(0), int64(0))
	f.Add(int64(-1), int64(-1))
	f.Add(int64(math.MaxInt64), int64(math.MinInt64))

	// Arbitrary pairs
	f.Add(int64(11111), int64(22222))
	f.Add(int64(99999), int64(12345))

	f.Fuzz(func(t *testing.T, opSeed, chaosSeed int64) {
		config := ChaosConfig{
			ReadFailRate:     0.3,
			WriteFailRate:    0.3,
			OpenFailRate:     0.3,
			PartialReadRate:  0.3,
			PartialWriteRate: 0.3,
			StatFailRate:     0.3,
			RemoveFailRate:   0.3,
		}

		type result struct {
			op      string
			failed  bool
			n       int
			content string
		}

		run := func() []result {
			dir := t.TempDir()
			realFS := NewReal()
			opRng := rand.New(rand.NewSource(opSeed))
			chaos := NewChaos(realFS, chaosSeed, config)

			var results []result

			existingContent := "test content"

			// Pre-create some files for read operations
			for i := range 5 {
				path := filepath.Join(dir, fmt.Sprintf("existing%d.txt", i))
				if err := realFS.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("MkdirAll: %v", err)
				}
				f, err := realFS.Create(path)
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				_, _ = f.Write([]byte(existingContent))
				_ = f.Close()
			}

			for i := range 30 {
				op := opRng.Intn(4)

				switch op {
				case 0: // create and write
					path := filepath.Join(dir, fmt.Sprintf("new%d.txt", i))
					writeData := []byte("data")
					f, err := chaos.Create(path)
					if err != nil {
						results = append(results, result{"create", true, 0, ""})
						continue
					}
					n, writeErr := f.Write(writeData)
					_ = f.Close()

					var onDisk string
					if data, err := realFS.ReadFile(path); err == nil {
						onDisk = string(data)
					}

					results = append(results, result{"write", writeErr != nil, n, onDisk})

				case 1: // read existing file
					path := filepath.Join(dir, fmt.Sprintf("existing%d.txt", opRng.Intn(5)))
					data, err := chaos.ReadFile(path)
					results = append(results, result{"read", err != nil, len(data), string(data)})

				case 2: // stat existing file
					path := filepath.Join(dir, fmt.Sprintf("existing%d.txt", opRng.Intn(5)))
					info, err := chaos.Stat(path)
					size := 0
					if info != nil {
						size = int(info.Size())
					}
					results = append(results, result{"stat", err != nil, size, ""})

				case 3: // remove (may or may not exist)
					path := filepath.Join(dir, fmt.Sprintf("new%d.txt", opRng.Intn(i+1)))
					err := chaos.Remove(path)
					results = append(results, result{"remove", IsChaosErr(err), 0, ""})
				}
			}
			return results
		}

		first := run()
		second := run()

		if len(first) != len(second) {
			t.Fatalf("different result lengths: %d vs %d", len(first), len(second))
		}

		for i := range first {
			if first[i] != second[i] {
				t.Fatalf("diverged at operation %d:\n  first:  %+v\n  second: %+v", i, first[i], second[i])
			}
		}
	})
}

// -----------------------------------------------------------------------------
// Fuzz_Chaos_Returns_Prefix_When_Partial_Read_Rate_Is_One
//
// Property: Partial reads should ALWAYS return a prefix of the original data.
//
// This ensures the chaos partial read implementation is correct - it returns
// real data, just truncated, not garbage or data from wrong offset.
// -----------------------------------------------------------------------------

func Fuzz_Chaos_Returns_Prefix_When_Partial_Read_Rate_Is_One(f *testing.F) {
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
// Fuzz_Chaos_Writes_Prefix_When_Partial_Write_Rate_Is_One
//
// Property: Partial writes should write a prefix of the original data.
//
// When chaos injects a partial write (simulating crash), the data on disk
// should be a valid prefix, not corrupted garbage.
// -----------------------------------------------------------------------------

func Fuzz_Chaos_Writes_Prefix_When_Partial_Write_Rate_Is_One(f *testing.F) {
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
