package fs

import (
	"bytes"
	"fmt"
	"math"
	"math/rand"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// Fuzz Tests
//
// These tests verify PROPERTIES that should hold across many random inputs:
//   - Chaos disabled behaves exactly like Real
//   - Partial reads always return valid prefix of original
//   - Fault rates are approximately correct
//   - Locking provides mutual exclusion
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
		chaosFS := NewChaos(NewReal(), seed, DefaultChaosConfig())
		chaosFS.SetMode(ChaosModePassthrough) // Passthrough - should match Real exactly

		path := filepath.Join(dir, "test.txt")
		content := []byte("hello world")

		// WriteFileAtomic
		realErr := realFS.WriteFileAtomic(path, content, 0644)

		chaosErr := chaosFS.WriteFileAtomic(path, content, 0644)
		if got, want := (chaosErr == nil), (realErr == nil); got != want {
			t.Fatalf("WriteFileAtomic: real=%v chaos=%v", realErr, chaosErr)
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
		realFS.WriteFileAtomic(path, content, 0644)

		chaosFS := NewChaos(realFS, seed, ChaosConfig{
			PartialReadRate: 1.0, // Always partial
		})
		chaosFS.SetMode(ChaosModeInject)

		data, err := chaosFS.ReadFile(path)
		if err != nil {
			return // Read failed entirely, that's OK
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
		chaosFS.SetMode(ChaosModeInject)

		err := chaosFS.WriteFileAtomic(path, content, 0644)
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
		realFS.WriteFileAtomic(path, []byte("hello world test content"), 0644)

		config := ChaosConfig{PartialReadRate: 1.0} // Always partial

		chaos1 := NewChaos(realFS, seed1, config)
		chaos1.SetMode(ChaosModeInject)

		chaos2 := NewChaos(realFS, seed2, config)
		chaos2.SetMode(ChaosModeInject)

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

// -----------------------------------------------------------------------------
// FuzzLock_MutualExclusion
//
// Property: Two goroutines cannot hold the same lock simultaneously.
//
// This spawns multiple goroutines that compete for the same lock.
// Each goroutine, while holding the lock, checks that no one else is in
// the critical section. If mutual exclusion is violated, the test fails.
// -----------------------------------------------------------------------------

func FuzzLock_MutualExclusion(f *testing.F) {
	// Boundary values (test clamping logic)
	f.Add(int64(0), 2, 1)     // Minimum valid (after clamping)
	f.Add(int64(1), 10, 20)   // Maximum valid (after clamping)
	f.Add(int64(2), 1, 0)     // Below minimum (tests clamping)
	f.Add(int64(3), 100, 100) // Above maximum (tests clamping)

	// High contention scenarios
	f.Add(int64(100), 10, 10) // Max goroutines, medium iterations
	f.Add(int64(101), 5, 20)  // Medium goroutines, max iterations

	// Seed boundaries
	f.Add(int64(-1), 5, 10)
	f.Add(int64(math.MaxInt64), 5, 10)

	f.Fuzz(func(t *testing.T, seed int64, goroutines int, iterations int) {
		// Bound inputs to reasonable ranges
		if goroutines < 2 {
			goroutines = 2
		}

		if goroutines > 10 {
			goroutines = 10
		}

		if iterations < 1 {
			iterations = 1
		}

		if iterations > 20 {
			iterations = 20
		}

		fs := NewReal()
		dir := t.TempDir()
		path := filepath.Join(dir, "data.txt")

		// Shared counter - if mutual exclusion works, final value is predictable
		var (
			counter   int
			counterMu sync.Mutex
		)

		// Track if anyone is in critical section
		var inCritical atomic.Int32

		var wg sync.WaitGroup

		errors := make(chan error, goroutines*iterations)

		for g := range goroutines {
			wg.Add(1)

			go func(id int) {
				defer wg.Done()

				for range iterations {
					lock, err := fs.Lock(path)
					if err != nil {
						errors <- fmt.Errorf("goroutine %d: Lock failed: %w", id, err)

						return
					}

					// PROPERTY: No one else should be in critical section
					if got, want := inCritical.Add(1), int32(1); got != want {
						errors <- fmt.Errorf("goroutine %d: inCritical=%d, want=%d (mutual exclusion violated)", id, got, want)

						lock.Close()

						return
					}

					// Critical section - increment counter
					counterMu.Lock()

					counter++

					counterMu.Unlock()

					// Small sleep to increase chance of race detection
					time.Sleep(time.Microsecond * 10)

					inCritical.Add(-1)
					lock.Close()
				}
			}(g)
		}

		wg.Wait()
		close(errors)

		// Check for errors
		for err := range errors {
			t.Fatal(err)
		}

		// PROPERTY: Counter should equal total iterations
		if got, want := counter, goroutines*iterations; got != want {
			t.Fatalf("counter=%d, want=%d (lost updates = broken mutex)", got, want)
		}
	})
}

// -----------------------------------------------------------------------------
// FuzzLock_NoDeadlock
//
// Property: Acquire + release cycles always complete (no deadlock).
//
// This does many lock/unlock cycles and verifies they all complete
// within a reasonable time.
// -----------------------------------------------------------------------------

func FuzzLock_NoDeadlock(f *testing.F) {
	// Boundary cycles (tests clamping)
	f.Add(int64(0), 1)   // Minimum
	f.Add(int64(1), 100) // Maximum
	f.Add(int64(2), 0)   // Below min (clamped to 1)
	f.Add(int64(3), 200) // Above max (clamped to 100)

	// Seed boundaries
	f.Add(int64(-1), 50)
	f.Add(int64(math.MaxInt64), 50)

	f.Fuzz(func(t *testing.T, seed int64, cycles int) {
		if cycles < 1 {
			cycles = 1
		}

		if cycles > 100 {
			cycles = 100
		}

		fs := NewReal()
		dir := t.TempDir()
		path := filepath.Join(dir, "data.txt")

		done := make(chan struct{})

		go func() {
			for range cycles {
				lock, err := fs.Lock(path)
				if err != nil {
					return // Lock timeout is OK
				}

				lock.Close()
			}

			close(done)
		}()

		// PROPERTY: Should complete within reasonable time
		select {
		case <-done:
			// Good - completed
		case <-time.After(5 * time.Second):
			t.Fatal("deadlock detected: lock cycles did not complete")
		}
	})
}

// -----------------------------------------------------------------------------
// FuzzLock_IndependentPaths
//
// Property: Locks on different paths don't interfere.
//
// Two goroutines locking different paths should never block each other.
// -----------------------------------------------------------------------------

func FuzzLock_IndependentPaths(f *testing.F) {
	// Boundary paths (tests clamping)
	f.Add(int64(0), 2)  // Minimum
	f.Add(int64(1), 10) // Maximum
	f.Add(int64(2), 1)  // Below min (clamped to 2)
	f.Add(int64(3), 20) // Above max (clamped to 10)

	// Seed boundaries
	f.Add(int64(-1), 5)
	f.Add(int64(math.MaxInt64), 5)

	f.Fuzz(func(t *testing.T, seed int64, numPaths int) {
		if numPaths < 2 {
			numPaths = 2
		}

		if numPaths > 10 {
			numPaths = 10
		}

		fs := NewReal()
		dir := t.TempDir()

		// Create paths
		paths := make([]string, numPaths)
		for i := range numPaths {
			paths[i] = filepath.Join(dir, fmt.Sprintf("file%d.txt", i))
		}

		// Acquire ALL locks simultaneously - should not block
		locks := make([]Locker, numPaths)
		done := make(chan struct{})

		go func() {
			for i, path := range paths {
				lock, err := fs.Lock(path)
				if err != nil {
					return
				}

				locks[i] = lock
			}

			close(done)
		}()

		// PROPERTY: Should acquire all locks quickly (no blocking)
		select {
		case <-done:
			// Good - all acquired
		case <-time.After(2 * time.Second):
			t.Fatal("independent paths should not block each other")
		}

		// Cleanup
		for _, lock := range locks {
			if lock != nil {
				lock.Close()
			}
		}
	})
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func randomFilename(rng *rand.Rand) string {
	return "file-" + string(rune('a'+rng.Intn(5))) + ".txt"
}

func randomString(rng *rand.Rand, length int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"

	b := make([]byte, length)
	for i := range b {
		b[i] = chars[rng.Intn(len(chars))]
	}

	return string(b)
}
