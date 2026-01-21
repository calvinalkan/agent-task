// Curated fuzz corpus seeds for behavior testing.
//
// These byte sequences are carefully constructed to exercise specific code
// paths in the OpGenerator. They are shared between fuzz tests (as seeds)
// and guard tests that verify they still emit the intended operations.
//
// If OpGenerator's byte consumption changes, these seeds may need updating.
// Guard tests will fail if seeds no longer emit their intended behavior.
//
// Each seed documents its intended purpose and expected operation sequence.
// The byte encoding follows the canonical OpGenerator protocol defined in
// opgen_config.go (CanonicalOpGenConfig + BehaviorOpSet).
//
// Seeds are constructed using BehaviorSeedBuilder which encapsulates the
// protocol details. This makes seeds more robust against minor changes
// in the OpGenerator implementation.

package testutil

// Default key/index sizes for seed construction.
const (
	seedKeySize   = 8
	seedIndexSize = 4
)

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
var SeedBasicHappyPath = NewBehaviorSeedBuilder(seedKeySize, seedIndexSize).
	BeginWrite().
	Put([]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}, 1, []byte{0xAA, 0xBB, 0xCC, 0xDD}).
	Commit().
	Get([]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}).
	Scan().
	ScanPrefix([]byte{0x01}).
	Build()

// SeedUpdateExistingKey exercises key updates across writer sessions.
//
// Sequence:
//   - BeginWrite -> Put(keyA) -> Commit -> BeginWrite -> Put(keyA) -> Commit -> Get(keyA) -> Scan
//
// Validates: revision updates, same key updated across sessions.
var SeedUpdateExistingKey = NewBehaviorSeedBuilder(seedKeySize, seedIndexSize).
	BeginWrite().
	Put([]byte{0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17}, 1, []byte{0x01, 0x02, 0x03, 0x04}).
	Commit().
	BeginWrite().
	Put([]byte{0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17}, 2, []byte{0x05, 0x06, 0x07, 0x08}).
	Commit().
	Get([]byte{0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17}).
	Scan().
	Build()

// SeedDeleteCommittedKey exercises the delete path.
//
// Sequence:
//   - BeginWrite -> Put(keyD) -> Commit -> BeginWrite -> Delete(keyD) -> Commit -> Get(keyD) -> Len
//
// Validates: delete marks key as missing, len reflects deletion.
var SeedDeleteCommittedKey = NewBehaviorSeedBuilder(seedKeySize, seedIndexSize).
	BeginWrite().
	Put([]byte{0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28}, 1, []byte{0xDE, 0xAD, 0xBE, 0xEF}).
	Commit().
	BeginWrite().
	Delete([]byte{0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28}).
	Commit().
	Get([]byte{0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28}).
	Len().
	Build()

// SeedCloseDiscardsBuffered exercises writer close discarding uncommitted ops.
//
// Sequence:
//   - BeginWrite -> Put(keyA) -> Commit -> BeginWrite -> Put(keyB) -> WriterClose -> Scan
//
// Validates: Writer.Close discards buffered ops without committing.
var SeedCloseDiscardsBuffered = NewBehaviorSeedBuilder(seedKeySize, seedIndexSize).
	BeginWrite().
	Put([]byte{0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38}, 1, []byte{0x01, 0x01, 0x01, 0x01}).
	Commit().
	BeginWrite().
	Put([]byte{0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48}, 2, []byte{0x02, 0x02, 0x02, 0x02}).
	WriterClose().
	Scan().
	Build()

// SeedErrBusyPaths exercises ErrBusy error paths.
//
// Sequence:
//   - BeginWrite -> Close (ErrBusy) -> Reopen (ErrBusy) -> WriterClose -> Close -> Reopen -> Len
//
// Validates: Close/Reopen return ErrBusy when writer is active.
var SeedErrBusyPaths = NewBehaviorSeedBuilder(seedKeySize, seedIndexSize).
	BeginWrite().
	Close().
	Reopen().
	WriterClose().
	Close().
	Reopen().
	Len().
	Build()

// SeedInvalidInputs exercises invalid input handling.
//
// Sequence:
//   - Get -> Scan -> ScanPrefix -> BeginWrite -> Put -> Commit
//
// Note: The OpGenerator generates some invalid inputs based on probability.
// This seed exercises basic paths; invalid inputs are covered by fuzz testing.
var SeedInvalidInputs = NewBehaviorSeedBuilder(seedKeySize, seedIndexSize).
	Get([]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}).
	Scan().
	ScanPrefix([]byte{0x00}).
	BeginWrite().
	Put([]byte{0x51, 0x52, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58}, 1, []byte{0x00, 0x00, 0x00, 0x00}).
	Commit().
	Build()

