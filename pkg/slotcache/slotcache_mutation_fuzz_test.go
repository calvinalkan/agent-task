// Mutation fuzz tests for robustness against corrupt/malformed files.
// Starts from a valid file, applies random mutations, then verifies:
//   - No panics or hangs
//   - Graceful error handling (ErrCorrupt, ErrIncompatible, etc.)
//   - API probes (Get, Scan, Len) behave correctly
//
// Failures mean: corrupt input caused a panic, hang, or was incorrectly accepted.

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

const (
	largeRecordKeySize   = 512
	largeRecordIndexSize = 16 * 1024
	largeRecordCapacity  = 60 // keep base file <= 1MiB so mutations don't always truncate
)

// Mutates a valid file with derived options and verifies graceful handling.
func FuzzSlotcache_Handles_Corruption_When_File_Mutated_With_Derived_Options(f *testing.F) {
	// Seeds with encoded option profiles
	f.Add([]byte{})

	commonOpts := testutil.OptionsToSeed(slotcache.Options{KeySize: 8, IndexSize: 4, SlotCapacity: 64})
	f.Add(commonOpts) // common config

	edgeOpts := testutil.OptionsToSeed(slotcache.Options{KeySize: 8, IndexSize: 0, SlotCapacity: 64, OrderedKeys: true})
	f.Add(append(edgeOpts, 0xAA, 0xBB)) // edge config (index 0 / ordered)

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		tmpDir := t.TempDir()
		cacheFilePath := filepath.Join(tmpDir, "spec_mut_fuzz.slc")

		options, rest := testutil.OptionsFromSeed(fuzzBytes, cacheFilePath)

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
			w, beginErr := cache.Writer()
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
			w2, beginErr2 := cache.Writer()
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
		mutated := mutateBytes(baseBytes, testutil.NewByteStream(rest))

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
				errors.Is(openErr, slotcache.ErrBusy) ||
				errors.Is(openErr, slotcache.ErrInvalidated) {
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
				key, ok := findKeyForBucketStartIndex(options.KeySize, specErr.BucketCount, specErr.BucketIndex, 4096)
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

// Same as above but with large KeySize (512) and IndexSize (16KB) to stress
// record-layout arithmetic and large key/index copy paths.
func FuzzSlotcache_Handles_Corruption_When_File_Mutated_With_Large_Records(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("near-cap-robust"))
	f.Add([]byte{0x00, 0x01, 0x02, 0x03, 0x04})
	f.Add([]byte("near-cap-corruption-seed"))

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		tmpDir := t.TempDir()
		cacheFilePath := filepath.Join(tmpDir, "spec_mut_fuzz_nearcap.slc")

		options := slotcache.Options{
			Path:         cacheFilePath,
			KeySize:      largeRecordKeySize,
			IndexSize:    largeRecordIndexSize,
			UserVersion:  1,
			SlotCapacity: largeRecordCapacity,
		}

		// -----------------------------------------------------------------
		// 1) Create a valid base file in the chosen configuration.
		// -----------------------------------------------------------------
		cache, err := slotcache.Open(options)
		if err != nil {
			t.Fatalf("Open(valid base) failed unexpectedly: %v", err)
		}

		// Write a tiny committed state (and a tombstone) so the mutated file is
		// more likely to reach deep parsing paths.
		keys := make([][]byte, 0, 4)

		w, beginErr := cache.Writer()
		if beginErr == nil {
			for i := range 4 {
				k := make([]byte, options.KeySize)
				// Deterministic prefix so keys are distinct.
				if options.KeySize >= 4 {
					k[0] = 0
					k[1] = 0
					k[2] = 0
					k[3] = byte(i + 1)
				} else {
					k[options.KeySize-1] = byte(i + 1)
				}

				idx := bytes.Repeat([]byte{byte(0xA0 + i)}, options.IndexSize)

				_ = w.Put(k, int64(i+1), idx)
				keys = append(keys, k)
			}

			_ = w.Commit()
			_ = w.Close()
		}

		// Create a tombstone to exercise bucket tombstone handling.
		if len(keys) > 0 {
			w2, beginErr2 := cache.Writer()
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
		mutated := mutateBytes(baseBytes, testutil.NewByteStream(fuzzBytes))

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
				errors.Is(openErr, slotcache.ErrBusy) ||
				errors.Is(openErr, slotcache.ErrInvalidated) {
				return
			}

			t.Fatalf("Open(mutated) returned unexpected error: %v", openErr)
		}

		defer func() { _ = cacheHandle.Close() }()

		// Use spec_oracle as a classifier/hint.
		oracleErr := testutil.ValidateFile(cacheFilePath, options)
		oracleAccepted := oracleErr == nil

		// -----------------------------------------------------------------
		// 4) Basic API probes. Must not panic/hang.
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
					t.Fatalf("oracle accepted file but Get failed after Scan: %v", err)
				}

				if !ok {
					t.Fatal("oracle accepted file but Get missing after Scan")
				}

				if got.Revision != e.Revision {
					t.Fatal("oracle accepted file but Get revision mismatch after Scan")
				}

				if options.IndexSize > 0 && !bytes.Equal(got.Index, e.Index) {
					t.Fatal("oracle accepted file but Get index mismatch after Scan")
				}
			}
		}

		// Simple prefix probe (must not panic/hang).
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

		// Also probe ScanMatch with a bit-prefix spec (exercise Prefix.Bits math).
		bitSpec := slotcache.Prefix{Offset: 0, Bits: 9, Bytes: []byte{0x00, 0x00}}
		_, _ = cacheHandle.ScanMatch(bitSpec, slotcache.ScanOptions{Reverse: false, Offset: 0, Limit: 0})
	})
}

