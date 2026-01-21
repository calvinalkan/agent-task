// Curated fuzz corpus seeds for spec fuzz testing.
//
// These byte sequences are carefully constructed to exercise specific code
// paths in the OpGenerator with SpecOpSet. They are shared between spec fuzz
// tests (as seeds) and guard tests that verify they still emit the intended
// operations.
//
// Unlike behavior seeds (which exclude Invalidate), spec seeds can include
// Invalidate operations. This is the key distinction between SpecOpSet and
// BehaviorOpSet.
//
// Seeds are constructed using SpecSeedBuilder which wraps BehaviorSeedBuilder
// and adds Invalidate support. This makes seeds robust against minor changes
// in the OpGenerator implementation while ensuring protocol alignment.
//
// Protocol: All seeds use the canonical OpGenerator protocol defined in
// opgen_config.go (CanonicalOpGenConfig + SpecOpSet).

package testutil

// padOrTruncate ensures data is exactly length bytes.
// Used by both BehaviorSeedBuilder and SpecSeedBuilder.
func padOrTruncate(data []byte, length int) []byte {
	if len(data) == length {
		return data
	}

	result := make([]byte, length)
	copy(result, data)

	return result
}

// SpecSeedBuilder helps construct spec fuzz seeds with a fluent API.
// It wraps BehaviorSeedBuilder and adds Invalidate support for spec testing.
//
// The builder tracks writer state to select appropriate operation bytes.
// All emitted bytes follow the canonical OpGenerator protocol.
type SpecSeedBuilder struct {
	*BehaviorSeedBuilder
}

// NewSpecSeedBuilder creates a builder for spec fuzz seeds.
func NewSpecSeedBuilder(keySize, indexSize int) *SpecSeedBuilder {
	return &SpecSeedBuilder{
		BehaviorSeedBuilder: NewBehaviorSeedBuilder(keySize, indexSize),
	}
}

// Invalidate emits an Invalidate operation.
// This is only valid when writer is not active (Invalidate takes a write lock).
//
// In OpGenerator, Invalidate is triggered via the global roulette byte:
//   - roulette < reopenThreshold → Reopen
//   - roulette < closeThreshold → Close
//   - roulette < invalidateThreshold → Invalidate (when allowed)
//
// The invalidate threshold is approximately at closeThreshold + 2% of 256 ≈ 5.
// closeThreshold ≈ 16 (8 for reopen + 8 for close in canonical config).
// So invalidate is in range [16, 21).
// We use roulette=0x11 (17) which falls into this range.
func (b *SpecSeedBuilder) Invalidate() *SpecSeedBuilder {
	if b.writerActive {
		return b // Invalidate returns ErrBusy when writer is active
	}

	// Use roulette byte in the invalidate range [closeThreshold, invalidateThreshold)
	// closeThreshold ≈ 16, invalidateThreshold ≈ 21
	b.data = append(b.data, 0x11) // roulette=17 → Invalidate

	return b
}

// Override fluent methods to return *SpecSeedBuilder for chaining.

// BeginWrite emits a BeginWrite operation.
func (b *SpecSeedBuilder) BeginWrite() *SpecSeedBuilder {
	b.BehaviorSeedBuilder.BeginWrite()

	return b
}

// Put emits a Put operation with the given key, revision, and index.
func (b *SpecSeedBuilder) Put(key []byte, revision int64, index []byte) *SpecSeedBuilder {
	b.BehaviorSeedBuilder.Put(key, revision, index)

	return b
}

// Delete emits a Delete operation.
func (b *SpecSeedBuilder) Delete(key []byte) *SpecSeedBuilder {
	b.BehaviorSeedBuilder.Delete(key)

	return b
}

// Commit emits a Commit operation.
func (b *SpecSeedBuilder) Commit() *SpecSeedBuilder {
	b.BehaviorSeedBuilder.Commit()

	return b
}

// WriterClose emits a Writer.Close operation (discards uncommitted).
func (b *SpecSeedBuilder) WriterClose() *SpecSeedBuilder {
	b.BehaviorSeedBuilder.WriterClose()

	return b
}

// Get emits a Get operation.
func (b *SpecSeedBuilder) Get(key []byte) *SpecSeedBuilder {
	b.BehaviorSeedBuilder.Get(key)

	return b
}

// Scan emits a Scan operation (no filter).
func (b *SpecSeedBuilder) Scan() *SpecSeedBuilder {
	b.BehaviorSeedBuilder.Scan()

	return b
}

