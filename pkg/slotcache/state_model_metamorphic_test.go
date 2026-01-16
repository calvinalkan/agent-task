//go:build slotcache_impl

package slotcache_test

// Metamorphic testing:
//
// This file tests *semantic laws* that should hold for both the model and the
// real implementation. Unlike property tests that compare model vs real,
// metamorphic tests verify that transformations of input produce equivalent
// output.

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/model"
)

// -----------------------------------------------------------------------------
// Test: Last-write-wins buffer reduction
// -----------------------------------------------------------------------------

// Test_Metamorphic_LastWriteWins_BufferReduction verifies:
//
// If you take a writer session's operation buffer and reduce it to only the
// last operation per key (preserving order), committing the reduced buffer
// must produce the same committed state as committing the original buffer.
func Test_Metamorphic_LastWriteWins_BufferReduction(t *testing.T) {
	seedCount := 25
	opsPerSeed := 50

	for i := 0; i < seedCount; i++ {
		seed := int64(1000 + i)

		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()

			basePath := filepath.Join(tmpDir, "base.slc")
			originalPath := filepath.Join(tmpDir, "original.slc")
			reducedPath := filepath.Join(tmpDir, "reduced.slc")

			options := slotcache.Options{
				Path:         basePath,
				KeySize:      8,
				IndexSize:    4,
				SlotCapacity: 256,
			}

			rng := rand.New(rand.NewSource(seed))

			// 1) Build a non-trivial committed base state.
			h := newHarness(t, options)
			defer func() { _ = h.real.cache.Close() }()

			var keys [][]byte
			baseOps := 100
			for i := 0; i < baseOps; i++ {
				op := randOp(rng, h, keys)

				mRes := applyModel(h, op)
				rRes := applyReal(h, op)

				assertMatch(t, op, mRes, rRes)
				compareObservableState(t, h)

				// Track keys from successful Put operations.
				if put, isPutOp := op.(opPut); isPutOp {
					if mErr, isErrorResult := mRes.(resErr); isErrorResult {
						if mErr.Error == nil && len(put.Key) == options.KeySize {
							keys = append(keys, append([]byte(nil), put.Key...))
						}
					}
				}
			}

			// Ensure base state is committed and no writer is active.
			if h.model.writer != nil {
				_ = h.model.writer.Abort()
				h.model.writer = nil
			}
			if h.real.writer != nil {
				_ = h.real.writer.Abort()
				h.real.writer = nil
			}

			// Close the base real handle so the on-disk file is stable for copying.
			if err := h.real.cache.Close(); err != nil {
				if !errors.Is(err, slotcache.ErrClosed) {
					t.Fatalf("unexpected error closing base real cache: %v", err)
				}
			}

			// 2) Fork the base state.
			// Model fork: deep copy of the in-memory file.
			mFileOrig := h.model.file.Clone()
			mFileRed := h.model.file.Clone()

			// Real fork: byte-for-byte copy of the cache file.
			copyFile(t, basePath, originalPath)
			copyFile(t, basePath, reducedPath)

			optsOrig := options
			optsOrig.Path = originalPath
			optsRed := options
			optsRed.Path = reducedPath

			// 3) Generate a writer session buffer and its reduced form.
			origOps := genWriterOps(rng, options, keys, opsPerSeed)
			redOps := reduceOps(origOps)

			// 4) Execute both sequences against both model and real.
			mOrig := execModel(t, mFileOrig, origOps)
			mRed := execModel(t, mFileRed, redOps)

			rOrig := execReal(t, optsOrig, origOps)
			rRed := execReal(t, optsRed, redOps)

			// 5) Check metamorphic equivalence for model and for real.
			if diff := cmp.Diff(mOrig, mRed); diff != "" {
				t.Fatalf("model: reduced writer buffer changed committed state (-original +reduced):\n%s\n\noriginal ops:\n%s\n\nreduced ops:\n%s", diff, fmtOps(origOps), fmtOps(redOps))
			}
			if diff := cmp.Diff(rOrig, rRed); diff != "" {
				t.Fatalf("real: reduced writer buffer changed committed state (-original +reduced):\n%s\n\noriginal ops:\n%s\n\nreduced ops:\n%s", diff, fmtOps(origOps), fmtOps(redOps))
			}

			// 6) Also cross-check model vs real for each fork.
			if diff := cmp.Diff(mOrig, rOrig); diff != "" {
				t.Fatalf("model vs real mismatch (original sequence) (-model +real):\n%s\n\nops:\n%s", diff, fmtOps(origOps))
			}
			if diff := cmp.Diff(mRed, rRed); diff != "" {
				t.Fatalf("model vs real mismatch (reduced sequence) (-model +real):\n%s\n\nops:\n%s", diff, fmtOps(redOps))
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Test: Update commutativity on existing slots
// -----------------------------------------------------------------------------

// Test_Metamorphic_UpdateCommutativity verifies:
//
// If two keys already have live slots, then Put(A); Put(B) and Put(B); Put(A)
// must produce identical final state.
//
// This tests that update order for *different existing keys* does not matter.
func Test_Metamorphic_Produces_Same_State_When_Update_Order_Differs(t *testing.T) {
	seedCount := 20

	for i := 0; i < seedCount; i++ {
		seed := int64(2000 + i)

		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			rng := rand.New(rand.NewSource(seed))

			options := slotcache.Options{
				Path:         filepath.Join(tmpDir, "base.slc"),
				KeySize:      8,
				IndexSize:    4,
				SlotCapacity: 64,
			}

			// Create base state with two live keys.
			keyA := randValidKey(rng, options.KeySize, nil)
			keyB := randValidKey(rng, options.KeySize, nil)

			// Ensure keys are distinct.
			for string(keyA) == string(keyB) {
				keyB = randValidKey(rng, options.KeySize, nil)
			}

			// Build base state with both keys live.
			baseModelFile, _ := model.NewFile(options)
			baseModelCache := model.Open(baseModelFile)
			baseWriter, _ := baseModelCache.BeginWrite()
			_ = baseWriter.Put(keyA, 1, randValidIdx(rng, options.IndexSize))
			_ = baseWriter.Put(keyB, 2, randValidIdx(rng, options.IndexSize))
			_ = baseWriter.Commit()
			_ = baseModelCache.Close()

			// Fork the base state.
			modelFileAB := baseModelFile.Clone()
			modelFileBA := baseModelFile.Clone()

			// Generate update values.
			revisionA := int64(rng.Intn(1000) + 100)
			revisionB := int64(rng.Intn(1000) + 100)
			indexA := randValidIdx(rng, options.IndexSize)
			indexB := randValidIdx(rng, options.IndexSize)

			// Execute Put(A); Put(B) on first fork.
			cacheAB := model.Open(modelFileAB)
			writerAB, _ := cacheAB.BeginWrite()
			_ = writerAB.Put(keyA, revisionA, indexA)
			_ = writerAB.Put(keyB, revisionB, indexB)
			_ = writerAB.Commit()
			entriesAB, _ := cacheAB.Scan(slotcache.ScanOpts{})
			_ = cacheAB.Close()

			// Execute Put(B); Put(A) on second fork.
			cacheBA := model.Open(modelFileBA)
			writerBA, _ := cacheBA.BeginWrite()
			_ = writerBA.Put(keyB, revisionB, indexB)
			_ = writerBA.Put(keyA, revisionA, indexA)
			_ = writerBA.Commit()
			entriesBA, _ := cacheBA.Scan(slotcache.ScanOpts{})
			_ = cacheBA.Close()

			// Compare: both should have the same entries (order may differ for different keys,
			// but the set of entries should be equivalent).
			slotcacheEntriesAB := toSlotcacheEntries(entriesAB)
			slotcacheEntriesBA := toSlotcacheEntries(entriesBA)

			if diff := cmp.Diff(slotcacheEntriesAB, slotcacheEntriesBA); diff != "" {
				t.Fatalf("update commutativity violated (-AB +BA):\n%s", diff)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Test: Abort equivalence
// -----------------------------------------------------------------------------

// Test_Metamorphic_AbortEquivalence verifies:
//
// BeginWrite; Put/Delete...; Abort must be equivalent to BeginWrite; Abort.
//
// In other words, aborting a writer session with buffered operations must
// leave the committed state unchanged.
func Test_Metamorphic_Preserves_State_When_Writer_Aborts_With_Operations(t *testing.T) {
	seedCount := 20

	for i := 0; i < seedCount; i++ {
		seed := int64(3000 + i)

		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			rng := rand.New(rand.NewSource(seed))

			options := slotcache.Options{
				Path:         filepath.Join(tmpDir, "base.slc"),
				KeySize:      8,
				IndexSize:    4,
				SlotCapacity: 64,
			}

			// Build a base state with some data.
			var keys [][]byte
			baseModelFile, _ := model.NewFile(options)
			baseModelCache := model.Open(baseModelFile)
			baseWriter, _ := baseModelCache.BeginWrite()
			for keyIndex := 0; keyIndex < 10; keyIndex++ {
				key := randValidKey(rng, options.KeySize, keys)
				idx := randValidIdx(rng, options.IndexSize)
				_ = baseWriter.Put(key, int64(keyIndex), idx)
				keys = append(keys, key)
			}
			_ = baseWriter.Commit()

			// Snapshot the base state.
			baseEntries, _ := baseModelCache.Scan(slotcache.ScanOpts{})
			baseSlotcacheEntries := toSlotcacheEntries(baseEntries)

			// Fork the base state.
			modelFileWithOps := baseModelFile.Clone()
			modelFileEmptyAbort := baseModelFile.Clone()

			// Execute BeginWrite; Put/Delete...; Abort on first fork.
			cacheWithOps := model.Open(modelFileWithOps)
			writerWithOps, _ := cacheWithOps.BeginWrite()
			for opIndex := 0; opIndex < 20; opIndex++ {
				if rng.Intn(2) == 0 {
					key := randValidKey(rng, options.KeySize, keys)
					idx := randValidIdx(rng, options.IndexSize)
					_ = writerWithOps.Put(key, int64(opIndex+100), idx)
				} else {
					key := randValidKey(rng, options.KeySize, keys)
					_, _ = writerWithOps.Delete(key)
				}
			}
			_ = writerWithOps.Abort()
			entriesAfterOpsAbort, _ := cacheWithOps.Scan(slotcache.ScanOpts{})
			_ = cacheWithOps.Close()

			// Execute BeginWrite; Abort on second fork.
			cacheEmptyAbort := model.Open(modelFileEmptyAbort)
			writerEmptyAbort, _ := cacheEmptyAbort.BeginWrite()
			_ = writerEmptyAbort.Abort()
			entriesAfterEmptyAbort, _ := cacheEmptyAbort.Scan(slotcache.ScanOpts{})
			_ = cacheEmptyAbort.Close()

			// Both should match the original base state.
			slotcacheEntriesAfterOpsAbort := toSlotcacheEntries(entriesAfterOpsAbort)
			slotcacheEntriesAfterEmptyAbort := toSlotcacheEntries(entriesAfterEmptyAbort)

			if diff := cmp.Diff(baseSlotcacheEntries, slotcacheEntriesAfterOpsAbort); diff != "" {
				t.Fatalf("abort with ops changed state (-base +afterOpsAbort):\n%s", diff)
			}
			if diff := cmp.Diff(baseSlotcacheEntries, slotcacheEntriesAfterEmptyAbort); diff != "" {
				t.Fatalf("empty abort changed state (-base +afterEmptyAbort):\n%s", diff)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Test: Overwritten new-key elimination
// -----------------------------------------------------------------------------

// Test_Metamorphic_OverwrittenNewKeyElimination verifies:
//
// Put(X); Delete(X); Commit must not allocate a slot and must not affect
// future ErrFull behavior.
//
// This tests that inserting then immediately deleting a new key in the same
// writer session is a no-op for slot allocation purposes.
func Test_Metamorphic_Eliminates_Allocation_When_Put_Then_Delete_Same_Key(t *testing.T) {
	t.Run("model", func(t *testing.T) {
		t.Parallel()

		options := slotcache.Options{
			Path:         "", // Not used for model.
			KeySize:      8,
			IndexSize:    4,
			SlotCapacity: 2,
		}

		rng := rand.New(rand.NewSource(4000))

		// Create a cache with capacity for 2 slots.
		modelFile, _ := model.NewFile(options)
		cache := model.Open(modelFile)

		// Insert one key to use one slot.
		keyA := randValidKey(rng, options.KeySize, nil)
		indexA := randValidIdx(rng, options.IndexSize)

		writer1, _ := cache.BeginWrite()
		_ = writer1.Put(keyA, 1, indexA)
		_ = writer1.Commit()

		// Now we have 1 slot used, 1 remaining.

		// In a single writer session: Put(newKey); Delete(newKey).
		keyNew := randValidKey(rng, options.KeySize, nil)
		for string(keyNew) == string(keyA) {
			keyNew = randValidKey(rng, options.KeySize, nil)
		}
		indexNew := randValidIdx(rng, options.IndexSize)

		writer2, _ := cache.BeginWrite()
		_ = writer2.Put(keyNew, 2, indexNew)
		_, _ = writer2.Delete(keyNew)
		_ = writer2.Commit()

		// The Put-then-Delete should have been a no-op for slot allocation.
		// We should still have only 1 slot used.
		if len(modelFile.Slots) != 1 {
			t.Fatalf("expected 1 slot after Put-Delete elimination, got %d", len(modelFile.Slots))
		}

		// We should be able to insert another key without hitting capacity.
		keyB := randValidKey(rng, options.KeySize, nil)
		for string(keyB) == string(keyA) || string(keyB) == string(keyNew) {
			keyB = randValidKey(rng, options.KeySize, nil)
		}
		indexB := randValidIdx(rng, options.IndexSize)

		writer3, _ := cache.BeginWrite()
		err := writer3.Put(keyB, 3, indexB)
		if err != nil {
			t.Fatalf("unexpected ErrFull after Put-Delete elimination: %v", err)
		}
		_ = writer3.Commit()

		// Now we should have 2 slots.
		if len(modelFile.Slots) != 2 {
			t.Fatalf("expected 2 slots after second Put, got %d", len(modelFile.Slots))
		}

		// A third Put should fail with ErrFull.
		keyC := randValidKey(rng, options.KeySize, nil)
		for string(keyC) == string(keyA) || string(keyC) == string(keyB) {
			keyC = randValidKey(rng, options.KeySize, nil)
		}
		indexC := randValidIdx(rng, options.IndexSize)

		writer4, _ := cache.BeginWrite()
		errC := writer4.Put(keyC, 4, indexC)
		if !errors.Is(errC, slotcache.ErrFull) {
			t.Fatalf("expected ErrFull for third key, got %v", errC)
		}
		_ = writer4.Abort()

		_ = cache.Close()
	})
}

// -----------------------------------------------------------------------------
// Test: Get ↔ Scan consistency
// -----------------------------------------------------------------------------

// Test_Metamorphic_GetScanConsistency verifies:
//
// Get(key) must return the same entry as filtering Scan() for that key.
//
// This tests that two fundamentally different code paths produce identical
// results:
//   - Get: O(1) hash table lookup via the bucket index
//   - Scan: O(n) linear traversal of the slots array
//
// If these disagree, it indicates corruption in either the hash index
// (wrong slot_id, hash collision mishandling) or the slot array (stale data,
// wrong tombstone handling).
//
// This is a read-only metamorphic test: no state forking needed because
// we're comparing two query methods on the same immutable snapshot.
func Test_Metamorphic_Get_Matches_Scan_When_Querying_Same_Key(t *testing.T) {
	seedCount := 20

	for i := 0; i < seedCount; i++ {
		seed := int64(5000 + i)

		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()

			rng := rand.New(rand.NewSource(seed))

			options := slotcache.Options{
				Path:         "",
				KeySize:      8,
				IndexSize:    4,
				SlotCapacity: 64,
			}

			// Build a state with some entries and some deletions.
			modelFile, _ := model.NewFile(options)
			cache := model.Open(modelFile)

			var keys [][]byte
			n := 30 + rng.Intn(30)

			for i := 0; i < n; i++ {
				writer, _ := cache.BeginWrite()

				if rng.Intn(100) < 80 {
					// 80%: Put
					key := randValidKey(rng, options.KeySize, keys)
					idx := randValidIdx(rng, options.IndexSize)
					_ = writer.Put(key, int64(i), idx)
					keys = append(keys, key)
				} else {
					// 20%: Delete
					if len(keys) > 0 {
						key := keys[rng.Intn(len(keys))]
						_, _ = writer.Delete(key)
					}
				}

				_ = writer.Commit()
			}

			// Get all entries via Scan.
			allEntries, err := cache.Scan(slotcache.ScanOpts{})
			if err != nil {
				t.Fatalf("Scan failed: %v", err)
			}

			// Build a map for quick lookup of scan results.
			entriesByKey := make(map[string]model.Entry)
			for _, entry := range allEntries {
				entriesByKey[string(entry.Key)] = entry
			}

			// For every key we ever inserted, verify Get matches Scan.
			for _, key := range keys {
				getEntry, getExists, getError := cache.Get(key)
				if getError != nil {
					t.Fatalf("Get(%x) failed: %v", key, getError)
				}

				scanEntry, scanExists := entriesByKey[string(key)]

				if getExists != scanExists {
					t.Fatalf("Get/Scan existence mismatch for key %x: Get.exists=%v, Scan.exists=%v",
						key, getExists, scanExists)
				}

				if getExists {
					if getEntry.Revision != scanEntry.Revision {
						t.Fatalf("Get/Scan revision mismatch for key %x: Get=%d, Scan=%d",
							key, getEntry.Revision, scanEntry.Revision)
					}
					if string(getEntry.Index) != string(scanEntry.Index) {
						t.Fatalf("Get/Scan index mismatch for key %x: Get=%x, Scan=%x",
							key, getEntry.Index, scanEntry.Index)
					}
				}
			}

			// Also test some keys that were never inserted.
			for testIndex := 0; testIndex < 10; testIndex++ {
				randomKey := make([]byte, options.KeySize)
				_, _ = rng.Read(randomKey)

				_, getExists, getError := cache.Get(randomKey)
				if getError != nil {
					t.Fatalf("Get(%x) failed: %v", randomKey, getError)
				}

				_, scanExists := entriesByKey[string(randomKey)]

				if getExists != scanExists {
					t.Fatalf("Get/Scan existence mismatch for random key %x: Get.exists=%v, Scan.exists=%v",
						randomKey, getExists, scanExists)
				}
			}

			_ = cache.Close()
		})
	}
}

// -----------------------------------------------------------------------------
// Test: ScanPrefix ↔ Scan + filter consistency
// -----------------------------------------------------------------------------

// Test_Metamorphic_ScanPrefixConsistency verifies:
//
// ScanPrefix(prefix) must return exactly the same entries as Scan() filtered
// to only entries where bytes.HasPrefix(entry.Key, prefix) is true.
//
// The spec defines prefix matching as: "A key k matches a prefix p iff
// k[0:len(p)] == p". This test verifies that ScanPrefix correctly implements
// this definition.
//
// This catches bugs like:
//   - Off-by-one errors in prefix comparison
//   - Incorrect early termination in prefix iteration
//   - Hash-based prefix filtering that misses entries
//
// This is a read-only metamorphic test: we compare two query methods on the
// same state without mutation.
func Test_Metamorphic_ScanPrefix_Matches_Filtered_Scan_When_Using_Same_Prefix(t *testing.T) {
	seedCount := 20

	for i := 0; i < seedCount; i++ {
		seed := int64(6000 + i)

		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()

			rng := rand.New(rand.NewSource(seed))

			options := slotcache.Options{
				Path:         "",
				KeySize:      8,
				IndexSize:    4,
				SlotCapacity: 64,
			}

			// Build a state with entries that share common prefixes.
			modelFile, _ := model.NewFile(options)
			cache := model.Open(modelFile)

			var allKeys [][]byte

			// Insert entries with some shared prefixes.
			prefixGroups := [][]byte{
				{0xAA, 0xBB},
				{0xAA, 0xCC},
				{0xDD},
				{0xEE, 0xFF, 0x00},
			}

			for _, prefix := range prefixGroups {
				// Insert 3-5 entries per prefix group.
				entriesInGroup := 3 + rng.Intn(3)
				for entryIndex := 0; entryIndex < entriesInGroup; entryIndex++ {
					key := make([]byte, options.KeySize)
					copy(key, prefix)
					// Fill the rest with random bytes.
					_, _ = rng.Read(key[len(prefix):])

					writer, _ := cache.BeginWrite()
					idx := randValidIdx(rng, options.IndexSize)
					_ = writer.Put(key, int64(len(allKeys)), idx)
					_ = writer.Commit()

					allKeys = append(allKeys, key)
				}
			}

			// Get full scan.
			all, err := cache.Scan(slotcache.ScanOpts{})
			if err != nil {
				t.Fatalf("Scan failed: %v", err)
			}

			// Test various prefixes.
			prefixes := [][]byte{
				{0xAA},       // Should match both 0xAABB... and 0xAACC... groups
				{0xAA, 0xBB}, // Should match only 0xAABB... group
				{0xDD},       // Should match 0xDD... group
				{0xEE, 0xFF}, // Should match 0xEEFF00... group
				{0x99},       // Should match nothing
				{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22}, // Full key length prefix
			}

			// Also add prefixes derived from actual keys.
			for _, key := range allKeys {
				for prefixLen := 1; prefixLen <= len(key); prefixLen++ {
					prefixes = append(prefixes, key[:prefixLen])
				}
			}

			for _, prefix := range prefixes {
				// Get entries via ScanPrefix.
				got, prefixError := cache.ScanPrefix(prefix, slotcache.ScanOpts{})
				if prefixError != nil {
					t.Fatalf("ScanPrefix(%x) failed: %v", prefix, prefixError)
				}

				// Manually filter full scan.
				var want []model.Entry
				for _, entry := range all {
					if len(entry.Key) >= len(prefix) {
						if string(entry.Key[:len(prefix)]) == string(prefix) {
							want = append(want, entry)
						}
					}
				}

				// Compare.
				prefixConverted := toSlotcacheEntries(got)
				expectedConverted := toSlotcacheEntries(want)

				if diff := cmp.Diff(expectedConverted, prefixConverted); diff != "" {
					t.Fatalf("ScanPrefix(%x) mismatch with filtered Scan (-expected +actual):\n%s",
						prefix, diff)
				}
			}

			_ = cache.Close()
		})
	}
}

// -----------------------------------------------------------------------------
// Test: Pagination slice equivalence
// -----------------------------------------------------------------------------

// Test_Metamorphic_PaginationSliceEquivalence verifies:
//
// Scan(offset=N, limit=M) must return exactly the same entries as Scan()[N:N+M].
//
// Pagination (offset/limit) should be semantically equivalent to fetching all
// results and slicing. This tests that:
//   - Offset correctly skips the first N entries
//   - Limit correctly caps the result count
//   - Edge cases (offset beyond length, limit=0) are handled correctly
//
// This is a read-only metamorphic test: we compare two ways of obtaining
// a subset of entries from the same state.
func Test_Metamorphic_Paginated_Scan_Matches_Slice_When_Using_Offset_And_Limit(t *testing.T) {
	seedCount := 20

	for i := 0; i < seedCount; i++ {
		seed := int64(7000 + i)

		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()

			rng := rand.New(rand.NewSource(seed))

			options := slotcache.Options{
				Path:         "",
				KeySize:      8,
				IndexSize:    4,
				SlotCapacity: 64,
			}

			// Build a state with a known number of entries.
			modelFile, _ := model.NewFile(options)
			cache := model.Open(modelFile)

			n := 15 + rng.Intn(20) // 15-34 entries
			for entryIndex := 0; entryIndex < n; entryIndex++ {
				writer, _ := cache.BeginWrite()
				key := randValidKey(rng, options.KeySize, nil)
				idx := randValidIdx(rng, options.IndexSize)
				_ = writer.Put(key, int64(entryIndex), idx)
				_ = writer.Commit()
			}

			// Get full scan (forward and reverse).
			fullForward, _ := cache.Scan(slotcache.ScanOpts{Reverse: false})
			fullReverse, _ := cache.Scan(slotcache.ScanOpts{Reverse: true})

			// Test various offset/limit combinations.
			testCases := []struct {
				offset  int
				limit   int
				reverse bool
			}{
				{0, 5, false},
				{5, 5, false},
				{0, 1, false},
				{0, 100, false}, // limit exceeds count
				{100, 5, false}, // offset exceeds count
				{3, 0, false},   // limit=0 means no limit
				{0, 5, true},    // reverse
				{5, 5, true},
				{10, 3, true},
			}

			// Add some random test cases.
			for randomIndex := 0; randomIndex < 10; randomIndex++ {
				testCases = append(testCases, struct {
					offset  int
					limit   int
					reverse bool
				}{
					offset:  rng.Intn(len(fullForward) + 10),
					limit:   rng.Intn(15),
					reverse: rng.Intn(2) == 0,
				})
			}

			for _, testCase := range testCases {
				scanOpts := slotcache.ScanOpts{
					Offset:  testCase.offset,
					Limit:   testCase.limit,
					Reverse: testCase.reverse,
				}

				pagedEntries, pagedError := cache.Scan(scanOpts)
				if pagedError != nil {
					t.Fatalf("Scan(%+v) failed: %v", scanOpts, pagedError)
				}

				// Compute expected slice manually.
				var fullEntries []model.Entry
				if testCase.reverse {
					fullEntries = fullReverse
				} else {
					fullEntries = fullForward
				}

				start := testCase.offset
				if start > len(fullEntries) {
					start = len(fullEntries)
				}

				var end int
				if testCase.limit == 0 {
					end = len(fullEntries)
				} else {
					end = start + testCase.limit
					if end > len(fullEntries) {
						end = len(fullEntries)
					}
				}

				want := fullEntries[start:end]

				// Compare.
				pagedConverted := toSlotcacheEntries(pagedEntries)
				expectedConverted := toSlotcacheEntries(want)

				if diff := cmp.Diff(expectedConverted, pagedConverted); diff != "" {
					t.Fatalf("pagination mismatch (offset=%d, limit=%d, reverse=%v, total=%d) (-expected +actual):\n%s",
						testCase.offset, testCase.limit, testCase.reverse, len(fullEntries), diff)
				}
			}

			_ = cache.Close()
		})
	}
}

// -----------------------------------------------------------------------------
// Test: Pagination concatenation
// -----------------------------------------------------------------------------

// Test_Metamorphic_PaginationConcatenation verifies:
//
// Scan(offset=0, limit=N) ++ Scan(offset=N, limit=M) == Scan(offset=0, limit=N+M)
//
// Consecutive pages should concatenate to form the equivalent larger page.
// This tests that pagination has no gaps or overlaps at page boundaries.
//
// This catches bugs like:
//   - Off-by-one errors at page boundaries
//   - Entries being duplicated or skipped between pages
//   - Inconsistent iteration state between calls
//
// This is a read-only metamorphic test.
func Test_Metamorphic_Pages_Concatenate_Correctly_When_Fetched_Sequentially(t *testing.T) {
	seedCount := 20

	for i := 0; i < seedCount; i++ {
		seed := int64(8000 + i)

		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()

			rng := rand.New(rand.NewSource(seed))

			options := slotcache.Options{
				Path:         "",
				KeySize:      8,
				IndexSize:    4,
				SlotCapacity: 64,
			}

			// Build a state with entries.
			modelFile, _ := model.NewFile(options)
			cache := model.Open(modelFile)

			n := 20 + rng.Intn(20) // 20-39 entries
			for entryIndex := 0; entryIndex < n; entryIndex++ {
				writer, _ := cache.BeginWrite()
				key := randValidKey(rng, options.KeySize, nil)
				idx := randValidIdx(rng, options.IndexSize)
				_ = writer.Put(key, int64(entryIndex), idx)
				_ = writer.Commit()
			}

			// Test concatenation for various page sizes.
			for attempt := 0; attempt < 15; attempt++ {
				pageSize1 := 1 + rng.Intn(10)
				pageSize2 := 1 + rng.Intn(10)
				reverse := rng.Intn(2) == 0

				// Get two consecutive pages.
				page1, err1 := cache.Scan(slotcache.ScanOpts{
					Offset:  0,
					Limit:   pageSize1,
					Reverse: reverse,
				})
				if err1 != nil {
					t.Fatalf("Scan page1 failed: %v", err1)
				}

				page2, err2 := cache.Scan(slotcache.ScanOpts{
					Offset:  pageSize1,
					Limit:   pageSize2,
					Reverse: reverse,
				})
				if err2 != nil {
					t.Fatalf("Scan page2 failed: %v", err2)
				}

				// Get combined page.
				combined, errCombined := cache.Scan(slotcache.ScanOpts{
					Offset:  0,
					Limit:   pageSize1 + pageSize2,
					Reverse: reverse,
				})
				if errCombined != nil {
					t.Fatalf("Scan combined failed: %v", errCombined)
				}

				// Concatenate page1 + page2.
				concatenated := append(page1, page2...)

				// Compare.
				concatenatedConverted := toSlotcacheEntries(concatenated)
				combinedConverted := toSlotcacheEntries(combined)

				if diff := cmp.Diff(combinedConverted, concatenatedConverted); diff != "" {
					t.Fatalf("pagination concatenation mismatch (page1=%d, page2=%d, reverse=%v) (-combined +concatenated):\n%s",
						pageSize1, pageSize2, reverse, diff)
				}
			}

			_ = cache.Close()
		})
	}
}

// -----------------------------------------------------------------------------
// Test: Delete return value consistency
// -----------------------------------------------------------------------------

// Test_Metamorphic_DeleteReturnValueConsistency verifies:
//
// Delete(key) must return existed=true iff the key was "effectively present"
// immediately before the call, considering on-disk state plus buffered ops.
//
// The spec states: "Delete(key) (bool, error) returns whether the key was
// effectively present immediately before the call, considering the on-disk
// state at BeginWrite plus buffered ops so far."
//
// This tests several laws:
//  1. Delete of a key that exists (via Get) must return existed=true
//  2. Delete of a key that doesn't exist must return existed=false
//  3. Second Delete of same key in same session must return existed=false
//  4. Put then Delete of same key in same session must return existed=true
//
// This requires state forking because we're testing write operations.
func Test_Metamorphic_Delete_Returns_Correct_Existed_When_Key_State_Varies(t *testing.T) {
	seedCount := 20

	for i := 0; i < seedCount; i++ {
		seed := int64(9000 + i)

		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()

			rng := rand.New(rand.NewSource(seed))

			options := slotcache.Options{
				Path:         "",
				KeySize:      8,
				IndexSize:    4,
				SlotCapacity: 64,
			}

			// Build a base state with some entries.
			modelFile, _ := model.NewFile(options)
			cache := model.Open(modelFile)

			var keys [][]byte
			for entryIndex := 0; entryIndex < 10; entryIndex++ {
				writer, _ := cache.BeginWrite()
				key := randValidKey(rng, options.KeySize, nil)
				idx := randValidIdx(rng, options.IndexSize)
				_ = writer.Put(key, int64(entryIndex), idx)
				_ = writer.Commit()
				keys = append(keys, key)
			}

			// Law 1: Delete of existing key returns true.
			for _, key := range keys {
				// Verify key exists via Get.
				_, exists, _ := cache.Get(key)
				if !exists {
					continue // Key may have been overwritten by duplicate random key.
				}

				// Fork state for this test.
				forkedFile := modelFile.Clone()
				forkedCache := model.Open(forkedFile)

				writer, _ := forkedCache.BeginWrite()
				existed, err := writer.Delete(key)
				if err != nil {
					t.Fatalf("Delete(%x) failed: %v", key, err)
				}
				if !existed {
					t.Fatalf("Delete(%x) returned existed=false but key exists in committed state", key)
				}
				_ = writer.Abort()
				_ = forkedCache.Close()
			}

			// Law 2: Delete of non-existing key returns false.
			for testIndex := 0; testIndex < 5; testIndex++ {
				key := make([]byte, options.KeySize)
				_, _ = rng.Read(key)

				// Verify key doesn't exist.
				_, exists, _ := cache.Get(key)
				if exists {
					continue // Unlikely collision with existing key.
				}

				writer, _ := cache.BeginWrite()
				existed, err := writer.Delete(key)
				if err != nil {
					t.Fatalf("Delete(%x) failed: %v", key, err)
				}
				if existed {
					t.Fatalf("Delete(%x) returned existed=true but key does not exist", key)
				}
				_ = writer.Abort()
			}

			// Law 3: Second Delete of same key returns false.
			if len(keys) > 0 {
				key := keys[0]

				// Fork state.
				forkedFile := modelFile.Clone()
				forkedCache := model.Open(forkedFile)

				writer, _ := forkedCache.BeginWrite()

				existed1, _ := writer.Delete(key)
				existed2, _ := writer.Delete(key)

				if !existed1 {
					// Key might not exist; skip this check.
				} else if existed2 {
					t.Fatalf("Second Delete(%x) returned existed=true; should be false after first delete", key)
				}

				_ = writer.Abort()
				_ = forkedCache.Close()
			}

			// Law 4: Put then Delete in same session returns true.
			newKey := make([]byte, options.KeySize)
			_, _ = rng.Read(newKey)

			// Verify key doesn't exist.
			_, exists, _ := cache.Get(newKey)
			if !exists {
				writer, _ := cache.BeginWrite()
				idx := randValidIdx(rng, options.IndexSize)
				_ = writer.Put(newKey, 999, idx)
				existed, _ := writer.Delete(newKey)
				if !existed {
					t.Fatalf("Delete after Put in same session returned existed=false")
				}
				_ = writer.Abort()
			}

			_ = cache.Close()
		})
	}
}

