package testutil

import "github.com/calvinalkan/agent-task/pkg/slotcache"

// This file contains all curated seed byte sequences used by slotcache fuzzing
// and deterministic tests.
//
// Seeds are grouped by test type:
//   - Unordered behavior seeds (model vs real)
//   - Ordered-keys behavior seeds (model vs real)
//
// IMPORTANT: Seeds are tied to specific KeySize/IndexSize values.
// The OpGenerator consumes different amounts of bytes based on these sizes
// (e.g., genKey reads KeySize bytes, genIndex reads IndexSize bytes).
// Using seeds with mismatched sizes causes byte consumption drift and
// produces different operation sequences than intended.
//
// All seeds in this file are built for:
//   - KeySize = 8 (seedKeySize)
//   - IndexSize = 4 (seedIndexSize)
//
// Tests using these seeds MUST use matching Options, or the curated
// operation sequences will not be exercised correctly.

// SeedOptions are the options that all seeds in this file are built for.
// Tests using these seeds MUST use these options (with Path set).
var SeedOptions = slotcache.Options{
	KeySize:      8,
	IndexSize:    4,
	SlotCapacity: 64,
}

// CurratedSeedOpGenConfig returns the frozen configuration for fuzz targets and
// seed guard tests.
//
// IMPORTANT: All fuzz-seeded behavior and spec tests MUST use this config to
// ensure curated seeds remain meaningful. Any change to this config's values
// will break seed guards and require explicit seed migration.
//
// The canonical config uses phased generation for better state coverage:
//   - Fill phase (0–60%): Heavy writes to populate the cache
//   - Churn phase (60–85%): Mix of puts/deletes for tombstone stress
//   - Read phase (85–100%): Heavy reads to validate final state
//
// AllowedOps defaults to BehaviorOpSet. Use SpecOpSet to include Invalidate.
func CurratedSeedOpGenConfig() OpGenConfig {
	return OpGenConfig{
		// Frozen probability rates — DO NOT CHANGE without updating seeds.
		InvalidKeyRate:               5,
		InvalidIndexRate:             5,
		InvalidScanOptsRate:          5,
		DeleteRate:                   15,
		CommitRate:                   15,
		WriterCloseRate:              5,
		NonMonotonicRate:             3,
		ReopenRate:                   3,
		CloseRate:                    3,
		BeginWriteRate:               20,
		InvalidateRate:               2,
		ReaderUserHeaderRate:         5,
		WriterReadRate:               15,
		WriterSetUserHeaderFlagsRate: 3,
		WriterSetUserHeaderDataRate:  2,
		SmallScanLimitBias:           true,
		KeyReuseMinThreshold:         4,
		KeyReuseMaxThreshold:         32,
		// Phased generation enabled for better coverage.
		PhasedEnabled:           true,
		FillPhaseEnd:            60,
		ChurnPhaseEnd:           85,
		FillPhaseBeginWriteRate: 50,
		FillPhaseCommitRate:     8,
		ChurnPhaseDeleteRate:    35,
		ReadPhaseBeginWriteRate: 5,
		// AllowedOps defaults to zero (BehaviorOpSet).
		AllowedOps: 0,
	}
}

// Seed represents a named seed with its intended purpose.
// Used by behavior fuzz tests.
type Seed struct {
	Name        string // Short identifier (e.g., "BasicHappyPath")
	Description string // What behavior this seed validates
	Data        []byte // Raw seed bytes
}

// -----------------------------------------------------------------------------
// Unordered behavior seeds
// -----------------------------------------------------------------------------

