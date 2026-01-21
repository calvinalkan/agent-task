// Curated fuzz corpus seeds for behavior testing.
//
// These byte sequences are carefully constructed to exercise specific code
// paths in the fuzz decoder. They are shared between fuzz tests (as seeds)
// and guard tests that verify they still emit the intended operations.
//
// If the decoder's byte consumption changes, these seeds may need updating.
// Guard tests will fail if seeds no longer emit their intended behavior.
//
// Each seed documents its intended purpose and expected operation sequence.
// The byte encoding follows the FuzzDecoder protocol defined in fuzz_decoder.go.

package testutil

// -----------------------------------------------------------------------------
// Core behavior seeds (A-H).
//
// These seeds exercise fundamental API operations and error paths.
// They form the initial corpus for FuzzBehavior_ModelVsReal.
// -----------------------------------------------------------------------------

// SeedBasicHappyPath exercises the core write->read path.
//
// Sequence:
//   - BeginWrite -> Put(key1) -> Commit -> Get(key1) -> Scan -> ScanPrefix(derived)
//
// Validates: basic put/get, scan enumeration, prefix derivation.
var SeedBasicHappyPath = []byte{
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
}

// SeedUpdateExistingKey exercises key updates across writer sessions.
//
// Sequence:
//   - Put(keyA)->Commit ; Put(keyA)->Commit ; Get(keyA) ; Scan(offset=4)
//
// Validates: revision updates, offset beyond matches returns empty.
var SeedUpdateExistingKey = []byte{
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
}

// SeedDeleteCommittedKey exercises the delete path.
//
// Sequence:
//   - Put(keyD)->Commit ; Delete(keyD)->Commit ; Get(keyD) should be missing ; Len()
//
// Validates: delete marks key as missing, len reflects deletion.
var SeedDeleteCommittedKey = []byte{
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
}

// SeedCloseDiscardsBuffered exercises writer close discarding uncommitted ops.
//
// Sequence:
//   - Put(keyA)->Commit ; BeginWrite ; Put(keyB) ; Close ; Scan shows only keyA
//
// Validates: Writer.Close discards buffered ops without committing.
var SeedCloseDiscardsBuffered = []byte{
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
}

// SeedErrBusyPaths exercises ErrBusy error paths.
//
// Sequence:
//   - BeginWrite ; Close (ErrBusy) ; Reopen (ErrBusy) ; Writer.Close ; Close ; Reopen ; Len
//
// Validates: Close/Reopen return ErrBusy when writer is active.
var SeedErrBusyPaths = []byte{
	0x80, 0x04, // BeginWrite

	0x10, // Close() via roulette 13..25 => expect ErrBusy
	0x00, // Reopen() via roulette <13 => expect ErrBusy (writer still active)

	0x80, 0x03, // Writer.Close

	0x10, // Close() now should succeed
	0x00, // Reopen() should succeed

	0x80, 0x00, // Len()
}

// SeedInvalidInputs exercises invalid input handling.
//
// Sequence:
//   - Get(nil key) ; Scan(invalid opts) ; ScanPrefix(empty prefix) ; BeginWrite ; Put(invalid index) ; Commit
//
// Validates: proper error handling for nil/invalid inputs.
var SeedInvalidInputs = []byte{
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
}

// SeedMultiKeyPersistence exercises multi-key commit and reopen persistence.
//
// Sequence:
//   - BeginWrite ; Put(A) ; Put(B) ; Commit ; Scan ; Reopen ; Scan ; Get(B)
//
// Validates: multi-key commit, persistence across reopen.
var SeedMultiKeyPersistence = []byte{
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
}

// SeedPrefixBehavior exercises prefix scan with shared prefixes.
//
// Sequence:
//   - BeginWrite ; Put(A=AA BB ..) ; Put(B=AA CC ..) ; Put(C=DD ..) ; Commit
//   - ScanPrefix(AA) ; ScanPrefix(AA BB) ; ScanPrefix(DD)
//
// Validates: prefix matching with varying prefix lengths.
var SeedPrefixBehavior = []byte{
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
}

// -----------------------------------------------------------------------------
// Filter seeds (I-J).
//
// These seeds exercise filtered scan operations. Guard tests verify they
// emit at least one scan with Filter != nil.
// -----------------------------------------------------------------------------

// SeedFilteredScans exercises multiple scan types with different filters.
//
// Sequence:
//  1. BeginWrite -> Put(key1, rev=1) -> Put(key2, rev=2) -> Commit
//  2. Scan with FilterRevisionMask (even revisions)
//  3. Scan with FilterIndexByteEq
//  4. ScanPrefix with FilterAll
//
// Validates: filter function integration with scan operations.
var SeedFilteredScans = []byte{
	// --- Insert test data ---
	0x80, 0x06, // roulette=0x80 (>=26), choice=0x06 (<20) -> BeginWrite

	0x80, 0x00, // roulette=0x80, choice=0 -> Put
	0xFF,                                           // key mode: new valid key
	0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, // key1
	0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // revision=1 (odd)
	0xFF,                   // index mode: valid
	0x10, 0x00, 0x00, 0x00, // index (0x10 in first byte)

	0x80, 0x00, // Put
	0xFF,
	0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, // key2
	0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // revision=2 (even)
	0xFF,
	0x10, 0x01, 0x00, 0x00, // index (0x10 in first byte)

	0x80, 0x3C, // Commit (writer-active choice=60 -> Commit)

	// --- Scan with FilterRevisionMask(mask=1, want=0) -> even revisions ---
	0x80, 0x1E, // roulette=0x80, choice=0x1E (30) -> Scan
	0x02, // nextFilterSpec: 0x02 % 10 = 2 < 3 -> get filter
	0x02, // kind: 0x02 % 5 = 2 -> FilterRevisionMask
	0x00, // mask selector: 0x00 % 4 = 0 -> mask=1
	0x00, // want: 0x00 & 1 = 0 -> want=0 (even revisions)
	0x80, // scanOpts mode >= 26 -> valid
	0x00, // offset=0
	0x00, // limit=0
	0x00, // reverse=false

	// --- Scan with FilterIndexByteEq(offset=0, byte=0x10) ---
	0x80, 0x1E, // Scan
	0x01, // nextFilterSpec: 0x01 % 10 = 1 < 3 -> get filter
	0x03, // kind: 0x03 % 5 = 3 -> FilterIndexByteEq
	0x00, // offset: 0x00 % 4 = 0
	0x10, // byte: 0x10
	0x80, // scanOpts valid
	0x00, 0x00, 0x00,

	// --- ScanPrefix with FilterAll ---
	0x80, 0x32, // roulette=0x80, choice=0x32 (50) -> ScanPrefix
	0xFF, // prefix mode: valid (>=52)
	0x00, // select key index (no keys seen yet in this context, so random)
	0x00, // prefixLen byte
	0x00, // nextFilterSpec: 0x00 % 10 = 0 < 3 -> get filter
	0x00, // kind: 0x00 % 5 = 0 -> FilterAll
	0x80, // scanOpts valid
	0x00, 0x00, 0x00,
}

