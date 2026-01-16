//go:build slotcache_impl

package slotcache_test

import (
	"encoding/binary"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

// FuzzBehavior_ModelVsReal is a coverage-guided fuzz test for *public behavior*.
//
// It does NOT try to validate the on-disk format. The oracle is the in-memory
// behavior model.
func FuzzBehavior_ModelVsReal(f *testing.F) {
	// A small seed corpus helps the fuzzer reach deeper states quickly.
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Add([]byte{0xFF})
	f.Add([]byte("slotcache"))
	f.Add(make([]byte, 64))

	// -------------------------------
	// Seed A: basic happy path
	// BeginWrite -> Put(key1) -> Commit -> Get(key1) -> Scan -> ScanPrefix(derived)
	// -------------------------------
	f.Add([]byte{
		0x80, 0x04, // BeginWrite

		0x80, 0x00, // Writer.Put
		0xFF,                                           // key mode: new valid
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, // key (8)
		0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // revision int64=1
		0xFF,                   // index mode: valid
		0xAA, 0xBB, 0xCC, 0xDD, // index (4)

		0x80, 0x02, // Writer.Commit

		0x80, 0x01, // Get
		0xFF,                                           // key mode: new valid
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, // same key

		0x80, 0x02, // Scan
		0xFF, 0x00, 0x00, 0x00, // scanopts: valid, offset=0, limit=0, reverse=false

		0x80, 0x03, // ScanPrefix
		0xFF, 0x00, 0x00, // prefix: valid, select key[0], prefixLenByte=0 => len=1
		0xFF, 0x00, 0x00, 0x00, // scanopts
	})

	// -------------------------------
	// Seed B: update existing key across sessions + offset out of bounds
	// Put(keyA)->Commit ; Put(keyA)->Commit ; Get(keyA) ; Scan(offset=4) => ErrOffsetOutOfBounds
	// -------------------------------
	f.Add([]byte{
		0x80, 0x04, // BeginWrite
		0x80, 0x00, // Put
		0xFF,
		0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, // keyA
		0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // rev=1
		0xFF, 0x01, 0x02, 0x03, 0x04, // idx1
		0x80, 0x02, // Commit

		0x80, 0x04, // BeginWrite
		0x80, 0x00, // Put (update same keyA)
		0xFF,
		0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, // keyA
		0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // rev=2
		0xFF, 0x05, 0x06, 0x07, 0x08, // idx2
		0x80, 0x02, // Commit

		0x80, 0x01, // Get(keyA)
		0xFF,
		0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17,

		0x80, 0x02, // Scan with offset=4 (mod 5 => 4)
		0xFF, 0x04, 0x00, 0x00, // valid scanopts, offset=4, limit=0, reverse=false
	})

	// -------------------------------
	// Seed C: delete committed key
	// Put(keyD)->Commit ; Delete(keyD)->Commit ; Get(keyD) should be missing ; Len()
	// -------------------------------
	f.Add([]byte{
		0x80, 0x04, // BeginWrite
		0x80, 0x00, // Put(keyD)
		0xFF,
		0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28,
		0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0xFF, 0xDE, 0xAD, 0xBE, 0xEF,
		0x80, 0x02, // Commit

		0x80, 0x04, // BeginWrite
		0x80, 0x01, // Delete(keyD)
		0xFF,
		0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28,
		0x80, 0x02, // Commit

		0x80, 0x01, // Get(keyD)
		0xFF,
		0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28,

		0x80, 0x00, // Len()
	})

	// -------------------------------
	// Seed D: abort discards buffered ops
	// Put(keyA)->Commit ; BeginWrite ; Put(keyB) ; Abort ; Scan should still show only keyA
	// -------------------------------
	f.Add([]byte{
		0x80, 0x04, // BeginWrite
		0x80, 0x00, // Put(keyA)
		0xFF,
		0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38,
		0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0xFF, 0x01, 0x01, 0x01, 0x01,
		0x80, 0x02, // Commit

		0x80, 0x04, // BeginWrite
		0x80, 0x00, // Put(keyB) (buffered only)
		0xFF,
		0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48,
		0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0xFF, 0x02, 0x02, 0x02, 0x02,
		0x80, 0x03, // Writer.Abort

		0x80, 0x02, // Scan
		0xFF, 0x00, 0x00, 0x00,
	})

	// -------------------------------
	// Seed E: ErrBusy paths (Close/Reopen while writer active)
	// BeginWrite ; Close (expect ErrBusy) ; Reopen (expect ErrBusy) ; Abort ; Close ; Reopen ; Len
	// -------------------------------
	f.Add([]byte{
		0x80, 0x04, // BeginWrite

		0x10, // Close() via roulette 13..25 => expect ErrBusy
		0x00, // Reopen() via roulette <13 => expect ErrBusy (writer still active)

		0x80, 0x03, // Writer.Abort

		0x10, // Close() now should succeed
		0x00, // Reopen() should succeed

		0x80, 0x00, // Len()
	})

	// -------------------------------
	// Seed F: invalid input surface area
	// Get(nil key) ; Scan(invalid opts) ; ScanPrefix(empty prefix) ; BeginWrite ; Put(invalid index len) ; Commit
	// -------------------------------
	f.Add([]byte{
		0x80, 0x01, // Get
		0x00, 0x01, // key mode<38 then nextBool=true => nil key

		0x80, 0x02, // Scan
		0x00, 0x01, // scanopts invalid mode<26, nextBool=true => offset=-1

		0x80, 0x03, // ScanPrefix
		0x00, 0x01, // prefix invalid: mode<52, invalidMode=1 => empty prefix
		0xFF, 0x00, 0x00, 0x00, // scanopts (won't matter; prefix invalid should win)

		0x80, 0x04, // BeginWrite

		0x80, 0x00, // Writer.Put
		0xFF,
		0x51, 0x52, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58, // key
		0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // rev=1
		0x00, 0x00, // index mode<26, wrongLength=0 => invalid index length
		0x80, 0x02, // Commit (should commit no-op)
	})

	// -------------------------------
	// Seed G: multi-key commit + reopen persistence
	// BeginWrite ; Put(A) ; Put(B) ; Commit ; Scan ; Reopen ; Scan ; Get(B)
	// -------------------------------
	f.Add([]byte{
		0x80, 0x04, // BeginWrite

		0x80, 0x00, // Put(A)
		0xFF,
		0x61, 0x62, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68,
		0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0xFF, 0x0A, 0x0A, 0x0A, 0x0A,

		0x80, 0x00, // Put(B)
		0xFF,
		0x71, 0x72, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78,
		0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0xFF, 0x0B, 0x0B, 0x0B, 0x0B,

		0x80, 0x02, // Commit

		0x80, 0x02, // Scan
		0xFF, 0x00, 0x00, 0x00,

		0x00, // Reopen

		0x80, 0x02, // Scan again
		0xFF, 0x00, 0x00, 0x00,

		0x80, 0x01, // Get(B)
		0xFF,
		0x71, 0x72, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78,
	})

	// -------------------------------
	// Seed H: prefix behavior (keys share prefixes)
	// BeginWrite ; Put(A=AA BB ..) ; Put(B=AA CC ..) ; Put(C=DD ..) ; Commit
	// ScanPrefix(AA) ; ScanPrefix(AA BB) ; ScanPrefix(DD)
	// -------------------------------
	f.Add([]byte{
		0x80, 0x04, // BeginWrite

		0x80, 0x00, // Put(A)
		0xFF,
		0xAA, 0xBB, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
		0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0xFF, 0xA1, 0xA1, 0xA1, 0xA1,

		0x80, 0x00, // Put(B)
		0xFF,
		0xAA, 0xCC, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02,
		0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0xFF, 0xB2, 0xB2, 0xB2, 0xB2,

		0x80, 0x00, // Put(C)
		0xFF,
		0xDD, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x03,
		0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0xFF, 0xC3, 0xC3, 0xC3, 0xC3,

		0x80, 0x02, // Commit

		// ScanPrefix derived from key[0] (A), prefixLen=1 => "AA" (matches A and B)
		0x80, 0x03,
		0xFF, 0x00, 0x00, // selectKeyIndex=0, prefixLenByte=0 => len=1
		0xFF, 0x00, 0x00, 0x00,

		// ScanPrefix derived from key[0] (A), prefixLen=2 => "AA BB" (matches only A)
		0x80, 0x03,
		0xFF, 0x00, 0x01, // prefixLenByte=1 => len=2
		0xFF, 0x00, 0x00, 0x00,

		// ScanPrefix derived from key[2] (C), prefixLen=1 => "DD" (matches only C)
		0x80, 0x03,
		0xFF, 0x02, 0x00, // selectKeyIndex=2, prefixLenByte=0 => len=1
		0xFF, 0x00, 0x00, 0x00,
	})

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		tmpDir := t.TempDir()
		cacheFilePath := filepath.Join(tmpDir, "fuzz.slc")

		options := slotcache.Options{
			Path:         cacheFilePath,
			KeySize:      8,
			IndexSize:    4,
			SlotCapacity: 64,
		}

		testHarness := newHarness(t, options)

		defer func() {
			_ = testHarness.real.cache.Close()
		}()

		decoder := newFuzzOperationDecoder(fuzzBytes, options)

		// We track keys that were successfully written at least once.
		// This increases the chance of hitting update/delete and prefix paths.
		var previouslySeenKeys [][]byte

		// Hard bound so one fuzz input cannot run forever.
		const maximumOperations = 200

		for operationIndex := 0; operationIndex < maximumOperations && decoder.hasMoreBytes(); operationIndex++ {
			nextOperation := decoder.nextOperation(testHarness, previouslySeenKeys)

			modelResult := applyModel(testHarness, nextOperation)
			realResult := applyReal(testHarness, nextOperation)

			rememberKeyAfterSuccessfulPutIfValid(nextOperation, modelResult, options.KeySize, &previouslySeenKeys)

			assertMatch(t, nextOperation, modelResult, realResult)
			compareObservableState(t, testHarness)
		}
	})
}