// SeedBasicHappyPath exercises the core write->read path.
//
// Sequence:
//   - BeginWrite -> Put(key1) -> Commit -> Get(key1) -> Scan -> ScanPrefix(derived)
//
// Validates: basic put/get, scan enumeration, prefix derivation.
var SeedBasicHappyPath = NewBehaviorSeedBuilder(SeedOptions.KeySize, SeedOptions.IndexSize).
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
var SeedUpdateExistingKey = NewBehaviorSeedBuilder(SeedOptions.KeySize, SeedOptions.IndexSize).
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
var SeedDeleteCommittedKey = NewBehaviorSeedBuilder(SeedOptions.KeySize, SeedOptions.IndexSize).
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
var SeedCloseDiscardsBuffered = NewBehaviorSeedBuilder(SeedOptions.KeySize, SeedOptions.IndexSize).
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
var SeedErrBusyPaths = NewBehaviorSeedBuilder(SeedOptions.KeySize, SeedOptions.IndexSize).
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
// Note: OpGenerator generates invalid inputs probabilistically; this seed
// is mostly a “smoke” seed for common paths.
var SeedInvalidInputs = NewBehaviorSeedBuilder(SeedOptions.KeySize, SeedOptions.IndexSize).
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
var SeedMultiKeyPersistence = NewBehaviorSeedBuilder(SeedOptions.KeySize, SeedOptions.IndexSize).
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
var SeedPrefixBehavior = NewBehaviorSeedBuilder(SeedOptions.KeySize, SeedOptions.IndexSize).
	BeginWrite().
	Put([]byte{0xAA, 0xBB, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01}, 1, []byte{0xA1, 0xA1, 0xA1, 0xA1}).
	Put([]byte{0xAA, 0xCC, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02}, 2, []byte{0xB2, 0xB2, 0xB2, 0xB2}).
	Put([]byte{0xDD, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x03}, 3, []byte{0xC3, 0xC3, 0xC3, 0xC3}).
	Commit().
	ScanPrefix([]byte{0xAA}).
	ScanPrefix([]byte{0xAA, 0xBB}).
	ScanPrefix([]byte{0xDD}).
	Build()

// SeedUserHeaderFlagsCommit exercises SetUserHeaderFlags with commit.
//
// Sequence:
//   - BeginWrite -> SetUserHeaderFlags -> Put -> Commit -> UserHeader
//
// Validates: header flags are published on commit.
var SeedUserHeaderFlagsCommit = NewBehaviorSeedBuilder(SeedOptions.KeySize, SeedOptions.IndexSize).
	BeginWrite().
	SetUserHeaderFlags(0x1234567890ABCDEF).
	Put([]byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22}, 1, []byte{0x01, 0x02, 0x03, 0x04}).
	Commit().
	UserHeader().
	Build()

// SeedUserHeaderDataDiscard exercises SetUserHeaderData with writer close (discard).
//
// Sequence:
//   - BeginWrite -> Put -> Commit -> BeginWrite -> SetUserHeaderData -> WriterClose -> UserHeader
//
// Validates: header data changes are discarded on Writer.Close without commit.
var SeedUserHeaderDataDiscard = NewBehaviorSeedBuilder(SeedOptions.KeySize, SeedOptions.IndexSize).
	BeginWrite().
	Put([]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}, 1, []byte{0xAA, 0xBB, 0xCC, 0xDD}).
	Commit().
	BeginWrite().
	SetUserHeaderData([64]byte{
		0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE, 0xBA, 0xBE,
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
		0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28,
		0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38,
		0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48,
		0x51, 0x52, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58,
		0x61, 0x62, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68,
	}).
	WriterClose().
	UserHeader().
	Build()

// SeedUserHeaderDataCommit exercises SetUserHeaderData with commit (persisted).
//
// Sequence:
//   - BeginWrite -> SetUserHeaderData -> Put -> Commit -> UserHeader
//
// Validates: header data is persisted on commit and readable via UserHeader.
var SeedUserHeaderDataCommit = NewBehaviorSeedBuilder(SeedOptions.KeySize, SeedOptions.IndexSize).
	BeginWrite().
	SetUserHeaderData([64]byte{
		't', 'e', 's', 't', '-', 'h', 'e', 'a',
		'd', 'e', 'r', '-', 'd', 'a', 't', 'a',
	}).
	Put([]byte{0x99, 0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22}, 1, []byte{0xF0, 0xF1, 0xF2, 0xF3}).
	Commit().
	UserHeader().
	Build()