// findKeyForBucketStartIndex returns a deterministic key (length keySize) whose
// FNV-1a hash maps to the desired bucket start index (hash & (bucketCount-1)).
//
// This is used by robustness fuzz tests to force Cache.Get() to touch a specific
// bucket index.
//
// The search is bounded by maxAttempts to keep fuzz iterations fast.
func findKeyForBucketStartIndex(keySize int, bucketCount, wantIndex uint64, maxAttempts uint32) ([]byte, bool) {
	if keySize <= 0 {
		return nil, false
	}

	// Must be power-of-two for mask math.
	if bucketCount < 2 || (bucketCount&(bucketCount-1)) != 0 {
		return nil, false
	}

	mask := bucketCount - 1
	if wantIndex > mask {
		return nil, false
	}

	key := make([]byte, keySize)

	var tmp [4]byte

	for attempt := range maxAttempts {
		// Deterministic pattern: encode attempt counter into a fixed prefix,
		// then zero the remainder.
		for i := range key {
			key[i] = 0
		}

		binary.BigEndian.PutUint32(tmp[:], attempt)

		if keySize >= 4 {
			copy(key[:4], tmp[:])
		} else {
			copy(key, tmp[4-keySize:])
		}

		h := fnv1a64(key)
		if (h & mask) == wantIndex {
			return append([]byte(nil), key...), true
		}
	}

	return nil, false
}

// MutateBytes applies a bounded sequence of simple deterministic mutations to src.
//
// This is used for "near-valid" corruption fuzzing: start from a valid file,
// then apply small edits so Open() gets past the header more often.
func mutateBytes(src []byte, stream *testutil.ByteStream) []byte {
	mut := append([]byte(nil), src...)

	// 1..8 mutation steps.
	steps := 1 + int(stream.NextByte()%8)

	for range steps {
		if len(mut) == 0 {
			// Ensure we can still grow from empty.
			mut = append(mut, 0)
		}

		op := stream.NextByte() % 6

		switch op {
		case 0: // flip bits in-place
			off := int(stream.NextUint32()) % len(mut)
			n := 1 + int(stream.NextByte()%32)
			end := min(off+n, len(mut))

			mask := byte(1 << (stream.NextByte() % 8))
			for i := off; i < end; i++ {
				mut[i] ^= mask
			}

		case 1: // overwrite a range
			off := int(stream.NextUint32()) % len(mut)
			n := 1 + int(stream.NextByte()%64)

			end := min(off+n, len(mut))
			for i := off; i < end; i++ {
				mut[i] = stream.NextByte()
			}

		case 2: // truncate to a smaller length
			newLen := int(stream.NextUint32()) % (len(mut) + 1)
			mut = mut[:newLen]

		case 3: // append some bytes (bounded growth)
			add := 1 + int(stream.NextByte()%128)
			for range add {
				mut = append(mut, stream.NextByte())
			}

		case 4: // insert a short run at an arbitrary position
			off := int(stream.NextUint32()) % (len(mut) + 1)
			add := 1 + int(stream.NextByte()%32)

			insert := make([]byte, 0, add)
			for range add {
				insert = append(insert, stream.NextByte())
			}

			mut = append(mut[:off], append(insert, mut[off:]...)...)

		case 5: // duplicate a short range somewhere else
			if len(mut) < 2 {
				continue
			}

			from := int(stream.NextUint32()) % len(mut)
			to := int(stream.NextUint32()) % (len(mut) + 1)
			ln := 1 + int(stream.NextByte()%32)
			end := min(from+ln, len(mut))
			chunk := append([]byte(nil), mut[from:end]...)
			mut = append(mut[:to], append(chunk, mut[to:]...)...)
		}

		// Keep mutated blobs bounded so one input can't force huge allocations.
		if len(mut) > 1<<20 { // 1 MiB
			mut = mut[:1<<20]
		}
	}

	return mut
}