// SeedFilterPagination exercises filter combined with pagination.
//
// Sequence:
//  1. BeginWrite -> Put 4 entries with varying revisions -> Commit
//  2. Scan with FilterRevisionMask + Offset=1, Limit=1
//
// Validates: filter operates before pagination (offset/limit apply to filtered results).
var SeedFilterPagination = []byte{
	// --- Insert 4 entries ---
	0x80, 0x06, // BeginWrite (choice=0x06 < 20)

	0x80, 0x00, // Put key1, rev=1
	0xFF,
	0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28,
	0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0xFF, 0xA0, 0x00, 0x00, 0x00,

	0x80, 0x00, // Put key2, rev=2
	0xFF,
	0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38,
	0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0xFF, 0xB0, 0x00, 0x00, 0x00,

	0x80, 0x00, // Put key3, rev=3
	0xFF,
	0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48,
	0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0xFF, 0xC0, 0x00, 0x00, 0x00,

	0x80, 0x00, // Put key4, rev=4
	0xFF,
	0x51, 0x52, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58,
	0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0xFF, 0xD0, 0x00, 0x00, 0x00,

	0x80, 0x3C, // Commit (writer-active choice=60 -> Commit)

	// --- Scan with FilterRevisionMask(mask=1, want=0) + pagination ---
	// This filters to even revisions (2, 4), then applies offset=1, limit=1
	0x80, 0x1E, // Scan
	0x00, // nextFilterSpec: get filter
	0x02, // FilterRevisionMask
	0x00, // mask=1
	0x00, // want=0 (even)
	0x80, // scanOpts valid
	0x01, // offset=1 (skip first match)
	0x01, // limit=1
	0x00, // reverse=false
}

// -----------------------------------------------------------------------------
// Seed collections for test iteration.
// -----------------------------------------------------------------------------

// BehaviorSeed represents a named seed with its intended purpose.
type BehaviorSeed struct {
	Name        string // Short identifier (e.g., "BasicHappyPath")
	Description string // What behavior this seed validates
	Data        []byte // Raw seed bytes
}

// CoreBehaviorSeeds returns the core behavior seeds (A-H).
// These exercise fundamental API operations.
func CoreBehaviorSeeds() []BehaviorSeed {
	return []BehaviorSeed{
		{
			Name:        "BasicHappyPath",
			Description: "basic put/get, scan enumeration, prefix derivation",
			Data:        SeedBasicHappyPath,
		},
		{
			Name:        "UpdateExistingKey",
			Description: "revision updates, offset beyond matches returns empty",
			Data:        SeedUpdateExistingKey,
		},
		{
			Name:        "DeleteCommittedKey",
			Description: "delete marks key as missing, len reflects deletion",
			Data:        SeedDeleteCommittedKey,
		},
		{
			Name:        "CloseDiscardsBuffered",
			Description: "Writer.Close discards buffered ops without committing",
			Data:        SeedCloseDiscardsBuffered,
		},
		{
			Name:        "ErrBusyPaths",
			Description: "Close/Reopen return ErrBusy when writer is active",
			Data:        SeedErrBusyPaths,
		},
		{
			Name:        "InvalidInputs",
			Description: "proper error handling for nil/invalid inputs",
			Data:        SeedInvalidInputs,
		},
		{
			Name:        "MultiKeyPersistence",
			Description: "multi-key commit, persistence across reopen",
			Data:        SeedMultiKeyPersistence,
		},
		{
			Name:        "PrefixBehavior",
			Description: "prefix matching with varying prefix lengths",
			Data:        SeedPrefixBehavior,
		},
	}
}

// FilterSeeds returns seeds that exercise filtered scan operations.
// Guard tests verify these emit at least one scan with Filter != nil.
func FilterSeeds() []BehaviorSeed {
	return []BehaviorSeed{
		{
			Name:        "FilteredScans",
			Description: "filter function integration with scan operations",
			Data:        SeedFilteredScans,
		},
		{
			Name:        "FilterPagination",
			Description: "filter operates before pagination",
			Data:        SeedFilterPagination,
		},
	}
}

// AllBehaviorSeeds returns all curated behavior seeds.
func AllBehaviorSeeds() []BehaviorSeed {
	seeds := CoreBehaviorSeeds()
	seeds = append(seeds, FilterSeeds()...)

	return seeds
}