// fuzzOperationDecoder interprets fuzz bytes as a deterministic stream of choices.
//
// IMPORTANT: The decoder must be deterministic *for a given input* so Go's fuzzer
// can minimize failing inputs.
type fuzzOperationDecoder struct {
	rawBytes []byte
	cursor   int

	options slotcache.Options
}

func newFuzzOperationDecoder(fuzzBytes []byte, options slotcache.Options) *fuzzOperationDecoder {
	return &fuzzOperationDecoder{
		rawBytes: fuzzBytes,
		cursor:   0,
		options:  options,
	}
}

func (decoder *fuzzOperationDecoder) hasMoreBytes() bool {
	return decoder.cursor < len(decoder.rawBytes)
}

func (decoder *fuzzOperationDecoder) nextByte() byte {
	if decoder.cursor >= len(decoder.rawBytes) {
		return 0
	}

	value := decoder.rawBytes[decoder.cursor]
	decoder.cursor++

	return value
}

func (decoder *fuzzOperationDecoder) nextBool() bool {
	return (decoder.nextByte() & 0x01) == 1
}

func (decoder *fuzzOperationDecoder) nextInt64() int64 {
	// Little-endian, 8 bytes; if we run out, missing bytes are treated as 0.
	var raw [8]byte
	for index := range raw {
		raw[index] = decoder.nextByte()
	}

	return int64(binary.LittleEndian.Uint64(raw[:]))
}

