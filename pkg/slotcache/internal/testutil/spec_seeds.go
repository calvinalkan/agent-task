// Curated fuzz corpus seeds for spec fuzz testing.
//
// These byte sequences are constructed to exercise specific code paths in
// spec_fuzz_test.go. They use a different protocol than behavior seeds because
// the spec fuzz test uses a custom actionByte dispatch rather than OpGenerator.
//
// Protocol reference (from spec_fuzz_test.go):
//
//	actionByte % 100:
//	  - [0, 3): close/reopen cycle
//
//	When writer is NOT active:
//	  - [0, 23):  BeginWrite (pick 10)
//	  - [23, 26): Invalidate (pick 24)
//	  - [26, 35): Len (pick 30)
//	  - [35, 45): Get (pick 40)
//	  - [45, 55): Scan (pick 50)
//	  - [55, 100): ScanPrefix (pick 60)
//
//	When writer IS active:
//	  - [0, 46):  Put (pick 10)
//	  - [46, 61): Delete (pick 50)
//	  - [61, 75): Commit (pick 65)
//	  - [75, 83): Writer.Close/abort (pick 78)
//	  - [83, 87): SetUserHeaderFlags (pick 84)
//	  - [87, 91): SetUserHeaderData (pick 88)
//	  - [91, 100): Len (pick 95)

package testutil

import (
	"encoding/binary"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

// SpecSeedBuilder helps construct spec fuzz seeds with a fluent API.
// It encapsulates the spec_fuzz_test.go protocol to reduce brittleness.
type SpecSeedBuilder struct {
	data         []byte
	keySize      int
	indexSize    int
	writerActive bool
}

// NewSpecSeedBuilder creates a builder for spec fuzz seeds.
func NewSpecSeedBuilder(keySize, indexSize int) *SpecSeedBuilder {
	return &SpecSeedBuilder{
		data:      make([]byte, 0, 256),
		keySize:   keySize,
		indexSize: indexSize,
	}
}

// Build returns the constructed seed bytes.
func (b *SpecSeedBuilder) Build() []byte {
	return append([]byte(nil), b.data...)
}

// BeginWrite emits a BeginWrite action.
func (b *SpecSeedBuilder) BeginWrite() *SpecSeedBuilder {
	// actionByte % 100 must be in [3, 23) for BeginWrite when no writer active
	// Use 10 (not in reopen range [0,3))
	b.data = append(b.data, 10)
	b.writerActive = true

	return b
}

// Put emits a Put action with the given key, revision, and index.
func (b *SpecSeedBuilder) Put(key []byte, revision int64, index []byte) *SpecSeedBuilder {
	if !b.writerActive {
		return b
	}
	// actionByte % 100 must be in [3, 46) for Put when writer active
	// Use 10
	b.data = append(b.data, 10)

	// FuzzDecoder key generation: mode >= 38 for valid key (0xFF = valid new key)
	keyData := padOrTruncate(key, b.keySize)
	b.data = append(b.data, append([]byte{0xFF}, keyData...)...)

	// Revision as int64 LE (use PutUint64 with bit cast to preserve sign bits)
	var revBytes [8]byte
	putInt64LE(revBytes[:], revision)
	b.data = append(b.data, revBytes[:]...)

	// FuzzDecoder index generation: mode >= 26 for valid index (0xFF = valid)
	indexData := padOrTruncate(index, b.indexSize)
	b.data = append(b.data, append([]byte{0xFF}, indexData...)...)

	return b
}

// Commit emits a Commit action.
func (b *SpecSeedBuilder) Commit() *SpecSeedBuilder {
	if !b.writerActive {
		return b
	}
	// actionByte % 100 must be in [61, 75) for Commit when writer active
	// Use 65
	b.data = append(b.data, 65)
	b.writerActive = false

	return b
}

// WriterClose emits a Writer.Close (abort) action.
func (b *SpecSeedBuilder) WriterClose() *SpecSeedBuilder {
	if !b.writerActive {
		return b
	}
	// actionByte % 100 must be in [75, 83) for Writer.Close when writer active
	// Use 78
	b.data = append(b.data, 78)
	b.writerActive = false

	return b
}

// Invalidate emits an Invalidate action.
func (b *SpecSeedBuilder) Invalidate() *SpecSeedBuilder {
	if b.writerActive {
		return b
	}
	// actionByte % 100 must be in [23, 26) for Invalidate when no writer active
	// Use 24
	b.data = append(b.data, 24)

	return b
}

// SetUserHeaderFlags emits a SetUserHeaderFlags action with the given flags.
func (b *SpecSeedBuilder) SetUserHeaderFlags(flags uint64) *SpecSeedBuilder {
	if !b.writerActive {
		return b
	}
	// actionByte % 100 must be in [83, 87) for SetUserHeaderFlags when writer active
	// Use 84
	b.data = append(b.data, 84)

	// flags as uint64 LE
	var flagBytes [8]byte
	binary.LittleEndian.PutUint64(flagBytes[:], flags)
	b.data = append(b.data, flagBytes[:]...)

	return b
}

// SetUserHeaderData emits a SetUserHeaderData action with the given data.
func (b *SpecSeedBuilder) SetUserHeaderData(data []byte) *SpecSeedBuilder {
	if !b.writerActive {
		return b
	}
	// actionByte % 100 must be in [87, 91) for SetUserHeaderData when writer active
	// Use 88
	b.data = append(b.data, 88)

	b.data = append(b.data, padOrTruncate(data, slotcache.UserDataSize)...)

	return b
}

// Len emits a Len action.
func (b *SpecSeedBuilder) Len() *SpecSeedBuilder {
	if b.writerActive {
		// actionByte % 100 must be in [91, 100) for Len when writer active
		// Use 95
		b.data = append(b.data, 95)
	} else {
		// actionByte % 100 must be in [26, 35) for Len when no writer active
		// Use 30
		b.data = append(b.data, 30)
	}

	return b
}

// Scan emits a Scan action.
func (b *SpecSeedBuilder) Scan() *SpecSeedBuilder {
	if b.writerActive {
		return b
	}
	// actionByte % 100 must be in [45, 55) for Scan when no writer active
	// Use 50
	b.data = append(b.data, 50)

	return b
}

// CloseReopen emits a close/reopen cycle (actionByte in [0, 3)).
func (b *SpecSeedBuilder) CloseReopen() *SpecSeedBuilder {
	if b.writerActive {
		// Skip if writer active; real fuzz handles ErrBusy
		return b
	}
	// actionByte % 100 in [0, 3) triggers close/reopen
	// But we need actionByte % 100 < 3, so use 0, 1, or 2
	b.data = append(b.data, 0)

	return b
}

// padOrTruncate ensures data is exactly length bytes.
func padOrTruncate(data []byte, length int) []byte {
	if len(data) == length {
		return data
	}

	result := make([]byte, length)
	copy(result, data)

	return result
}

// putInt64LE writes an int64 in little-endian byte order.
// This avoids the gosec G115 warning about int64->uint64 conversion.
func putInt64LE(buf []byte, v int64) {
	_ = buf[7] // bounds check hint
	buf[0] = byte(v)
	buf[1] = byte(v >> 8)
	buf[2] = byte(v >> 16)
	buf[3] = byte(v >> 24)
	buf[4] = byte(v >> 32)
	buf[5] = byte(v >> 40)
	buf[6] = byte(v >> 48)
	buf[7] = byte(v >> 56)
}

// -----------------------------------------------------------------------------
// Curated spec fuzz seeds.
//
// These seeds target specific spec-level operations that aren't covered
// by behavior tests (which don't model Invalidate or user header ops).
// -----------------------------------------------------------------------------

// SpecSeedInvalidate exercises the Invalidate() path.
//
// Sequence:
//   - BeginWrite -> Put(key1) -> Commit -> Invalidate
//
// Validates: file format remains valid after invalidation.
var SpecSeedInvalidate = NewSpecSeedBuilder(8, 4).
	BeginWrite().
	Put([]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}, 1, []byte{0xAA, 0xBB, 0xCC, 0xDD}).
	Commit().
	Invalidate().
	Build()