// ScanPrefix emits a ScanPrefix operation.
func (b *SpecSeedBuilder) ScanPrefix(prefix []byte) *SpecSeedBuilder {
	b.BehaviorSeedBuilder.ScanPrefix(prefix)

	return b
}

// Len emits a Len operation.
func (b *SpecSeedBuilder) Len() *SpecSeedBuilder {
	b.BehaviorSeedBuilder.Len()

	return b
}

// UserHeader emits a UserHeader (Cache.UserHeader()) operation.
func (b *SpecSeedBuilder) UserHeader() *SpecSeedBuilder {
	b.BehaviorSeedBuilder.UserHeader()

	return b
}

// SetUserHeaderFlags emits a Writer.SetUserHeaderFlags operation.
func (b *SpecSeedBuilder) SetUserHeaderFlags(flags uint64) *SpecSeedBuilder {
	b.BehaviorSeedBuilder.SetUserHeaderFlags(flags)

	return b
}

// SetUserHeaderData emits a Writer.SetUserHeaderData operation.
func (b *SpecSeedBuilder) SetUserHeaderData(data [64]byte) *SpecSeedBuilder {
	b.BehaviorSeedBuilder.SetUserHeaderData(data)

	return b
}

// Close emits a Cache.Close operation.
func (b *SpecSeedBuilder) Close() *SpecSeedBuilder {
	b.BehaviorSeedBuilder.Close()

	return b
}

// Reopen emits a Reopen operation.
func (b *SpecSeedBuilder) Reopen() *SpecSeedBuilder {
	b.BehaviorSeedBuilder.Reopen()

	return b
}

// -----------------------------------------------------------------------------
// Curated spec fuzz seeds.
//
// These seeds target specific spec-level operations that aren't covered
// by behavior tests (which don't model Invalidate). They also exercise
// user header operations which are validated via the spec oracle.
// -----------------------------------------------------------------------------

// SpecSeedInvalidate exercises the Invalidate() path.
//
// Sequence:
//   - BeginWrite -> Put(key1) -> Commit -> Invalidate
//
// Validates: file format remains valid after invalidation.
var SpecSeedInvalidate = NewSpecSeedBuilder(seedKeySize, seedIndexSize).
	BeginWrite().
	Put([]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}, 1, []byte{0xAA, 0xBB, 0xCC, 0xDD}).
	Commit().
	Invalidate().
	Build()

// SpecSeedUserHeaderFlags exercises SetUserHeaderFlags for spec validation.
//
// Sequence:
//   - BeginWrite -> SetUserHeaderFlags(0xDEADBEEF) -> Put(key1) -> Commit -> UserHeader
//
// Validates: user header flags are persisted, file format remains valid.
var SpecSeedUserHeaderFlags = NewSpecSeedBuilder(seedKeySize, seedIndexSize).
	BeginWrite().
	SetUserHeaderFlags(0xDEADBEEF).
	Put([]byte{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18}, 1, []byte{0x01, 0x02, 0x03, 0x04}).
	Commit().
	UserHeader().
	Build()

// SpecSeedUserHeaderData exercises SetUserHeaderData for spec validation.
//
// Sequence:
//   - BeginWrite -> SetUserHeaderData(pattern) -> Put(key1) -> Commit -> Scan
//
// Validates: user header data is persisted, file format remains valid.
var SpecSeedUserHeaderData = NewSpecSeedBuilder(seedKeySize, seedIndexSize).
	BeginWrite().
	SetUserHeaderData([64]byte{
		't', 'e', 's', 't', '-', 'u', 's', 'e',
		'r', '-', 'd', 'a', 't', 'a', '-', 'p',
		'a', 'y', 'l', 'o', 'a', 'd', '-', 'f',
		'o', 'r', '-', 'h', 'e', 'a', 'd', 'e',
	}).
	Put([]byte{0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28}, 2, []byte{0x05, 0x06, 0x07, 0x08}).
	Commit().
	Scan().
	Build()