// nextBytes reads exactly length bytes from the stream.
// If the stream ends, remaining bytes are filled with 0.
func (decoder *fuzzOperationDecoder) nextBytes(length int) []byte {
	if length <= 0 {
		return []byte{}
	}

	output := make([]byte, length)
	for index := range length {
		output[index] = decoder.nextByte()
	}

	return output
}

// nextOperation chooses the next operation based on the current harness state.
//
// It ONLY chooses operations that are valid *from the harness perspective*
// (e.g. it will not emit Writer.Put unless a writer is active), so failures are
// meaningful implementation/model issues, not harness panics.
func (decoder *fuzzOperationDecoder) nextOperation(testHarness *harness, previouslySeenKeys [][]byte) operation {
	writerIsActive := testHarness.model.writer != nil && testHarness.real.writer != nil

	// Mirror the property-test behavior: give Close/Reopen a chance even when a writer is active.
	// This is important because Close/Reopen are specified to return ErrBusy while a writer is active.
	roulette := decoder.nextByte()

	// ~5% Reopen
	if roulette < 13 {
		return opReopen{}
	}

	// ~5% Close
	if roulette >= 13 && roulette < 26 {
		return opClose{}
	}

	if !writerIsActive {
		choice := int(decoder.nextByte() % 5)

		switch choice {
		case 0:
			return opLen{}
		case 1:
			return opGet{Key: decoder.nextKey(testHarness.opts.KeySize, previouslySeenKeys)}
		case 2:
			return opScan{Options: decoder.nextScanOpts()}
		case 3:
			return opScanPrefix{
				Prefix:  decoder.nextPrefix(testHarness.opts.KeySize, previouslySeenKeys),
				Options: decoder.nextScanOpts(),
			}
		case 4:
			return opBeginWrite{}
		default:
			return opLen{}
		}
	}

	// Writer is active.
	choice := int(decoder.nextByte() % 8)

	switch choice {
	case 0:
		return opPut{
			Key:      decoder.nextKey(testHarness.opts.KeySize, previouslySeenKeys),
			Revision: decoder.nextInt64(),
			Index:    decoder.nextIndex(testHarness.opts.IndexSize),
		}
	case 1:
		return opDelete{Key: decoder.nextKey(testHarness.opts.KeySize, previouslySeenKeys)}
	case 2:
		return opCommit{}
	case 3:
		return opAbort{}
	case 4:
		return opLen{}
	case 5:
		return opGet{Key: decoder.nextKey(testHarness.opts.KeySize, previouslySeenKeys)}
	case 6:
		return opScan{Options: decoder.nextScanOpts()}
	case 7:
		return opScanPrefix{
			Prefix:  decoder.nextPrefix(testHarness.opts.KeySize, previouslySeenKeys),
			Options: decoder.nextScanOpts(),
		}
	default:
		return opLen{}
	}
}

