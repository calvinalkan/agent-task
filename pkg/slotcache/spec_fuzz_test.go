// File format correctness: fuzz testing
//
// Oracle: spec_oracle (internal/testutil/spec_oracle.go)
// Technique: coverage-guided fuzzing (go test -fuzz)
//
// These tests drive the API with fuzz-derived operations, then validate
// the on-disk file format. The spec_oracle independently parses the file and
// checks all format invariants (header CRC, slot layout, bucket integrity,
// probe sequences).
//
// Failures here mean: "the file format violates the spec".

package slotcache_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

// FuzzSpec_GenerativeUsage drives the real API using fuzz-derived operations
// under a fixed, common configuration.
func FuzzSpec_GenerativeUsage(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("commit"))
	f.Add(make([]byte, 64))

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		tmpDir := t.TempDir()
		cacheFilePath := filepath.Join(tmpDir, "spec_gen_fuzz.slc")
		options := slotcache.Options{
			Path:         cacheFilePath,
			KeySize:      8,
			IndexSize:    4,
			UserVersion:  1,
			SlotCapacity: 64,
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

		const maximumSteps = 300

		for stepIndex := 0; stepIndex < maximumSteps && decoder.HasMore(); stepIndex++ {
			actionByte := decoder.NextByte()

			// ~3%: try a close/reopen cycle.
			if actionByte%100 < 3 {
				closeErr := cache.Close()
				if errors.Is(closeErr, slotcache.ErrBusy) {
					// Writer still active: spec says Close must be non-blocking and return ErrBusy.
					continue
				}

				var reopenErr error

				cache, reopenErr = slotcache.Open(options)
				if reopenErr != nil {
					// If reopen fails, the fuzzer will minimize. Treat as a bug.
					t.Fatalf("reopen failed: %v", reopenErr)
				}

				// If we successfully reopened, the file on disk must be valid.
				validationErr := testutil.ValidateFile(cacheFilePath, options)
				if validationErr != nil {
					t.Fatalf("speccheck failed after reopen: %s", testutil.DescribeSpecOracleError(validationErr))
				}

				writer = nil

				continue
			}

			writerIsActive := writer != nil

			if !writerIsActive {
				// No writer: pick BeginWrite, Invalidate, or read-only ops.
				switch actionByte % 100 {
				case 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22:
					var beginErr error

					writer, beginErr = cache.BeginWrite()
					_ = beginErr // ErrBusy/ErrClosed is fine
				case 23, 24, 25:
					// ~3%: Invalidate (terminal state).
					// After invalidation, reset the cache so fuzz iterations don't get stuck.
					invalidateErr := cache.Invalidate()
					if invalidateErr == nil || errors.Is(invalidateErr, slotcache.ErrInvalidated) {
						// Validate file format after invalidation.
						validationErr := testutil.ValidateFile(cacheFilePath, options)
						if validationErr != nil {
							t.Fatalf("speccheck failed after Invalidate: %s", testutil.DescribeSpecOracleError(validationErr))
						}

						// Reset: close, delete file, recreate.
						_ = cache.Close()
						_ = os.Remove(cacheFilePath)

						var reopenErr error

						cache, reopenErr = slotcache.Open(options)
						if reopenErr != nil {
							t.Fatalf("reopen after invalidation failed: %v", reopenErr)
						}

						writer = nil
						seen = nil
					}
				case 26, 27, 28, 29, 30, 31, 32, 33, 34:
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

			// Writer is active: choose Put/Delete/Commit/Close/SetUserHeader, with more weight on Put.
			switch actionByte % 100 {
			case 0, 1, 2, 3, 4, 5, 6, 7, 8, 9,
				10, 11, 12, 13, 14, 15, 16, 17, 18, 19,
				20, 21, 22, 23, 24, 25, 26, 27, 28, 29,
				30, 31, 32, 33, 34, 35, 36, 37, 38, 39,
				40, 41, 42, 43, 44, 45:
				key := decoder.NextKey(seen)
				idx := decoder.NextIndex()

				rev := decoder.NextInt64()

				err := writer.Put(key, rev, idx)
				if err == nil && len(key) == options.KeySize {
					seen = append(seen, append([]byte(nil), key...))
				}

			case 46, 47, 48, 49, 50, 51, 52, 53, 54, 55,
				56, 57, 58, 59, 60:
				key := decoder.NextKey(seen)
				_, _ = writer.Delete(key)

			case 61, 62, 63, 64, 65, 66, 67, 68, 69, 70, 71, 72, 73, 74:
				// Snapshot committed state before commit so we can verify failed commits
				// don't partially publish changes.
				beforeLen, beforeLenErr := cache.Len()
				beforeScan, beforeScanErr := cache.Scan(slotcache.ScanOptions{Reverse: false, Offset: 0, Limit: 0})

				commitErr := writer.Commit()
				writer = nil

				// Validate file format after ANY commit attempt that should not corrupt.
				if commitErr == nil ||
					errors.Is(commitErr, slotcache.ErrWriteback) ||
					errors.Is(commitErr, slotcache.ErrFull) ||
					errors.Is(commitErr, slotcache.ErrOutOfOrderInsert) {
					validationErr := testutil.ValidateFile(cacheFilePath, options)
					if validationErr != nil {
						t.Fatalf("speccheck failed after commit (err=%v): %s", commitErr, testutil.DescribeSpecOracleError(validationErr))
					}
				}

				// Stronger check: ErrFull / ErrOutOfOrderInsert must not partially publish.
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

			case 75, 76, 77, 78, 79, 80, 81, 82:
				// Abort: file must remain valid and state unchanged.
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

			case 83, 84, 85, 86:
				// ~4%: SetUserHeaderFlags
				flags := decoder.NextUint64()
				_ = writer.SetUserHeaderFlags(flags)

			case 87, 88, 89, 90:
				// ~4%: SetUserHeaderData
				var data [slotcache.UserDataSize]byte
				copy(data[:], decoder.NextBytes(slotcache.UserDataSize))
				_ = writer.SetUserHeaderData(data)

			default:
				// Mix in a few reads while writer active; they should observe committed state.
				_, _ = cache.Len()
			}
		}

		// If the fuzzer left a writer open, abort it.
		if writer != nil {
			_ = writer.Close()
		}
	})
}