// SeedMultiKeyPersistence exercises multi-key commit and reopen persistence.
//
// Sequence:
//   - BeginWrite -> Put(A) -> Put(B) -> Commit -> Scan -> Reopen -> Scan -> Get(B)
//
// Validates: multi-key commit, persistence across reopen.
var SeedMultiKeyPersistence = NewBehaviorSeedBuilder(seedKeySize, seedIndexSize).
	BeginWrite().
	Put([]byte{0x61, 0x62, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68}, 1, []byte{0x0A, 0x0A, 0x0A, 0x0A}).
	Put([]byte{0x71, 0x72, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78}, 2, []byte{0x0B, 0x0B, 0x0B, 0x0B}).
	Commit().
	Scan().
	Reopen().
	Scan().
	Get([]byte{0x71, 0x72, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78}).
	Build()

// SeedPrefixBehavior exercises prefix scan with shared prefixes.
//
// Sequence:
//   - BeginWrite -> Put(A=AA BB ..) -> Put(B=AA CC ..) -> Put(C=DD ..) -> Commit
//   - ScanPrefix(AA) -> ScanPrefix(AA BB) -> ScanPrefix(DD)
//
// Validates: prefix matching with varying prefix lengths.
var SeedPrefixBehavior = NewBehaviorSeedBuilder(seedKeySize, seedIndexSize).
	BeginWrite().
	Put([]byte{0xAA, 0xBB, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01}, 1, []byte{0xA1, 0xA1, 0xA1, 0xA1}).
	Put([]byte{0xAA, 0xCC, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02}, 2, []byte{0xB2, 0xB2, 0xB2, 0xB2}).
	Put([]byte{0xDD, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x03}, 3, []byte{0xC3, 0xC3, 0xC3, 0xC3}).
	Commit().
	ScanPrefix([]byte{0xAA}).
	ScanPrefix([]byte{0xAA, 0xBB}).
	ScanPrefix([]byte{0xDD}).
	Build()

// -----------------------------------------------------------------------------
// Filter seeds (I-J).
//
// These seeds exercise filtered scan operations. Guard tests verify they
// emit at least one scan with Filter != nil.
// -----------------------------------------------------------------------------

// SeedFilteredScans exercises multiple scan types with different filters.
//
// Sequence:
//   - BeginWrite -> Put(key1, rev=1) -> Put(key2, rev=2) -> Commit
//   - Scan with FilterRevisionMask (even revisions)
//   - Scan with FilterIndexByteEq
//   - ScanPrefix with FilterAll
//
// Validates: filter function integration with scan operations.
var SeedFilteredScans = NewBehaviorSeedBuilder(seedKeySize, seedIndexSize).
	BeginWrite().
	Put([]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}, 1, []byte{0x10, 0x00, 0x00, 0x00}).
	Put([]byte{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18}, 2, []byte{0x10, 0x01, 0x00, 0x00}).
	Commit().
	ScanWithFilter(FilterRevisionMask, 0, 0, false).
	ScanWithFilter(FilterIndexByteEq, 0, 0, false).
	ScanPrefix([]byte{0x01}).
	Build()

// SeedFilterPagination exercises filter combined with pagination.
//
// Sequence:
//   - BeginWrite -> Put 4 entries with varying revisions -> Commit
//   - Scan with FilterRevisionMask + Offset=1, Limit=1
//
// Validates: filter operates before pagination (offset/limit apply to filtered results).
var SeedFilterPagination = NewBehaviorSeedBuilder(seedKeySize, seedIndexSize).
	BeginWrite().
	Put([]byte{0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28}, 1, []byte{0xA0, 0x00, 0x00, 0x00}).
	Put([]byte{0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38}, 2, []byte{0xB0, 0x00, 0x00, 0x00}).
	Put([]byte{0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48}, 3, []byte{0xC0, 0x00, 0x00, 0x00}).
	Put([]byte{0x51, 0x52, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58}, 4, []byte{0xD0, 0x00, 0x00, 0x00}).
	Commit().
	ScanWithFilter(FilterRevisionMask, 1, 1, false).
	Build()

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
			Description: "revision updates, same key updated across sessions",
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
			Description: "basic paths with some inputs; invalid inputs covered by fuzz",
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