func (decoder *fuzzOperationDecoder) nextKey(keySize int, previouslySeenKeys [][]byte) []byte {
	// Match the property test distribution:
	// - 15% invalid (nil or wrong length)
	// - 60% reuse an existing key
	// - otherwise generate a new key
	mode := decoder.nextByte()

	// ~15%
	if mode < 38 {
		if decoder.nextBool() {
			return nil
		}

		// Wrong length.
		wrongLength := int(decoder.nextByte()) % (keySize + 2)
		if wrongLength == keySize {
			wrongLength = keySize + 1
		}

		return decoder.nextBytes(wrongLength)
	}

	// ~60% (when we have seen keys)
	if len(previouslySeenKeys) > 0 && mode < 192 {
		selectedIndex := int(decoder.nextByte()) % len(previouslySeenKeys)
		selectedKey := previouslySeenKeys[selectedIndex]

		return append([]byte(nil), selectedKey...)
	}

	// New valid key.
	return decoder.nextBytes(keySize)
}

func (decoder *fuzzOperationDecoder) nextIndex(indexSize int) []byte {
	// Match property test distribution: 10% invalid length.
	mode := decoder.nextByte()

	// ~10%
	if mode < 26 {
		wrongLength := int(decoder.nextByte()) % (indexSize + 2)
		if wrongLength == indexSize {
			wrongLength = indexSize + 1
		}

		return decoder.nextBytes(wrongLength)
	}

	return decoder.nextBytes(indexSize)
}

func (decoder *fuzzOperationDecoder) nextPrefix(keySize int, previouslySeenKeys [][]byte) []byte {
	// Match property test distribution: 20% invalid.
	mode := decoder.nextByte()

	// ~20%
	if mode < 52 {
		invalidMode := int(decoder.nextByte()) % 3

		switch invalidMode {
		case 0:
			return nil
		case 1:
			return []byte{}
		case 2:
			return make([]byte, keySize+1)
		default:
			return nil
		}
	}

	// Prefer deriving a prefix from an existing key.
	if len(previouslySeenKeys) > 0 {
		selectedIndex := int(decoder.nextByte()) % len(previouslySeenKeys)
		selectedKey := previouslySeenKeys[selectedIndex]

		prefixLength := 1 + (int(decoder.nextByte()) % keySize) // 1..keySize

		return append([]byte(nil), selectedKey[:prefixLength]...)
	}

	prefixLength := 1 + (int(decoder.nextByte()) % keySize)

	return decoder.nextBytes(prefixLength)
}

func (decoder *fuzzOperationDecoder) nextScanOpts() slotcache.ScanOpts {
	// Match property test distribution: 10% invalid.
	mode := decoder.nextByte()

	// ~10%
	if mode < 26 {
		if decoder.nextBool() {
			return slotcache.ScanOpts{Reverse: false, Offset: -1, Limit: 0}
		}

		return slotcache.ScanOpts{Reverse: false, Offset: 0, Limit: -1}
	}

	// Keep these small; large offsets are still exercised by `ErrOffsetOutOfBounds`.
	offset := int(decoder.nextByte() % 5)
	limit := int(decoder.nextByte() % 4) // 0..3 (0 means unlimited)

	return slotcache.ScanOpts{
		Reverse: decoder.nextBool(),
		Offset:  offset,
		Limit:   limit,
	}
}