// SeedUserHeaderBothCommit exercises both SetUserHeaderFlags and SetUserHeaderData.
//
// Sequence:
//   - BeginWrite -> SetUserHeaderFlags -> SetUserHeaderData -> Put -> Commit -> UserHeader
//
// Validates: both header fields are persisted and readable via UserHeader.
var SeedUserHeaderBothCommit = NewBehaviorSeedBuilder(SeedOptions.KeySize, SeedOptions.IndexSize).
	BeginWrite().
	SetUserHeaderFlags(0xCAFEBABE).
	SetUserHeaderData([64]byte{
		'b', 'o', 't', 'h', '-', 'f', 'l', 'a',
		'g', 's', '-', 'a', 'n', 'd', '-', 'd',
		'a', 't', 'a', '-', 't', 'e', 's', 't',
	}).
	Put([]byte{0xAB, 0xCD, 0xEF, 0x12, 0x34, 0x56, 0x78, 0x9A}, 1, []byte{0x11, 0x22, 0x33, 0x44}).
	Commit().
	UserHeader().
	Build()

// SeedFilteredScans exercises multiple scan types with different filters.
//
// Sequence:
//   - BeginWrite -> Put(rev=1) -> Put(rev=2) -> Commit
//   - Scan with filterRevisionMask
//   - Scan with filterIndexByteEq
//   - ScanPrefix
//
// Validates: filter function integration.
var SeedFilteredScans = NewBehaviorSeedBuilder(SeedOptions.KeySize, SeedOptions.IndexSize).
	BeginWrite().
	Put([]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}, 1, []byte{0x10, 0x00, 0x00, 0x00}).
	Put([]byte{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18}, 2, []byte{0x10, 0x01, 0x00, 0x00}).
	Commit().
	ScanWithFilter(filterRevisionMask, 0, 0, false).
	ScanWithFilter(filterIndexByteEq, 0, 0, false).
	ScanPrefix([]byte{0x01}).
	Build()

// SeedFilterPagination exercises filter combined with pagination.
//
// Sequence:
//   - BeginWrite -> Put 4 entries -> Commit
//   - Scan(filterRevisionMask, Offset=1, Limit=1)
//
// Validates: filter applies before pagination.
var SeedFilterPagination = NewBehaviorSeedBuilder(SeedOptions.KeySize, SeedOptions.IndexSize).
	BeginWrite().
	Put([]byte{0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28}, 1, []byte{0xA0, 0x00, 0x00, 0x00}).
	Put([]byte{0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38}, 2, []byte{0xB0, 0x00, 0x00, 0x00}).
	Put([]byte{0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48}, 3, []byte{0xC0, 0x00, 0x00, 0x00}).
	Put([]byte{0x51, 0x52, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58}, 4, []byte{0xD0, 0x00, 0x00, 0x00}).
	Commit().
	ScanWithFilter(filterRevisionMask, 1, 1, false).
	Build()

