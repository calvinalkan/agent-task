package slotcache_test

// Near-cap fuzz targets
//
// These fuzz tests run slotcache under a fixed "near-cap" configuration:
//   - KeySize=512
//   - IndexSize=16KiB
//   - SlotCapacity=64
//
// This configuration is intentionally NOT near the file-size/capacity caps; it is
// chosen to be large enough to stress record-layout arithmetic (padding/offsets),
// large key/index copy paths, and scan/filtering behavior, while still being cheap
// enough for fuzzing.
//
// Why a separate fuzz target (instead of extending the existing fuzz option
// generator)?
//   - The existing fuzz-option generator intentionally keeps sizes small to
//     maximize fuzz iteration throughput.
//   - Large per-entry records (like 16KiB indexes) drastically reduce the number
//     of operations the fuzzer can execute per second.
//   - Keeping this in a separate fuzz target gives it an independent corpus and
//     makes it easy to run in isolation via -fuzz / FUZZ_TARGET.

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

// Near-cap (but still safe) configuration used to exercise:
//   - large KeySize (max)
//   - large per-entry index bytes (moderately large, but well within caps)
//
// This is intentionally NOT near the file-size or capacity caps, so it stays
// cheap enough for fuzzing while still covering large-record logic.
const (
	nearCapKeySize   = 512
	nearCapIndexSize = 16 * 1024
	nearCapCapacity  = 64
)

// FuzzSpec_GenerativeUsage_NearCapConfig drives the real API under the near-cap
// configuration and validates the on-disk file against the spec_oracle.
func FuzzSpec_GenerativeUsage_NearCapConfig(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("commit"))
	f.Add(make([]byte, 64))
	// Big enough to allow multiple Put operations with a 16KiB index.
	f.Add(make([]byte, 64*1024))

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		tmpDir := t.TempDir()
		cacheFilePath := filepath.Join(tmpDir, "spec_gen_fuzz_nearcap.slc")

		options := slotcache.Options{
			Path:         cacheFilePath,
			KeySize:      nearCapKeySize,
			IndexSize:    nearCapIndexSize,
			UserVersion:  1,
			SlotCapacity: nearCapCapacity,
		}

		cache, openErr := slotcache.Open(options)
		if openErr != nil {
			t.Fatalf("Open failed unexpectedly: %v", openErr)
		}

		defer func() { _ = cache.Close() }()

		decoder := testutil.NewFuzzDecoder(fuzzBytes, options)

		var (
			writer slotcache.Writer
			seen   [][]byte
		)

		const maximumSteps = 251

		for stepIndex := 0; stepIndex < maximumSteps && decoder.HasMore(); stepIndex++ {
			actionByte := decoder.NextByte()

			// Occasionally close/reopen; on successful reopen the file must validate.
			if actionByte%100 < 3 {
				closeErr := cache.Close()
				if errors.Is(closeErr, slotcache.ErrBusy) {
					continue
				}

				var reopenErr error

				cache, reopenErr = slotcache.Open(options)
				if reopenErr != nil {
					t.Fatalf("reopen failed: %v", reopenErr)
				}

				validationErr := testutil.ValidateFile(cacheFilePath, options)
				if validationErr != nil {
					t.Fatalf("speccheck failed after reopen: %s", testutil.DescribeSpecOracleError(validationErr))
				}

				writer = nil

				continue
			}

			if writer == nil {
				// No writer.
				switch actionByte % 100 {
				case 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24:
					w, err := cache.BeginWrite()
					if err == nil {
						writer = w
					}
				case 25, 26, 27, 28, 29, 30, 31, 32, 33, 34:
					_, _ = cache.Len()
				case 35, 36, 37, 38, 39, 40, 41, 42, 43, 44:
					key := decoder.NextKey(seen)
					_, _, _ = cache.Get(key)
				case 45, 46, 47, 48, 49, 50, 51, 52, 53, 54:
					_, _ = cache.Scan(slotcache.ScanOptions{Reverse: false, Offset: 0, Limit: 0})
				default:
					prefix := decoder.NextPrefix(options.KeySize)
					_, _ = cache.ScanPrefix(prefix, slotcache.ScanOptions{Reverse: false, Offset: 0, Limit: 0})
				}

				continue
			}

			// Writer active.
			switch actionByte % 100 {
			case 0, 1, 2, 3, 4, 5, 6, 7, 8, 9,
				10, 11, 12, 13, 14, 15, 16, 17, 18, 19,
				20, 21, 22, 23, 24, 25, 26, 27, 28, 29,
				30, 31, 32, 33, 34, 35, 36, 37, 38, 39,
				40, 41, 42, 43, 44, 45, 46, 47, 48, 49:
				key := decoder.NextKey(seen)
				idx := decoder.NextIndex()
				rev := decoder.NextInt64()

				err := writer.Put(key, rev, idx)
				if err == nil && len(key) == options.KeySize {
					seen = append(seen, append([]byte(nil), key...))
				}

			case 50, 51, 52, 53, 54, 55, 56, 57, 58, 59,
				60, 61, 62, 63, 64:
				key := decoder.NextKey(seen)
				_, _ = writer.Delete(key)

			case 65, 66, 67, 68, 69, 70, 71, 72, 73, 74, 75, 76, 77, 78:
				beforeLen, beforeLenErr := cache.Len()
				beforeScan, beforeScanErr := cache.Scan(slotcache.ScanOptions{Reverse: false, Offset: 0, Limit: 0})

				commitErr := writer.Commit()
				writer = nil

				if commitErr == nil ||
					errors.Is(commitErr, slotcache.ErrWriteback) ||
					errors.Is(commitErr, slotcache.ErrFull) ||
					errors.Is(commitErr, slotcache.ErrOutOfOrderInsert) {
					validationErr := testutil.ValidateFile(cacheFilePath, options)
					if validationErr != nil {
						t.Fatalf("speccheck failed after commit (err=%v): %s", commitErr, testutil.DescribeSpecOracleError(validationErr))
					}
				}

				if errors.Is(commitErr, slotcache.ErrFull) || errors.Is(commitErr, slotcache.ErrOutOfOrderInsert) {
					afterLen, afterLenErr := cache.Len()
					afterScan, afterScanErr := cache.Scan(slotcache.ScanOptions{Reverse: false, Offset: 0, Limit: 0})

					if beforeLenErr == nil && afterLenErr == nil && beforeLen != afterLen {
						t.Fatalf("commit failed (%v) but Len() changed: before=%d after=%d", commitErr, beforeLen, afterLen)
					}

					if beforeScanErr == nil && afterScanErr == nil {
						if diff := testutil.DiffEntries(beforeScan, afterScan); diff != "" {
							t.Fatalf("commit failed (%v) but Scan() changed:\n%s", commitErr, diff)
						}
					}
				}

			case 79, 80, 81, 82, 83, 84, 85, 86:
				beforeLen, beforeLenErr := cache.Len()
				beforeScan, beforeScanErr := cache.Scan(slotcache.ScanOptions{Reverse: false, Offset: 0, Limit: 0})

				_ = writer.Close()
				writer = nil

				validationErr := testutil.ValidateFile(cacheFilePath, options)
				if validationErr != nil {
					t.Fatalf("speccheck failed after Writer.Close(): %s", testutil.DescribeSpecOracleError(validationErr))
				}

				afterLen, afterLenErr := cache.Len()
				afterScan, afterScanErr := cache.Scan(slotcache.ScanOptions{Reverse: false, Offset: 0, Limit: 0})

				if beforeLenErr == nil && afterLenErr == nil && beforeLen != afterLen {
					t.Fatalf("Writer.Close() changed Len(): before=%d after=%d", beforeLen, afterLen)
				}

				if beforeScanErr == nil && afterScanErr == nil {
					if diff := testutil.DiffEntries(beforeScan, afterScan); diff != "" {
						t.Fatalf("Writer.Close() changed Scan():\n%s", diff)
					}
				}

			default:
				_, _ = cache.Len()
			}
		}

		if writer != nil {
			_ = writer.Close()
		}
	})
}

