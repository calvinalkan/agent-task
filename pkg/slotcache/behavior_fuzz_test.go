// Behavioral correctness: fuzz testing
//
// Oracle: in-memory behavioral model (internal/testutil/model)
// Technique: coverage-guided fuzzing (go test -fuzz)
//
// These tests verify that the real implementation's observable API behavior
// matches the simple in-memory model. They catch logic bugs in Get, Put,
// Delete, Scan, and transaction handling - but NOT file format issues.
//
// Failures here mean: "the API returned wrong results or wrong errors"

package slotcache_test

import (
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

// maxFuzzOperations is the maximum number of operations to run in a single fuzz iteration.
const maxFuzzOperations = 200

// FuzzBehavior_ModelVsReal is a coverage-guided fuzz test for public behavior.
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
	// Seed B: update existing key across sessions + offset beyond matches
	// Put(keyA)->Commit ; Put(keyA)->Commit ; Get(keyA) ; Scan(offset=4) => empty result
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
	// Seed D: Close discards buffered ops
	// Put(keyA)->Commit ; BeginWrite ; Put(keyB) ; Close ; Scan should still show only keyA
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
		0x80, 0x03, // Writer.Close

		0x80, 0x02, // Scan
		0xFF, 0x00, 0x00, 0x00,
	})

	// -------------------------------
	// Seed E: ErrBusy paths (Close/Reopen while writer active)
	// BeginWrite ; Close (expect ErrBusy) ; Reopen (expect ErrBusy) ; Writer.Close ; Close ; Reopen ; Len
	// -------------------------------
	f.Add([]byte{
		0x80, 0x04, // BeginWrite

		0x10, // Close() via roulette 13..25 => expect ErrBusy
		0x00, // Reopen() via roulette <13 => expect ErrBusy (writer still active)

		0x80, 0x03, // Writer.Close

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

	// -------------------------------
	// Seed I: filtered scans (exercises Filter field on scan ops)
	// See behavior_filter_seeddata_test.go for byte sequence details.
	// -------------------------------
	f.Add(seedBehaviorFilteredScans)

	// -------------------------------
	// Seed J: filter + pagination (Filter combined with Offset/Limit)
	// See behavior_filter_seeddata_test.go for byte sequence details.
	// -------------------------------
	f.Add(seedBehaviorFilterPagination)

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		tmpDir := t.TempDir()
		cacheFilePath := filepath.Join(tmpDir, "fuzz.slc")

		options := slotcache.Options{
			Path:         cacheFilePath,
			KeySize:      8,
			IndexSize:    4,
			SlotCapacity: 64,
		}

		testHarness := testutil.NewHarness(t, options)

		defer func() {
			_ = testHarness.Real.Cache.Close()
		}()

		decoder := testutil.NewFuzzDecoder(fuzzBytes, options)

		// We track keys that were successfully written at least once.
		// This increases the chance of hitting update/delete and prefix paths.
		var previouslySeenKeys [][]byte

		// Hard bound so one fuzz input cannot run forever.
		const maximumOperations = maxFuzzOperations

		for operationIndex := 0; operationIndex < maximumOperations && decoder.HasMore(); operationIndex++ {
			nextOperation := decoder.NextOp(testHarness, previouslySeenKeys)

			modelResult := testutil.ApplyModel(testHarness, nextOperation)
			realResult := testutil.ApplyReal(testHarness, nextOperation)

			testutil.RememberPutKey(nextOperation, modelResult, options.KeySize, &previouslySeenKeys)

			testutil.AssertOpMatch(t, nextOperation, modelResult, realResult)
			testutil.CompareState(t, testHarness)
		}
	})
}

// FuzzBehavior_ModelVsReal_OrderedKeys is the same as FuzzBehavior_ModelVsReal,
// but runs with OrderedKeys enabled to exercise ordered-mode semantics.
func FuzzBehavior_ModelVsReal_OrderedKeys(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Add([]byte{0xFF})
	f.Add([]byte("slotcache"))
	f.Add(make([]byte, 64))

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		tmpDir := t.TempDir()
		cacheFilePath := filepath.Join(tmpDir, "fuzz_ordered.slc")

		options := slotcache.Options{
			Path:         cacheFilePath,
			KeySize:      8,
			IndexSize:    4,
			SlotCapacity: 64,
			OrderedKeys:  true,
		}

		testHarness := testutil.NewHarness(t, options)

		defer func() {
			_ = testHarness.Real.Cache.Close()
		}()

		decoder := testutil.NewFuzzDecoder(fuzzBytes, options)

		var previouslySeenKeys [][]byte

		const maximumOperations = maxFuzzOperations

		for operationIndex := 0; operationIndex < maximumOperations && decoder.HasMore(); operationIndex++ {
			nextOperation := decoder.NextOp(testHarness, previouslySeenKeys)

			modelResult := testutil.ApplyModel(testHarness, nextOperation)
			realResult := testutil.ApplyReal(testHarness, nextOperation)

			testutil.RememberPutKey(nextOperation, modelResult, options.KeySize, &previouslySeenKeys)

			testutil.AssertOpMatch(t, nextOperation, modelResult, realResult)
			testutil.CompareState(t, testHarness)
		}
	})
}
