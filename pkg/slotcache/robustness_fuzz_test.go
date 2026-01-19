// Robustness: fuzz testing with corrupt/malformed input
//
// Oracle: "no panics, no hangs, graceful errors"
// Technique: coverage-guided fuzzing (go test -fuzz)
//
// This fuzz test starts from a valid cache file and then applies small,
// deterministic mutations.
//
// Rationale: writing fully arbitrary bytes usually fails at the header magic
// and does not reach deeper parsing paths.
//
// Failures here mean: "corrupt input caused a panic, hang, or was incorrectly accepted"

package slotcache_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

// FuzzSpec_OpenAndReadRobustness mutates a valid file and tries to Open it.
func FuzzSpec_OpenAndReadRobustness(f *testing.F) {
	// Seeds must contain at least a few bytes so DeriveFuzzOptions has something
	// to work with.
	f.Add([]byte{})
	f.Add([]byte{7, 4, 0, 15, 0})             // common config (key 8 / index 4 / cap ~64 / unordered)
	f.Add([]byte{7, 0, 0, 15, 1, 0xAA, 0xBB}) // edge config (index 0 / ordered)
	f.Add([]byte("near-valid-corruption-seed"))

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		tmpDir := t.TempDir()
		cacheFilePath := filepath.Join(tmpDir, "spec_mut_fuzz.slc")

		options, rest := testutil.DeriveFuzzOptions(fuzzBytes, cacheFilePath)

		// -----------------------------------------------------------------
		// 1) Create a valid file in the chosen configuration.
		// -----------------------------------------------------------------
		cache, err := slotcache.Open(options)
		if err != nil {
			t.Fatalf("Open(valid base) failed unexpectedly: %v", err)
		}

		// Write a small committed state (including a tombstone when possible) so
		// the mutated file is more likely to reach deep parsing paths.
		keys := make([][]byte, 0, 8)

		n := int(min(uint64(8), options.SlotCapacity))
		if n > 0 {
			w, beginErr := cache.BeginWrite()
			if beginErr == nil {
				for i := range n {
					k := make([]byte, options.KeySize)
					// Monotonic key prefix (ordered-safe).
					if options.KeySize >= 4 {
						k[0] = 0
						k[1] = 0
						k[2] = 0
						k[3] = byte(i + 1)
					} else {
						k[options.KeySize-1] = byte(i + 1)
					}

					var idx []byte
					if options.IndexSize > 0 {
						idx = bytes.Repeat([]byte{byte(0xA0 + i)}, options.IndexSize)
					}

					_ = w.Put(k, int64(i+1), idx)
					keys = append(keys, k)
				}

				_ = w.Commit()
				_ = w.Close()
			}

			// Create a tombstone to exercise bucket tombstone handling.
			w2, beginErr2 := cache.BeginWrite()
			if beginErr2 == nil {
				_, _ = w2.Delete(keys[0])
				_ = w2.Commit()
				_ = w2.Close()
			}
		}

		_ = cache.Close()

		baseBytes, readErr := os.ReadFile(cacheFilePath)
		if readErr != nil {
			t.Fatalf("ReadFile(valid base) failed: %v", readErr)
		}

		// -----------------------------------------------------------------
		// 2) Mutate and write back.
		// -----------------------------------------------------------------
		mutated := testutil.MutateBytes(baseBytes, testutil.NewByteStream(rest))

		writeErr := os.WriteFile(cacheFilePath, mutated, 0o600)
		if writeErr != nil {
			t.Fatalf("WriteFile(mutated) failed: %v", writeErr)
		}

		// -----------------------------------------------------------------
		// 3) Open mutated file (must not panic/hang).
		// -----------------------------------------------------------------
		cacheHandle, openErr := slotcache.Open(options)
		if openErr != nil {
			// Only allow classified errors.
			if errors.Is(openErr, slotcache.ErrCorrupt) ||
				errors.Is(openErr, slotcache.ErrIncompatible) ||
				errors.Is(openErr, slotcache.ErrBusy) {
				return
			}

			t.Fatalf("Open(mutated) returned unexpected error: %v", openErr)
		}

		defer func() { _ = cacheHandle.Close() }()

		// Use spec_oracle as a classifier/hint.
		oracleErr := testutil.ValidateFile(cacheFilePath, options)
		oracleAccepted := oracleErr == nil

		// If spec_oracle returned a structured error, use it to craft a targeted probe.
		var specErr *testutil.SpecError
		if errors.As(oracleErr, &specErr) {
			switch specErr.Kind {
			case testutil.SpecErrBucketSlotOutOfRange:
				// Deterministic: any Get() whose starting bucket equals BucketIndex
				// will trip slotID >= highwater before hash/key matching.
				key, ok := testutil.FindKeyForBucketStartIndex(options.KeySize, specErr.BucketCount, specErr.BucketIndex, 4096)
				if ok {
					_, _, err := cacheHandle.Get(key)
					if err == nil || (!errors.Is(err, slotcache.ErrCorrupt) && !errors.Is(err, slotcache.ErrBusy)) {
						t.Fatalf("oracle=%v but Get(%x) did not surface corruption: %v", specErr, key, err)
					}
				}

			case testutil.SpecErrBucketPointsToNonLiveSlot:
				// Only assert this when it should be detectable by Get(): stored hash
				// matches the slot's key hash, and we have the key bytes.
				if len(specErr.Key) == options.KeySize &&
					specErr.StoredHash == specErr.ComputedHash &&
					specErr.BucketCount >= 2 &&
					(specErr.ComputedHash&(specErr.BucketCount-1)) == specErr.BucketIndex {
					_, _, err := cacheHandle.Get(specErr.Key)
					if err == nil || (!errors.Is(err, slotcache.ErrCorrupt) && !errors.Is(err, slotcache.ErrBusy)) {
						t.Fatalf("oracle=%v but Get(%x) did not surface corruption: %v", specErr, specErr.Key, err)
					}
				}

			case testutil.SpecErrNoEmptyBuckets:
				// This is observable without any extra production validation:
				// a Get() MISS must probe bucket_count entries without encountering
				// an EMPTY bucket, which is an impossible invariant.
				//
				// We need a key that is not present. We find one deterministically
				// via Scan() (slot walk; does not use buckets) and then probe it.
				scanEntries, scanErr := cacheHandle.Scan(slotcache.ScanOptions{Reverse: false, Offset: 0, Limit: 0})
				if scanErr != nil {
					// Corruption already surfaced.
					if errors.Is(scanErr, slotcache.ErrCorrupt) || errors.Is(scanErr, slotcache.ErrBusy) {
						return
					}

					break
				}

				seen := make(map[string]struct{}, len(scanEntries))
				for _, e := range scanEntries {
					seen[string(e.Key)] = struct{}{}
				}

				missingKey, ok := func() ([]byte, bool) {
					if options.KeySize == 1 {
						for b := range 256 {
							k := []byte{byte(b)}
							if _, exists := seen[string(k)]; !exists {
								return k, true
							}
						}

						return nil, false
					}

					var tmp [4]byte

					for counter := range uint32(4096) {
						k := make([]byte, options.KeySize)

						binary.BigEndian.PutUint32(tmp[:], counter)

						if options.KeySize >= 4 {
							copy(k[:4], tmp[:])
						} else {
							copy(k, tmp[4-options.KeySize:])
						}

						if _, exists := seen[string(k)]; !exists {
							return k, true
						}
					}

					return nil, false
				}()
				if ok {
					_, found, err := cacheHandle.Get(missingKey)
					if err == nil {
						// If we accidentally hit a present key, nothing to assert.
						// If we got a clean miss, that contradicts the oracle classification.
						if !found {
							t.Fatalf("oracle=%v but Get(%x) returned not-found without error", specErr, missingKey)
						}

						break
					}

					if !errors.Is(err, slotcache.ErrCorrupt) && !errors.Is(err, slotcache.ErrBusy) {
						t.Fatalf("oracle=%v but Get(%x) returned unexpected error: %v", specErr, missingKey, err)
					}
				}

			case testutil.SpecErrBucketHashMismatchForSlot, testutil.SpecErrKeyNotFindable, testutil.SpecErrUnknown:
				// Balance choice (documented): We do NOT hard-assert that these oracle
				// failures must surface as ErrCorrupt from a single public read.
				//
				// - BucketHashMismatchForSlot is not necessarily observable: Get() uses
				//   storedHash only as a quick reject against the *computed hash of the
				//   queried key*, and then verifies key bytes. If a bucket's stored hash
				//   is wrong but the key bytes don't match the queried key, Get() can
				//   legitimately continue probing without noticing.
				//
				// - KeyNotFindable would require extra O(n) validation on Get() misses
				//   to prove that a live slot exists but is unreachable via buckets.
				//   We don't want to incentivize adding such production validation just
				//   to satisfy fuzz tests.
				//
				// We still run the generic probes below.
			}
		}

		// -----------------------------------------------------------------
		// 4) Basic API probes. Must not panic/hang.
		//
		// If oracleAccepted, these should behave like a normal, correct cache.
		// If oracle rejected, reads may return ErrCorrupt/ErrBusy.
		// -----------------------------------------------------------------

		lenVal, lenErr := cacheHandle.Len()
		if lenErr != nil {
			if oracleAccepted {
				t.Fatalf("Len returned error but oracle accepted file: %v", lenErr)
			}

			if errors.Is(lenErr, slotcache.ErrCorrupt) || errors.Is(lenErr, slotcache.ErrBusy) {
				return
			}

			t.Fatalf("Len returned unexpected error: %v", lenErr)
		}

		probeKey := make([]byte, options.KeySize)

		_, _, getErr := cacheHandle.Get(probeKey)
		if getErr != nil {
			if oracleAccepted {
				t.Fatalf("Get returned error but oracle accepted file: %v", getErr)
			}

			if errors.Is(getErr, slotcache.ErrCorrupt) || errors.Is(getErr, slotcache.ErrBusy) {
				return
			}

			t.Fatalf("Get returned unexpected error: %v", getErr)
		}

		entries, scanErr := cacheHandle.Scan(slotcache.ScanOptions{Reverse: false, Offset: 0, Limit: 0})
		if scanErr != nil {
			if oracleAccepted {
				t.Fatalf("Scan returned error but oracle accepted file: %v", scanErr)
			}

			if errors.Is(scanErr, slotcache.ErrCorrupt) || errors.Is(scanErr, slotcache.ErrBusy) {
				return
			}

			t.Fatalf("Scan returned unexpected error: %v", scanErr)
		}

		// Scan succeeded. It must be structurally sane.
		for _, e := range entries {
			if len(e.Key) != options.KeySize {
				t.Fatalf("Scan returned key with wrong size: got %d want %d", len(e.Key), options.KeySize)
			}

			if options.IndexSize > 0 && len(e.Index) != options.IndexSize {
				t.Fatalf("Scan returned index with wrong size: got %d want %d", len(e.Index), options.IndexSize)
			}
		}

		if oracleAccepted {
			// On a valid file, Len and Scan must agree.
			if lenVal != len(entries) {
				t.Fatalf("oracle accepted file but Len=%d and Scan returned %d entries", lenVal, len(entries))
			}

			// On a valid file, Get must round-trip all scanned entries.
			for _, e := range entries {
				got, ok, err := cacheHandle.Get(e.Key)
				if err != nil {
					t.Fatalf("oracle accepted file but Get(%x) failed after Scan: %v", e.Key, err)
				}

				if !ok {
					t.Fatalf("oracle accepted file but Get(%x) missing after Scan", e.Key)
				}

				if got.Revision != e.Revision {
					t.Fatalf("oracle accepted file but Get(%x) revision mismatch after Scan", e.Key)
				}

				if options.IndexSize > 0 && !bytes.Equal(got.Index, e.Index) {
					t.Fatalf("oracle accepted file but Get(%x) index mismatch after Scan", e.Key)
				}
			}
		}

		_, prefixErr := cacheHandle.ScanPrefix([]byte{0x00}, slotcache.ScanOptions{Reverse: false, Offset: 0, Limit: 0})
		if prefixErr != nil {
			if oracleAccepted {
				t.Fatalf("ScanPrefix returned error but oracle accepted file: %v", prefixErr)
			}

			if errors.Is(prefixErr, slotcache.ErrCorrupt) ||
				errors.Is(prefixErr, slotcache.ErrBusy) ||
				errors.Is(prefixErr, slotcache.ErrInvalidInput) {
				return
			}

			t.Fatalf("ScanPrefix returned unexpected error: %v", prefixErr)
		}
	})
}