func FuzzBehavior_ModelVsReal_NearCapConfig(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Add([]byte{0xFF})
	// Big enough to allow multiple Put operations with a 16KiB index.
	f.Add(make([]byte, 64*1024))

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		tmpDir := t.TempDir()
		cacheFilePath := filepath.Join(tmpDir, "behavior_fuzz_nearcap.slc")

		options := slotcache.Options{
			Path:         cacheFilePath,
			KeySize:      nearCapKeySize,
			IndexSize:    nearCapIndexSize,
			SlotCapacity: nearCapCapacity,
		}

		h := testutil.NewHarness(t, options)

		defer func() { _ = h.Real.Cache.Close() }()

		decoder := testutil.NewFuzzDecoder(fuzzBytes, options)

		var previouslySeenKeys [][]byte

		for opIndex := 0; opIndex < maxFuzzOperations && decoder.HasMore(); opIndex++ {
			op := decoder.NextOp(h, previouslySeenKeys)

			modelResult := testutil.ApplyModel(h, op)
			realResult := testutil.ApplyReal(h, op)

			testutil.RememberPutKey(op, modelResult, options.KeySize, &previouslySeenKeys)

			testutil.AssertOpMatch(t, op, modelResult, realResult)
			testutil.CompareState(t, h)
		}
	})
}

func FuzzBehavior_ModelVsReal_NearCapConfig_OrderedKeys(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Add([]byte{0xFF})
	// Big enough to allow multiple Put operations with a 16KiB index.
	f.Add(make([]byte, 64*1024))

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		tmpDir := t.TempDir()
		cacheFilePath := filepath.Join(tmpDir, "behavior_fuzz_nearcap_ordered.slc")

		options := slotcache.Options{
			Path:         cacheFilePath,
			KeySize:      nearCapKeySize,
			IndexSize:    nearCapIndexSize,
			SlotCapacity: nearCapCapacity,
			OrderedKeys:  true,
		}

		h := testutil.NewHarness(t, options)

		defer func() { _ = h.Real.Cache.Close() }()

		decoder := testutil.NewFuzzDecoder(fuzzBytes, options)

		var previouslySeenKeys [][]byte

		for opIndex := 0; opIndex < maxFuzzOperations && decoder.HasMore(); opIndex++ {
			op := decoder.NextOp(h, previouslySeenKeys)

			modelResult := testutil.ApplyModel(h, op)
			realResult := testutil.ApplyReal(h, op)

			testutil.RememberPutKey(op, modelResult, options.KeySize, &previouslySeenKeys)

			testutil.AssertOpMatch(t, op, modelResult, realResult)
			testutil.CompareState(t, h)
		}
	})
}