// AllSeeds returns all curated behavior seeds for unordered mode.
func AllSeeds() []Seed {
	return []Seed{
		{Name: "BasicHappyPath", Description: "basic put/get + scan/prefix", Data: SeedBasicHappyPath},
		{Name: "UpdateExistingKey", Description: "updates across sessions", Data: SeedUpdateExistingKey},
		{Name: "DeleteCommittedKey", Description: "delete then len", Data: SeedDeleteCommittedKey},
		{Name: "CloseDiscardsBuffered", Description: "Writer.Close discards", Data: SeedCloseDiscardsBuffered},
		{Name: "ErrBusyPaths", Description: "ErrBusy on Close/Reopen while writer active", Data: SeedErrBusyPaths},
		{Name: "InvalidInputs", Description: "smoke for basic paths", Data: SeedInvalidInputs},
		{Name: "MultiKeyPersistence", Description: "multi-key + reopen", Data: SeedMultiKeyPersistence},
		{Name: "PrefixBehavior", Description: "ScanPrefix with shared prefixes", Data: SeedPrefixBehavior},
		{Name: "FilteredScans", Description: "filter integration", Data: SeedFilteredScans},
		{Name: "FilterPagination", Description: "filter before pagination", Data: SeedFilterPagination},
		{Name: "UserHeaderFlagsCommit", Description: "flags publish on commit", Data: SeedUserHeaderFlagsCommit},
		{Name: "UserHeaderDataDiscard", Description: "data discarded on Writer.Close", Data: SeedUserHeaderDataDiscard},
		{Name: "UserHeaderDataCommit", Description: "data persisted on commit", Data: SeedUserHeaderDataCommit},
		{Name: "UserHeaderBothCommit", Description: "flags + data persisted on commit", Data: SeedUserHeaderBothCommit},
	}
}

// -----------------------------------------------------------------------------
// Ordered-keys behavior seeds
// -----------------------------------------------------------------------------

// SeedOrderedScanRangeAll exercises ScanRange in ordered-keys mode.
//
// Sequence:
//   - BeginWrite -> Put(monotonic) -> Put(monotonic) -> Commit -> ScanRange
var SeedOrderedScanRangeAll = NewOrderedSeedBuilder(SeedOptions.KeySize, SeedOptions.IndexSize).
	BeginWrite().
	PutMonotonic(1, []byte{0xAA, 0xBB, 0xCC, 0xDD}).
	PutMonotonic(1, []byte{0x01, 0x02, 0x03, 0x04}).
	Commit().
	ScanRangeAll().
	Build()

// SeedOrderedOutOfOrderInsert exercises ErrOutOfOrderInsert in ordered-keys mode.
var SeedOrderedOutOfOrderInsert = NewOrderedSeedBuilder(SeedOptions.KeySize, SeedOptions.IndexSize).
	BeginWrite().
	PutMonotonic(1, []byte{0x10, 0x10, 0x10, 0x10}).
	Commit().
	BeginWrite().
	PutMonotonic(1, []byte{0x20, 0x20, 0x20, 0x20}).
	Commit().
	BeginWrite().
	PutNonMonotonicNew(1, []byte{0x30, 0x30, 0x30, 0x30}).
	Commit().
	Build()

// SeedOrderedScanRangeBounded exercises ScanRange with explicit [start, end) bounds.
//
// Sequence:
//   - BeginWrite -> Put(k1) -> Put(k2) -> Put(k3) -> Commit
//   - ScanRange([k1, k3)) -> ScanRange([k2, maxKey))
//
// Validates: binary search finds correct start position, end bound excludes keys.
var SeedOrderedScanRangeBounded = NewOrderedSeedBuilder(SeedOptions.KeySize, SeedOptions.IndexSize).
	BeginWrite().
	PutMonotonic(1, []byte{0x01, 0x01, 0x01, 0x01}).
	PutMonotonic(2, []byte{0x02, 0x02, 0x02, 0x02}).
	PutMonotonic(3, []byte{0x03, 0x03, 0x03, 0x03}).
	Commit().
	ScanRangeBounded([]byte{0x00, 0x00, 0x00, 0x01}, []byte{0x00, 0x00, 0x00, 0x03}).
	ScanRangeBounded([]byte{0x00, 0x00, 0x00, 0x02}, []byte{0xFF, 0xFF, 0xFF, 0xFF}).
	Build()

// SeedOrderedScanRangeReverse exercises ScanRange with reverse iteration.
//
// Sequence:
//   - BeginWrite -> Put(k1) -> Put(k2) -> Put(k3) -> Commit -> ScanRange(reverse=true)
//
// Validates: reverse iteration returns entries in descending key order.
var SeedOrderedScanRangeReverse = NewOrderedSeedBuilder(SeedOptions.KeySize, SeedOptions.IndexSize).
	BeginWrite().
	PutMonotonic(1, []byte{0xAA, 0xAA, 0xAA, 0xAA}).
	PutMonotonic(2, []byte{0xBB, 0xBB, 0xBB, 0xBB}).
	PutMonotonic(3, []byte{0xCC, 0xCC, 0xCC, 0xCC}).
	Commit().
	ScanRangeReverse().
	Build()

