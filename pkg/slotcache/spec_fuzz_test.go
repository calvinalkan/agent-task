// File format correctness: fuzz testing
//
// Oracle: spec_oracle (internal/testutil/spec_oracle.go)
// Technique: coverage-guided fuzzing (go test -fuzz)
//
// These tests drive the API with fuzz-derived operations, then validate
// the on-disk file format after each successful commit. The spec_oracle
// independently parses the file and checks all format invariants (header
// CRC, slot layout, bucket integrity, probe sequences).
//
// Failures here mean: "the file format violates the spec"

package slotcache_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

// FuzzSpec_GenerativeUsage drives the real API using fuzz-derived operations.
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

		cache, openError := slotcache.Open(options)
		if openError != nil {
			t.Fatalf("Open failed unexpectedly: %v", openError)
		}

		defer func() {
			_ = cache.Close()
		}()

		decoder := testutil.NewFuzzDecoder(fuzzBytes, options)

		var writer slotcache.Writer

		const maximumSteps = 300

		for stepIndex := 0; stepIndex < maximumSteps && decoder.HasMore(); stepIndex++ {
			// 1 byte drives the next action.
			actionByte := decoder.NextByte()

			// Allow close/reopen sometimes to exercise Open invariants.
			if actionByte%100 < 3 {
				_ = cache.Close()

				var reopenError error

				cache, reopenError = slotcache.Open(options)
				if reopenError != nil {
					// If reopen fails, the fuzzer will minimize. Treat as a bug.
					t.Fatalf("reopen failed: %v", reopenError)
				}

				writer = nil

				continue
			}

			writerIsActive := writer != nil

			if !writerIsActive {
				// No writer: choose BeginWrite or read-only ops.
				switch actionByte % 5 {
				case 0:
					var beginError error

					writer, beginError = cache.BeginWrite()
					_ = beginError // if ErrBusy/ErrClosed, that's fine

				case 1:
					_, _ = cache.Len()

				case 2:
					keyBytes := decoder.NextBytes(options.KeySize)
					_, _, _ = cache.Get(keyBytes)

				case 3:
					_, _ = cache.Scan(slotcache.ScanOptions{Reverse: false, Offset: 0, Limit: 0})

				case 4:
					prefixBytes := decoder.NextPrefix(options.KeySize)
					_, _ = cache.ScanPrefix(prefixBytes, slotcache.ScanOptions{Reverse: false, Offset: 0, Limit: 0})
				}

				continue
			}

			// Writer is active: choose Put/Delete/Commit/Close.
			switch actionByte % 6 {
			case 0:
				keyBytes := decoder.NextBytes(options.KeySize)
				indexBytes := decoder.NextBytes(options.IndexSize)
				revision := decoder.NextInt64()
				_ = writer.Put(keyBytes, revision, indexBytes)

			case 1:
				keyBytes := decoder.NextBytes(options.KeySize)
				_, _ = writer.Delete(keyBytes)

			case 2:
				commitError := writer.Commit()
				writer = nil

				// Treat ErrWriteback as published: commit completed but durability failed.
				if commitError == nil || errors.Is(commitError, slotcache.ErrWriteback) {
					// Validate the file format of the published cache.
					validationError := testutil.ValidateFile(cacheFilePath, options)
					if validationError != nil {
						t.Fatalf("speccheck failed after commit: %v", validationError)
					}
				}

			case 3:
				_ = writer.Close()
				writer = nil

			case 4:
				// Mix in some reads even while writer active; reads should observe committed state.
				_, _ = cache.Len()

			case 5:
				keyBytes := decoder.NextBytes(options.KeySize)
				_, _, _ = cache.Get(keyBytes)
			}
		}

		// If the fuzzer left a writer open, abort it.
		if writer != nil {
			_ = writer.Close()
		}
	})
}