// -----------------------------------------------------------------------------
// Test: Reopen persistence
// -----------------------------------------------------------------------------

// Test_Metamorphic_ReopenPersistence verifies:
//
// All committed state must survive a Close/Open cycle.
//
// After closing a cache and reopening it, a Scan must return exactly the
// same entries as before the close. This tests the fundamental durability
// guarantee of a file-backed cache.
//
// This catches bugs like:
//   - Data not being flushed to disk before close
//   - Incorrect serialization/deserialization of entries
//   - Header counters not matching actual slot contents
//   - mmap not properly synced
//
// This test uses the real implementation (not model) because persistence
// is inherently about the on-disk format.
func Test_Metamorphic_State_Persists_When_Cache_Is_Reopened(t *testing.T) {
	seedCount := 15

	for i := 0; i < seedCount; i++ {
		seed := int64(10000 + i)

		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			rng := rand.New(rand.NewSource(seed))

			options := slotcache.Options{
				Path:         filepath.Join(tmpDir, "persist.slc"),
				KeySize:      8,
				IndexSize:    4,
				SlotCapacity: 64,
			}

			// Create and populate cache.
			cache1, err := slotcache.Open(options)
			if err != nil {
				t.Fatalf("initial Open failed: %v", err)
			}

			var allKeys [][]byte
			n := 20 + rng.Intn(30)

			for i := 0; i < n; i++ {
				writer, _ := cache1.BeginWrite()

				if rng.Intn(100) < 75 {
					// 75%: Put
					key := randValidKey(rng, options.KeySize, allKeys)
					idx := randValidIdx(rng, options.IndexSize)
					err := writer.Put(key, int64(i), idx)
					if err == nil {
						allKeys = append(allKeys, key)
					}
				} else {
					// 25%: Delete
					if len(allKeys) > 0 {
						key := allKeys[rng.Intn(len(allKeys))]
						_, _ = writer.Delete(key)
					}
				}

				_ = writer.Commit()
			}

			// Snapshot state before close.
			before, err := cache1.Scan(slotcache.ScanOpts{})
			if err != nil {
				t.Fatalf("Scan before close failed: %v", err)
			}
			beforeSlice := collectSeq(before)

			// Close.
			err = cache1.Close()
			if err != nil {
				t.Fatalf("Close failed: %v", err)
			}

			// Reopen.
			cache2, reerr := slotcache.Open(options)
			if reerr != nil {
				t.Fatalf("reopen failed: %v", reerr)
			}
			defer func() { _ = cache2.Close() }()

			// Snapshot state after reopen.
			after, err2 := cache2.Scan(slotcache.ScanOpts{})
			if err2 != nil {
				t.Fatalf("Scan after reopen failed: %v", err2)
			}
			afterSlice := collectSeq(after)

			// Compare.
			if diff := cmp.Diff(beforeSlice, afterSlice); diff != "" {
				t.Fatalf("state changed after reopen (-before +after):\n%s", diff)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Test: Len ↔ Scan count consistency
// -----------------------------------------------------------------------------

// Test_Metamorphic_LenScanCountConsistency verifies:
//
// Len() must always equal len(Scan()).
//
// The header maintains a `live_count` counter that Len() returns. This counter
// must exactly match the number of entries returned by a full Scan. If they
// disagree, it indicates counter drift (e.g., increment without actual insert,
// or decrement without actual delete).
//
// This test performs a series of random operations and verifies the invariant
// holds after each committed transaction.
//
// This is technically a model-vs-self check, but it's metamorphic in spirit:
// two ways of measuring "how many entries" must agree.
func Test_Metamorphic_Len_Equals_Scan_Count_When_Entries_Exist(t *testing.T) {
	seedCount := 20

	for i := 0; i < seedCount; i++ {
		seed := int64(11000 + i)

		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()

			rng := rand.New(rand.NewSource(seed))

			options := slotcache.Options{
				Path:         "",
				KeySize:      8,
				IndexSize:    4,
				SlotCapacity: 64,
			}

			modelFile, _ := model.NewFile(options)
			cache := model.Open(modelFile)

			var allKeys [][]byte

			// Perform operations and check invariant after each commit.
			n := 50 + rng.Intn(50)

			for i := 0; i < n; i++ {
				writer, _ := cache.BeginWrite()

				// Mix of puts and deletes.
				nOps := 1 + rng.Intn(5)
				for opIndex := 0; opIndex < nOps; opIndex++ {
					if rng.Intn(100) < 70 {
						key := randValidKey(rng, options.KeySize, allKeys)
						idx := randValidIdx(rng, options.IndexSize)
						_ = writer.Put(key, int64(i*100+opIndex), idx)
						allKeys = append(allKeys, key)
					} else if len(allKeys) > 0 {
						key := allKeys[rng.Intn(len(allKeys))]
						_, _ = writer.Delete(key)
					}
				}

				_ = writer.Commit()

				// Check invariant: Len() == len(Scan())
				got, err := cache.Len()
				if err != nil {
					t.Fatalf("Len() failed: %v", err)
				}

				entries, err := cache.Scan(slotcache.ScanOpts{})
				if err != nil {
					t.Fatalf("Scan() failed: %v", err)
				}

				if got != len(entries) {
					t.Fatalf("Len/Scan count mismatch after operation %d: Len()=%d, len(Scan())=%d",
						i, got, len(entries))
				}
			}

			_ = cache.Close()
		})
	}
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func copyFile(t *testing.T, src string, dst string) {
	t.Helper()

	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("failed to read file %q: %v", src, err)
	}

	err = os.WriteFile(dst, data, 0o600)
	if err != nil {
		t.Fatalf("failed to write file %q: %v", dst, err)
	}
}

func genWriterOps(rng *rand.Rand, options slotcache.Options, keys [][]byte, n int) []writerOp {
	// We intentionally generate only *valid* keys and indices here.
	// This keeps the metamorphic relation simple: both sequences should succeed.

	var operations []writerOp

	for i := 0; i < n; i++ {
		isPut := rng.Intn(100) < 70

		key := randValidKey(rng, options.KeySize, keys)

		if isPut {
			idx := randValidIdx(rng, options.IndexSize)
			operations = append(operations, writerOp{
				IsPut:    true,
				Key:      key,
				Revision: int64(rng.Intn(1000)),
				Index:    idx,
			})
			continue
		}

		operations = append(operations, writerOp{
			IsPut: false,
			Key:   key,
		})
	}

	return operations
}

func randValidKey(rng *rand.Rand, keySize int, keys [][]byte) []byte {
	// 70%: choose an existing key if available.
	if len(keys) > 0 && rng.Intn(100) < 70 {
		key := keys[rng.Intn(len(keys))]
		return append([]byte(nil), key...)
	}

	key := make([]byte, keySize)
	_, _ = rng.Read(key)
	return key
}

func randValidIdx(rng *rand.Rand, indexSize int) []byte {
	idx := make([]byte, indexSize)
	_, _ = rng.Read(idx)
	return idx
}

// reduceOps implements the metamorphic transformation:
// keep only the last op per key, preserving the order of those last ops.
func reduceOps(ops []writerOp) []writerOp {
	// Find the last index for each key.
	lastIdx := make(map[string]int, len(ops))
	for i, operation := range ops {
		lastIdx[string(operation.Key)] = i
	}

	// Collect only the final operations in their original order.
	var reduced []writerOp
	for i, operation := range ops {
		if lastIdx[string(operation.Key)] != i {
			continue
		}
		reduced = append(reduced, operation)
	}

	return reduced
}

func execModel(t *testing.T, file *model.FileState, operations []writerOp) []slotcache.Entry {
	t.Helper()

	cache := model.Open(file)
	defer func() { _ = cache.Close() }()

	w, err := cache.BeginWrite()
	if err != nil {
		t.Fatalf("model.BeginWrite failed unexpectedly: %v", err)
	}

	for _, operation := range operations {
		if operation.IsPut {
			err := w.Put(operation.Key, operation.Revision, operation.Index)
			if err != nil {
				t.Fatalf("model.Writer.Put failed unexpectedly: %v\nops:\n%s", err, fmtOps(operations))
			}
			continue
		}

		_, err := w.Delete(operation.Key)
		if err != nil {
			t.Fatalf("model.Writer.Delete failed unexpectedly: %v\nops:\n%s", err, fmtOps(operations))
		}
	}

	err = w.Commit()
	if err != nil {
		t.Fatalf("model.Writer.Commit failed unexpectedly: %v\nops:\n%s", err, fmtOps(operations))
	}

	entries, err := cache.Scan(slotcache.ScanOpts{})
	if err != nil {
		t.Fatalf("model.Scan failed unexpectedly: %v", err)
	}

	return toSlotcacheEntries(entries)
}

func execReal(t *testing.T, options slotcache.Options, operations []writerOp) []slotcache.Entry {
	t.Helper()

	cache, err := slotcache.Open(options)
	if err != nil {
		t.Fatalf("slotcache.Open failed unexpectedly: %v", err)
	}
	defer func() { _ = cache.Close() }()

	w, err := cache.BeginWrite()
	if err != nil {
		t.Fatalf("real.BeginWrite failed unexpectedly: %v", err)
	}

	for _, operation := range operations {
		if operation.IsPut {
			err := w.Put(operation.Key, operation.Revision, operation.Index)
			if err != nil {
				t.Fatalf("real.Writer.Put failed unexpectedly: %v\nops:\n%s", err, fmtOps(operations))
			}
			continue
		}

		_, err := w.Delete(operation.Key)
		if err != nil {
			t.Fatalf("real.Writer.Delete failed unexpectedly: %v\nops:\n%s", err, fmtOps(operations))
		}
	}

	err = w.Commit()
	if err != nil {
		t.Fatalf("real.Writer.Commit failed unexpectedly: %v\nops:\n%s", err, fmtOps(operations))
	}

	sequence, err := cache.Scan(slotcache.ScanOpts{})
	if err != nil {
		t.Fatalf("real.Scan failed unexpectedly: %v", err)
	}

	return collectSeq(sequence)
}

func fmtOps(operations []writerOp) string {
	output := ""
	for i, operation := range operations {
		output += fmt.Sprintf("%3d: %s\n", i, operation.String())
	}
	return output
}

// writerOp represents a single Put or Delete for metamorphic testing.
type writerOp struct {
	IsPut    bool
	Key      []byte
	Revision int64
	Index    []byte
}

func (operation writerOp) String() string {
	if operation.IsPut {
		return fmt.Sprintf("Put(%x, revision=%d, index=%x)", operation.Key, operation.Revision, operation.Index)
	}
	return fmt.Sprintf("Delete(%x)", operation.Key)
}
