package slotcache_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

// FuzzSpec_GenerativeUsage_FuzzOptions is like FuzzSpec_GenerativeUsage, but
// derives slotcache.Options from the fuzz input so we exercise:
//   - key padding/alignment (KeySize != 8)
//   - IndexSize == 0
//   - tiny capacities (ErrFull, probe chains, tombstones)
//   - ordered/unordered mode
func FuzzSpec_GenerativeUsage_FuzzOptions(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{7, 4, 0, 15, 0, 0x80, 0x80, 0x80}) // common-ish options + some actions
	f.Add([]byte{0, 0, 0, 0, 1, 0x80, 0x80, 0x80})  // tiny options + ordered

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		tmpDir := t.TempDir()
		cacheFilePath := filepath.Join(tmpDir, "spec_gen_fuzz_opts.slc")

		options, rest := testutil.DeriveFuzzOptions(fuzzBytes, cacheFilePath)

		cache, openErr := slotcache.Open(options)
		if openErr != nil {
			t.Fatalf("Open failed unexpectedly: %v", openErr)
		}

		defer func() { _ = cache.Close() }()

		decoder := testutil.NewFuzzDecoder(rest, options)

		var (
			writer slotcache.Writer
			seen   [][]byte
		)

		const maximumSteps = 250

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