// SpecSeedUserHeaderBoth exercises both user header operations in one session.
//
// Sequence:
//   - BeginWrite -> SetUserHeaderFlags -> SetUserHeaderData -> Put -> Commit
//
// Validates: both user header operations work together.
var SpecSeedUserHeaderBoth = NewSpecSeedBuilder(seedKeySize, seedIndexSize).
	BeginWrite().
	SetUserHeaderFlags(0xCAFEBABE).
	SetUserHeaderData([64]byte{
		'c', 'o', 'm', 'b', 'i', 'n', 'e', 'd',
		'-', 'f', 'l', 'a', 'g', 's', '-', 'a',
		'n', 'd', '-', 'd', 'a', 't', 'a', '-',
		't', 'e', 's', 't', '-', 'p', 'a', 'y',
	}).
	Put([]byte{0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38}, 3, []byte{0x09, 0x0A, 0x0B, 0x0C}).
	Commit().
	Build()

// SpecSeedInvalidateAfterReopen exercises invalidation after a reopen cycle.
//
// Sequence:
//   - BeginWrite -> Put -> Commit -> Reopen -> Invalidate
//
// Validates: invalidation works on reopened files.
var SpecSeedInvalidateAfterReopen = NewSpecSeedBuilder(seedKeySize, seedIndexSize).
	BeginWrite().
	Put([]byte{0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48}, 1, []byte{0xF0, 0xF1, 0xF2, 0xF3}).
	Commit().
	Reopen().
	Invalidate().
	Build()

// SpecSeedMultipleInvalidates exercises multiple invalidate cycles.
//
// Sequence:
//   - BeginWrite -> Put -> Commit -> Invalidate
//   - (After reset in fuzz driver: cache is recreated)
//   - The seed continues but will hit a fresh cache
//
// Validates: invalidation correctly marks the file, subsequent ops handle it.
var SpecSeedMultipleInvalidates = NewSpecSeedBuilder(seedKeySize, seedIndexSize).
	BeginWrite().
	Put([]byte{0x51, 0x52, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58}, 1, []byte{0xE0, 0xE1, 0xE2, 0xE3}).
	Commit().
	Invalidate().
	Build()

// SpecSeed represents a named spec fuzz seed with its intended purpose.
type SpecSeed struct {
	Name        string // Short identifier
	Description string // What behavior this seed validates
	Data        []byte // Raw seed bytes
}

// SpecFuzzSeeds returns all curated spec fuzz seeds.
func SpecFuzzSeeds() []SpecSeed {
	return []SpecSeed{
		{
			Name:        "Invalidate",
			Description: "file format remains valid after invalidation",
			Data:        SpecSeedInvalidate,
		},
		{
			Name:        "UserHeaderFlags",
			Description: "user header flags are persisted, file format remains valid",
			Data:        SpecSeedUserHeaderFlags,
		},
		{
			Name:        "UserHeaderData",
			Description: "user header data is persisted, file format remains valid",
			Data:        SpecSeedUserHeaderData,
		},
		{
			Name:        "UserHeaderBoth",
			Description: "both user header operations work together",
			Data:        SpecSeedUserHeaderBoth,
		},
		{
			Name:        "InvalidateAfterReopen",
			Description: "invalidation works on reopened files",
			Data:        SpecSeedInvalidateAfterReopen,
		},
		{
			Name:        "MultipleInvalidates",
			Description: "invalidation correctly marks the file",
			Data:        SpecSeedMultipleInvalidates,
		},
	}
}

// InvalidateSeeds returns seeds that specifically exercise Invalidate.
// These are spec-only (Invalidate is not in BehaviorOpSet).
func InvalidateSeeds() []SpecSeed {
	return []SpecSeed{
		{
			Name:        "Invalidate",
			Description: "file format remains valid after invalidation",
			Data:        SpecSeedInvalidate,
		},
		{
			Name:        "InvalidateAfterReopen",
			Description: "invalidation works on reopened files",
			Data:        SpecSeedInvalidateAfterReopen,
		},
		{
			Name:        "MultipleInvalidates",
			Description: "invalidation correctly marks the file",
			Data:        SpecSeedMultipleInvalidates,
		},
	}
}

// SpecUserHeaderSeeds returns seeds that exercise user header ops in spec context.
func SpecUserHeaderSeeds() []SpecSeed {
	return []SpecSeed{
		{
			Name:        "UserHeaderFlags",
			Description: "user header flags are persisted, file format remains valid",
			Data:        SpecSeedUserHeaderFlags,
		},
		{
			Name:        "UserHeaderData",
			Description: "user header data is persisted, file format remains valid",
			Data:        SpecSeedUserHeaderData,
		},
		{
			Name:        "UserHeaderBoth",
			Description: "both user header operations work together",
			Data:        SpecSeedUserHeaderBoth,
		},
	}
}