// SpecSeedUserHeaderFlags exercises SetUserHeaderFlags.
//
// Sequence:
//   - BeginWrite -> SetUserHeaderFlags(0xDEADBEEF) -> Put(key1) -> Commit -> Len
//
// Validates: user header flags are persisted, file format remains valid.
var SpecSeedUserHeaderFlags = NewSpecSeedBuilder(8, 4).
	BeginWrite().
	SetUserHeaderFlags(0xDEADBEEF).
	Put([]byte{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18}, 1, []byte{0x01, 0x02, 0x03, 0x04}).
	Commit().
	Len().
	Build()

// SpecSeedUserHeaderData exercises SetUserHeaderData.
//
// Sequence:
//   - BeginWrite -> SetUserHeaderData(pattern) -> Put(key1) -> Commit -> Scan
//
// Validates: user header data is persisted, file format remains valid.
var SpecSeedUserHeaderData = NewSpecSeedBuilder(8, 4).
	BeginWrite().
	SetUserHeaderData([]byte("test-user-data-payload-for-header")).
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
var SpecSeedUserHeaderBoth = NewSpecSeedBuilder(8, 4).
	BeginWrite().
	SetUserHeaderFlags(0xCAFEBABE).
	SetUserHeaderData([]byte("combined-flags-and-data-test")).
	Put([]byte{0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38}, 3, []byte{0x09, 0x0A, 0x0B, 0x0C}).
	Commit().
	Build()

// SpecSeedInvalidateAfterReopen exercises invalidation after a reopen cycle.
//
// Sequence:
//   - BeginWrite -> Put -> Commit -> CloseReopen -> Invalidate
//
// Validates: invalidation works on reopened files.
var SpecSeedInvalidateAfterReopen = NewSpecSeedBuilder(8, 4).
	BeginWrite().
	Put([]byte{0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48}, 1, []byte{0xF0, 0xF1, 0xF2, 0xF3}).
	Commit().
	CloseReopen().
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
	}
}
