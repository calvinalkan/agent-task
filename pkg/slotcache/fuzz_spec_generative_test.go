//go:build slotcache_impl

package slotcache_test

import (
	"encoding/binary"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

// FuzzSpec_GenerativeUsage drives the real API using fuzz-derived operations.
//
// After every successful commit, it validates the on-disk file format with the
// independent spec oracle.
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

		cacheHandle, openError := slotcache.Open(options)
		if openError != nil {
			t.Fatalf("Open failed unexpectedly: %v", openError)
		}

		defer func() {
			_ = cacheHandle.Close()
		}()

		decoder := newSpecFuzzDecoder(fuzzBytes)

		var writerHandle slotcache.Writer

		const maximumSteps = 300

		for stepIndex := 0; stepIndex < maximumSteps && decoder.hasMoreBytes(); stepIndex++ {
			// 1 byte drives the next action.
			actionByte := decoder.nextByte()

			// Allow close/reopen sometimes to exercise Open invariants.
			if actionByte%100 < 3 {
				_ = cacheHandle.Close()

				var reopenError error

				cacheHandle, reopenError = slotcache.Open(options)
				if reopenError != nil {
					// If reopen fails, the fuzzer will minimize. Treat as a bug.
					t.Fatalf("reopen failed: %v", reopenError)
				}

				writerHandle = nil

				continue
			}

			writerIsActive := writerHandle != nil

			if !writerIsActive {
				// No writer: choose BeginWrite or read-only ops.
				switch actionByte % 5 {
				case 0:
					var beginError error

					writerHandle, beginError = cacheHandle.BeginWrite()
					_ = beginError // if ErrBusy/ErrClosed, that's fine

				case 1:
					_, _ = cacheHandle.Len()

				case 2:
					keyBytes := decoder.nextKeyBytes(options.KeySize)
					_, _, _ = cacheHandle.Get(keyBytes)

				case 3:
					_, _ = cacheHandle.Scan(slotcache.ScanOpts{Reverse: false, Offset: 0, Limit: 0})

				case 4:
					prefixBytes := decoder.nextPrefixBytes(options.KeySize)
					_, _ = cacheHandle.ScanPrefix(prefixBytes, slotcache.ScanOpts{Reverse: false, Offset: 0, Limit: 0})
				}

				continue
			}

			// Writer is active: choose Put/Delete/Commit/Abort.
			switch actionByte % 6 {
			case 0:
				keyBytes := decoder.nextKeyBytes(options.KeySize)
				indexBytes := decoder.nextIndexBytes(options.IndexSize)
				revision := decoder.nextRevision()
				_ = writerHandle.Put(keyBytes, revision, indexBytes)

			case 1:
				keyBytes := decoder.nextKeyBytes(options.KeySize)
				_, _ = writerHandle.Delete(keyBytes)

			case 2:
				commitError := writerHandle.Commit()
				writerHandle = nil

				if commitError == nil {
					// Validate the file format of the published cache.
					validationError := validateSlotcacheFileAgainstOptions(cacheFilePath, options)
					if validationError != nil {
						t.Fatalf("speccheck failed after commit: %v", validationError)
					}
				}

			case 3:
				_ = writerHandle.Abort()
				writerHandle = nil

			case 4:
				// Mix in some reads even while writer active; reads should observe committed state.
				_, _ = cacheHandle.Len()

			case 5:
				keyBytes := decoder.nextKeyBytes(options.KeySize)
				_, _, _ = cacheHandle.Get(keyBytes)
			}
		}

		// If the fuzzer left a writer open, abort it.
		if writerHandle != nil {
			_ = writerHandle.Abort()
		}
	})
}

// specFuzzDecoder is a minimal byte reader for spec f.
// It is deterministic given the input bytes.
type specFuzzDecoder struct {
	rawBytes []byte
	cursor   int
}

func newSpecFuzzDecoder(fuzzBytes []byte) *specFuzzDecoder {
	return &specFuzzDecoder{rawBytes: fuzzBytes}
}

func (decoder *specFuzzDecoder) hasMoreBytes() bool {
	return decoder.cursor < len(decoder.rawBytes)
}

func (decoder *specFuzzDecoder) nextByte() byte {
	if decoder.cursor >= len(decoder.rawBytes) {
		return 0
	}

	b := decoder.rawBytes[decoder.cursor]
	decoder.cursor++

	return b
}

func (decoder *specFuzzDecoder) nextRevision() int64 {
	// Read 8 bytes LE if available; otherwise 0.
	if decoder.cursor+8 > len(decoder.rawBytes) {
		decoder.cursor = len(decoder.rawBytes)

		return 0
	}

	revision := int64(binary.LittleEndian.Uint64(decoder.rawBytes[decoder.cursor : decoder.cursor+8]))
	decoder.cursor += 8

	return revision
}

func (decoder *specFuzzDecoder) nextKeyBytes(keySize int) []byte {
	keyBytes := make([]byte, keySize)

	for i := range keySize {
		keyBytes[i] = decoder.nextByte()
	}

	return keyBytes
}

func (decoder *specFuzzDecoder) nextIndexBytes(indexSize int) []byte {
	indexBytes := make([]byte, indexSize)

	for i := range indexSize {
		indexBytes[i] = decoder.nextByte()
	}

	return indexBytes
}

func (decoder *specFuzzDecoder) nextPrefixBytes(keySize int) []byte {
	// Prefix length in [1..keySize].
	length := 1 + int(decoder.nextByte())%keySize

	prefixBytes := make([]byte, length)

	for i := range length {
		prefixBytes[i] = decoder.nextByte()
	}

	return prefixBytes
}