// SeedOrderedScanPrefix exercises ScanPrefix in ordered mode (uses range scan optimization).
//
// Sequence:
//   - BeginWrite -> Put(0xAA...) -> Put(0xAB...) -> Put(0xBB...) -> Commit
//   - ScanPrefix(0xA)
//
// Validates: prefix scan uses binary search optimization in ordered mode.
var SeedOrderedScanPrefix = NewOrderedSeedBuilder(SeedOptions.KeySize, SeedOptions.IndexSize).
	BeginWrite().
	PutMonotonic(1, []byte{0x10, 0x10, 0x10, 0x10}).
	PutMonotonic(2, []byte{0x20, 0x20, 0x20, 0x20}).
	PutMonotonic(3, []byte{0x30, 0x30, 0x30, 0x30}).
	Commit().
	ScanPrefix([]byte{0x00, 0x00, 0x00, 0x01}).
	Build()

// SeedOrderedFilter exercises filter functions in ordered mode.
//
// Sequence:
//   - BeginWrite -> Put(rev=1) -> Put(rev=2) -> Commit
//   - Scan with filter -> ScanRange with filter
//
// Validates: filters work correctly with ordered-mode scan paths.
var SeedOrderedFilter = NewOrderedSeedBuilder(SeedOptions.KeySize, SeedOptions.IndexSize).
	BeginWrite().
	PutMonotonic(1, []byte{0x10, 0x00, 0x00, 0x00}).
	PutMonotonic(2, []byte{0x10, 0x01, 0x00, 0x00}).
	Commit().
	ScanWithFilter(filterRevisionMask).
	ScanRangeWithFilter(filterRevisionMask).
	Build()

// SeedOrderedDelete exercises delete (tombstone) in ordered mode.
//
// Sequence:
//   - BeginWrite -> Put(k1) -> Put(k2) -> Commit
//   - BeginWrite -> Delete(k1) -> Commit -> ScanRange
//
// Validates: tombstones are skipped in ordered-mode range scans.
var SeedOrderedDelete = NewOrderedSeedBuilder(SeedOptions.KeySize, SeedOptions.IndexSize).
	BeginWrite().
	PutMonotonic(1, []byte{0xDE, 0xAD, 0xBE, 0xEF}).
	PutMonotonic(2, []byte{0xCA, 0xFE, 0xBA, 0xBE}).
	Commit().
	BeginWrite().
	Delete().
	Commit().
	ScanRangeAll().
	Build()

// OrderedBehaviorSeeds returns all curated ordered-keys behavior seeds.
func OrderedBehaviorSeeds() []Seed {
	return []Seed{
		{Name: "OrderedScanRangeAll", Description: "ordered ScanRange is exercised", Data: SeedOrderedScanRangeAll},
		{Name: "OrderedOutOfOrderInsert", Description: "out-of-order inserts rejected", Data: SeedOrderedOutOfOrderInsert},
		{Name: "OrderedScanRangeBounded", Description: "ScanRange with [start, end) bounds", Data: SeedOrderedScanRangeBounded},
		{Name: "OrderedScanRangeReverse", Description: "ScanRange with reverse iteration", Data: SeedOrderedScanRangeReverse},
		{Name: "OrderedScanPrefix", Description: "ScanPrefix uses range optimization", Data: SeedOrderedScanPrefix},
		{Name: "OrderedFilter", Description: "filters in ordered mode", Data: SeedOrderedFilter},
		{Name: "OrderedDelete", Description: "delete/tombstone in ordered mode", Data: SeedOrderedDelete},
	}
}
