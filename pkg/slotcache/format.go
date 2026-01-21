package slotcache

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"
)

// isLittleEndian is true if the CPU uses little-endian byte order.
// Computed once at package init time.
var isLittleEndian = func() bool {
	var x uint32 = 0x04030201

	return *(*byte)(unsafe.Pointer(&x)) == 0x01
}()

// is64Bit is true if the architecture has 64-bit pointers.
// Required for atomic 64-bit operations across processes.
var is64Bit = unsafe.Sizeof(uintptr(0)) >= 8

// SLC1 file format constants.
const (
	// File format version.
	slc1Version = 1

	// Fixed header size in bytes.
	slc1HeaderSize = 256

	// Hash algorithm identifier (FNV-1a 64-bit).
	slc1HashAlgFNV1a64 = 1

	// Header flags.
	slc1FlagOrderedKeys uint32 = 1 << 0
)

// FNV-1a 64-bit hash constants.
const (
	fnv1aOffsetBasis uint64 = 14695981039346656037
	fnv1aPrime       uint64 = 1099511628211
)

// Safe integer conversion constants.
const (
	maxInt    = int(^uint(0) >> 1)
	maxInt64  = int64(^uint64(0) >> 1)
	maxUint32 = ^uint32(0)
)

// intToUint32Checked converts a non-negative int to uint32.
// Returns ErrInvalidInput if the value is negative or exceeds uint32 max.
//
// Callers should validate inputs upfront via Open() and reject with ErrInvalidInput.
// This function exists to avoid unsafe silent truncation.
func intToUint32Checked(v int) (uint32, error) {
	if v < 0 {
		return 0, fmt.Errorf("int %d is negative, cannot convert to uint32: %w", v, ErrInvalidInput)
	}

	// Convert through uint64 to avoid gosec G115 warning.
	u64 := uint64(v)

	if u64 > uint64(maxUint32) {
		return 0, fmt.Errorf("int %d exceeds uint32 max: %w", v, ErrInvalidInput)
	}

	return uint32(u64), nil
}

// uint64ToInt64Checked converts uint64 to int64.
// Returns ErrInvalidInput if the value exceeds maxInt64.
//
// Used for file sizes and offsets. Callers should validate configurations upfront
// to ensure computed sizes fit in int64.
func uint64ToInt64Checked(v uint64) (int64, error) {
	if v > uint64(maxInt64) {
		return 0, fmt.Errorf("uint64 %d exceeds int64 max: %w", v, ErrInvalidInput)
	}

	return int64(v), nil
}

// uint64ToIntChecked converts uint64 to int.
// Returns ErrInvalidInput if the value exceeds maxInt.
func uint64ToIntChecked(v uint64) (int, error) {
	if v > uint64(maxInt) {
		return 0, fmt.Errorf("uint64 %d exceeds int max: %w", v, ErrInvalidInput)
	}

	return int(v), nil
}

// fnv1a64 computes the FNV-1a 64-bit hash over key bytes.
func fnv1a64(key []byte) uint64 {
	hash := fnv1aOffsetBasis
	for _, b := range key {
		hash ^= uint64(b)
		hash *= fnv1aPrime
	}

	return hash
}

// Header field offsets (bytes from file start).
const (
	offMagic             = 0x000 // [4]byte
	offVersion           = 0x004 // uint32
	offHeaderSize        = 0x008 // uint32
	offKeySize           = 0x00C // uint32
	offIndexSize         = 0x010 // uint32
	offSlotSize          = 0x014 // uint32
	offHashAlg           = 0x018 // uint32
	offFlags             = 0x01C // uint32
	offSlotCapacity      = 0x020 // uint64
	offSlotHighwater     = 0x028 // uint64
	offLiveCount         = 0x030 // uint64
	offUserVersion       = 0x038 // uint64
	offGeneration        = 0x040 // uint64
	offBucketCount       = 0x048 // uint64
	offBucketUsed        = 0x050 // uint64
	offBucketTombstones  = 0x058 // uint64
	offSlotsOffset       = 0x060 // uint64
	offBucketsOffset     = 0x068 // uint64
	offHeaderCRC32C      = 0x070 // uint32
	offState             = 0x074 // uint32 (slotcache-owned state)
	offUserFlags         = 0x078 // uint64 (caller-owned)
	offUserData          = 0x080 // [64]byte (caller-owned)
	offReservedTailStart = 0x0C0 // reserved bytes through 0x0FF (64 bytes)
)

// Cache state values (stored in the state field at offset 0x074).
const (
	// stateNormal indicates the cache is operational.
	stateNormal uint32 = 0
	// stateInvalidated indicates the cache has been explicitly invalidated (terminal).
	stateInvalidated uint32 = 1
)

// slc1Header represents the 256-byte SLC1 file header.
type slc1Header struct {
	Magic            [4]byte
	Version          uint32
	HeaderSize       uint32
	KeySize          uint32
	IndexSize        uint32
	SlotSize         uint32
	HashAlg          uint32
	Flags            uint32
	SlotCapacity     uint64
	SlotHighwater    uint64
	LiveCount        uint64
	UserVersion      uint64
	Generation       uint64
	BucketCount      uint64
	BucketUsed       uint64
	BucketTombstones uint64
	SlotsOffset      uint64
	BucketsOffset    uint64
	HeaderCRC32C     uint32
	State            uint32             // slotcache-owned state (0=normal, 1=invalidated)
	UserFlags        uint64             // caller-owned opaque flags
	UserData         [UserDataSize]byte // caller-owned opaque data (64 bytes)
	// Reserved bytes from 0x0C0 to 0x0FF (64 bytes) MUST be zero.
}

// encodeHeader serializes the header to a 256-byte slice.
// The CRC is computed and stored in the output.
func encodeHeader(header *slc1Header) []byte {
	buf := make([]byte, slc1HeaderSize)

	// Magic and fixed fields.
	copy(buf[offMagic:], header.Magic[:])
	binary.LittleEndian.PutUint32(buf[offVersion:], header.Version)
	binary.LittleEndian.PutUint32(buf[offHeaderSize:], header.HeaderSize)
	binary.LittleEndian.PutUint32(buf[offKeySize:], header.KeySize)
	binary.LittleEndian.PutUint32(buf[offIndexSize:], header.IndexSize)
	binary.LittleEndian.PutUint32(buf[offSlotSize:], header.SlotSize)
	binary.LittleEndian.PutUint32(buf[offHashAlg:], header.HashAlg)
	binary.LittleEndian.PutUint32(buf[offFlags:], header.Flags)

	// 64-bit fields.
	binary.LittleEndian.PutUint64(buf[offSlotCapacity:], header.SlotCapacity)
	binary.LittleEndian.PutUint64(buf[offSlotHighwater:], header.SlotHighwater)
	binary.LittleEndian.PutUint64(buf[offLiveCount:], header.LiveCount)
	binary.LittleEndian.PutUint64(buf[offUserVersion:], header.UserVersion)
	binary.LittleEndian.PutUint64(buf[offGeneration:], header.Generation)
	binary.LittleEndian.PutUint64(buf[offBucketCount:], header.BucketCount)
	binary.LittleEndian.PutUint64(buf[offBucketUsed:], header.BucketUsed)
	binary.LittleEndian.PutUint64(buf[offBucketTombstones:], header.BucketTombstones)
	binary.LittleEndian.PutUint64(buf[offSlotsOffset:], header.SlotsOffset)
	binary.LittleEndian.PutUint64(buf[offBucketsOffset:], header.BucketsOffset)

	// State field.
	binary.LittleEndian.PutUint32(buf[offState:], header.State)

	// User header fields (caller-owned).
	binary.LittleEndian.PutUint64(buf[offUserFlags:], header.UserFlags)
	copy(buf[offUserData:offUserData+UserDataSize], header.UserData[:])

	// Compute CRC with generation and crc fields zeroed.
	crc := computeHeaderCRC(buf)
	binary.LittleEndian.PutUint32(buf[offHeaderCRC32C:], crc)

	return buf
}

// computeHeaderCRC calculates the CRC32-C checksum of the header buffer
// with the generation and crc fields treated as zero.
func computeHeaderCRC(buf []byte) uint32 {
	// Make a copy to zero the excluded fields.
	tmp := make([]byte, slc1HeaderSize)
	copy(tmp, buf)

	// Zero generation field (8 bytes at offset 0x040).
	for i := offGeneration; i < offGeneration+8; i++ {
		tmp[i] = 0
	}

	// Zero crc field (4 bytes at offset 0x070).
	for i := offHeaderCRC32C; i < offHeaderCRC32C+4; i++ {
		tmp[i] = 0
	}

	return crc32.Checksum(tmp, crc32.MakeTable(crc32.Castagnoli))
}

// validateHeaderCRC checks if the stored CRC matches the computed CRC.
func validateHeaderCRC(buf []byte) bool {
	storedCRC := binary.LittleEndian.Uint32(buf[offHeaderCRC32C:])
	computedCRC := computeHeaderCRC(buf)

	return storedCRC == computedCRC
}

// hasReservedBytesSet checks if any reserved tail bytes (0x0C0-0x0FF) are non-zero.
// Note: User header bytes (0x078-0x0BF) are caller-owned and NOT checked here.
func hasReservedBytesSet(buf []byte) bool {
	for i := offReservedTailStart; i < slc1HeaderSize; i++ {
		if buf[i] != 0 {
			return true
		}
	}

	return false
}

// computeSlotSize calculates the slot size per spec and enforces
// implementation limits.
//
// slot_size = align8( meta(8) + key_size + key_pad + revision(8) + index_size )
// where key_pad = (8 - (key_size % 8)) % 8.
//
// Returns ErrInvalidInput if the derived size cannot be represented safely.
func computeSlotSize(keySize, indexSize uint32) (uint32, error) {
	keyPad := (8 - (keySize % 8)) % 8

	// Compute in uint64 to avoid uint32 wraparound.
	unaligned := uint64(8) + uint64(keySize) + uint64(keyPad) + uint64(8) + uint64(indexSize)
	aligned := align8U64(unaligned)

	if aligned > uint64(maxUint32) {
		return 0, fmt.Errorf("computed slot_size %d exceeds uint32 max: %w", aligned, ErrInvalidInput)
	}

	if aligned > uint64(maxSlotSizeBytes) {
		return 0, fmt.Errorf("computed slot_size %d exceeds max slot size %d: %w", aligned, maxSlotSizeBytes, ErrInvalidInput)
	}

	return uint32(aligned), nil
}

// align8U64 rounds x up to the next multiple of 8.
func align8U64(x uint64) uint64 {
	return (x + 7) &^ 7
}

// computeBucketCount calculates the bucket count for a given slot capacity
// and enforces implementation limits.
//
// bucket_count = nextPow2(slot_capacity * 2)
// bucket_count must be >= 2 and a power of two.
//
// Per spec: "Implementations SHOULD size bucket_count = nextPowerOfTwo(slot_capacity * 2)
// to maintain load factor ≤ 0.5."
//
// Returns ErrInvalidInput if slot_capacity * 2 would overflow.
func computeBucketCount(slotCapacity uint64) (uint64, error) {
	if slotCapacity == 0 {
		return 2, nil // minimum valid bucket count
	}

	// Check for overflow: slot_capacity * 2 > maxUint64
	// Equivalent to: slot_capacity > maxUint64 / 2
	const maxSafeCapacity = ^uint64(0) >> 1 // maxUint64 / 2
	if slotCapacity > maxSafeCapacity {
		return 0, fmt.Errorf("slot_capacity %d causes bucket_count overflow: %w", slotCapacity, ErrInvalidInput)
	}

	needed := max(slotCapacity*2, 2)

	return nextPow2(needed), nil
}

// nextPow2 returns the smallest power of two >= value.
func nextPow2(value uint64) uint64 {
	if value == 0 {
		return 1
	}

	value--
	value |= value >> 1
	value |= value >> 2
	value |= value >> 4
	value |= value >> 8
	value |= value >> 16
	value |= value >> 32

	return value + 1
}

// Slot meta bit flags.
const (
	// slotMetaUsed indicates a live (non-deleted) slot.
	slotMetaUsed uint64 = 1 << 0

	// slotMetaReservedMask is the mask for reserved bits in slot meta.
	// Per spec (002-format.md): "All other bits are reserved and MUST be zero in v1."
	// If (meta & slotMetaReservedMask) != 0 under a stable even generation, it's corruption.
	slotMetaReservedMask = ^slotMetaUsed
)

// encodeSlot serializes a slot record to a fixed-size byte slice.
// The slot layout per spec:
//   - meta (8 bytes, uint64)
//   - key (keySize bytes)
//   - key_pad (padding to align revision to 8 bytes)
//   - revision (8 bytes, int64)
//   - index (indexSize bytes)
//   - padding (to slotSize)
//
// The caller must provide a precomputed slotSize (from computeSlotSize) to
// avoid redundant validation. This ensures error handling happens at cache
// initialization time, not during slot encoding.
func encodeSlot(key []byte, isLive bool, revision int64, index []byte, keySize, indexSize, slotSize uint32) []byte {
	buf := make([]byte, slotSize)

	// Meta: bit 0 = USED.
	var meta uint64
	if isLive {
		meta = slotMetaUsed
	}

	binary.LittleEndian.PutUint64(buf[0:8], meta)

	// Key (keySize bytes starting at offset 8).
	copy(buf[8:8+keySize], key)

	// Key padding is implicit (already zero in the slice).
	// Pad to align revision to 8 bytes.
	keyPad := (8 - (keySize % 8)) % 8

	// Revision (8 bytes, little-endian, at offset 8 + keySize + keyPad).
	revisionOffset := 8 + keySize + keyPad
	putInt64LE(buf[revisionOffset:revisionOffset+8], revision)

	// Index (indexSize bytes starting after revision).
	indexOffset := revisionOffset + 8
	copy(buf[indexOffset:indexOffset+indexSize], index)

	// Remaining padding is implicit (already zero in the slice).

	return buf
}

// decodedSlot holds the deserialized fields of a slot.
type decodedSlot struct {
	key      []byte
	isLive   bool
	revision int64
	index    []byte
}

// decodeSlot deserializes a fixed-size byte slice into slot fields.
func decodeSlot(buf []byte, keySize, indexSize uint32) decodedSlot {
	// Meta: bit 0 = USED.
	meta := binary.LittleEndian.Uint64(buf[0:8])

	// Key (keySize bytes starting at offset 8).
	key := make([]byte, keySize)
	copy(key, buf[8:8+keySize])

	// Pad to align revision to 8 bytes.
	keyPad := (8 - (keySize % 8)) % 8

	// Revision (8 bytes at offset 8 + keySize + keyPad).
	revisionOffset := 8 + keySize + keyPad
	revision := getInt64LE(buf[revisionOffset : revisionOffset+8])

	// Index (indexSize bytes starting after revision).
	indexOffset := revisionOffset + 8

	var index []byte
	if indexSize > 0 {
		index = make([]byte, indexSize)
		copy(index, buf[indexOffset:indexOffset+indexSize])
	}

	return decodedSlot{
		key:      key,
		isLive:   (meta & slotMetaUsed) != 0,
		revision: revision,
		index:    index,
	}
}

// newHeader creates a header for a new cache file with the given options.
// Returns ErrInvalidInput if slot size or bucket count computation fails.
func newHeader(keySize, indexSize uint32, slotCapacity, userVersion uint64, orderedKeys bool) (slc1Header, error) {
	slotSize, err := computeSlotSize(keySize, indexSize)
	if err != nil {
		return slc1Header{}, err
	}

	bucketCount, err := computeBucketCount(slotCapacity)
	if err != nil {
		return slc1Header{}, err
	}

	slotsOffset := uint64(slc1HeaderSize)
	bucketsOffset := slotsOffset + slotCapacity*uint64(slotSize)

	var flags uint32
	if orderedKeys {
		flags |= slc1FlagOrderedKeys
	}

	return slc1Header{
		Magic:            [4]byte{'S', 'L', 'C', '1'},
		Version:          slc1Version,
		HeaderSize:       slc1HeaderSize,
		KeySize:          keySize,
		IndexSize:        indexSize,
		SlotSize:         slotSize,
		HashAlg:          slc1HashAlgFNV1a64,
		Flags:            flags,
		SlotCapacity:     slotCapacity,
		SlotHighwater:    0,
		LiveCount:        0,
		UserVersion:      userVersion,
		Generation:       0, // even = stable
		BucketCount:      bucketCount,
		BucketUsed:       0,
		BucketTombstones: 0,
		SlotsOffset:      slotsOffset,
		BucketsOffset:    bucketsOffset,
		HeaderCRC32C:     0,                    // computed during encode
		State:            stateNormal,          // cache is operational
		UserFlags:        0,                    // caller-owned, default zero
		UserData:         [UserDataSize]byte{}, // caller-owned, default zero
	}, nil
}

// putInt64LE writes an int64 to buf in little-endian byte order.
// This avoids int64->uint64 conversion that binary.LittleEndian.PutUint64 requires.
func putInt64LE(buf []byte, value int64) {
	// Bounds check hint: if buf[7] is valid, buf[0..6] are too.
	// Lets the compiler eliminate redundant bounds checks below.
	_ = buf[7]

	buf[0] = byte(value)
	buf[1] = byte(value >> 8)
	buf[2] = byte(value >> 16)
	buf[3] = byte(value >> 24)
	buf[4] = byte(value >> 32)
	buf[5] = byte(value >> 40)
	buf[6] = byte(value >> 48)
	buf[7] = byte(value >> 56)
}

// getInt64LE reads an int64 from buf in little-endian byte order.
// This avoids uint64->int64 conversion that binary.LittleEndian.Uint64 returns.
func getInt64LE(buf []byte) int64 {
	// Bounds check hint: if buf[7] is valid, buf[0..6] are too.
	// Lets the compiler eliminate redundant bounds checks below.
	_ = buf[7]

	return int64(buf[0]) |
		int64(buf[1])<<8 |
		int64(buf[2])<<16 |
		int64(buf[3])<<24 |
		int64(buf[4])<<32 |
		int64(buf[5])<<40 |
		int64(buf[6])<<48 |
		int64(buf[7])<<56
}

// atomicLoadUint64 performs an atomic 64-bit load from an 8-byte-aligned
// position in the buffer. The spec requires generation reads to be atomic
// with acquire semantics to ensure proper seqlock operation across processes.
//
// Preconditions:
//   - buf must be at least 8 bytes
//   - buf[0] must be 8-byte aligned (enforced by SLC1 format: generation is at offset 0x40)
//
// Go's sync/atomic operations provide sequential consistency (stronger than
// acquire/release), which satisfies the spec requirements.
func atomicLoadUint64(buf []byte) uint64 {
	// Bounds check.
	_ = buf[7]

	// SAFETY: The SLC1 format places generation at offset 0x40 (64 bytes),
	// which is 8-byte aligned. The mmap'd buffer starts at the file beginning,
	// so &buf[0] is 8-byte aligned for the generation field.
	return atomic.LoadUint64((*uint64)(unsafe.Pointer(&buf[0])))
}

// atomicStoreUint64 performs an atomic 64-bit store to an 8-byte-aligned
// position in the buffer. The spec requires generation writes to be atomic
// with release semantics to ensure readers observe all prior data writes.
//
// Preconditions:
//   - buf must be at least 8 bytes
//   - buf[0] must be 8-byte aligned (enforced by SLC1 format: generation is at offset 0x40)
//
// Go's sync/atomic operations provide sequential consistency (stronger than
// acquire/release), which satisfies the spec requirements.
func atomicStoreUint64(buf []byte, val uint64) {
	// Bounds check.
	_ = buf[7]

	// SAFETY: Same alignment guarantees as atomicLoadUint64.
	atomic.StoreUint64((*uint64)(unsafe.Pointer(&buf[0])), val)
}

// atomicLoadInt64 performs an atomic 64-bit load from an 8-byte-aligned
// position in the buffer and returns it as int64. Used for slot revision reads.
//
// Preconditions:
//   - buf must be at least 8 bytes
//   - buf[0] must be 8-byte aligned (enforced by SLC1 slot layout: revision is 8-byte aligned)
//
// Go's sync/atomic operations provide sequential consistency.
func atomicLoadInt64(buf []byte) int64 {
	// Bounds check.
	_ = buf[7]

	// SAFETY: SLC1 slot layout ensures revision is 8-byte aligned:
	// meta(8) + key + key_pad(align to 8) + revision(8)
	return atomic.LoadInt64((*int64)(unsafe.Pointer(&buf[0])))
}

// atomicStoreInt64 performs an atomic 64-bit store to an 8-byte-aligned
// position in the buffer. Used for slot revision writes.
//
// Preconditions:
//   - buf must be at least 8 bytes
//   - buf[0] must be 8-byte aligned (enforced by SLC1 slot layout: revision is 8-byte aligned)
//
// Go's sync/atomic operations provide sequential consistency.
func atomicStoreInt64(buf []byte, val int64) {
	// Bounds check.
	_ = buf[7]

	// SAFETY: Same alignment guarantees as atomicLoadInt64.
	atomic.StoreInt64((*int64)(unsafe.Pointer(&buf[0])), val)
}

// pageSize is the system page size, used for aligning msync ranges.
// macOS requires page-aligned ranges for msync.
var pageSize = unix.Getpagesize()

// msyncRange performs a synchronous msync on the given byte range.
// The range is automatically page-aligned to satisfy macOS requirements.
//
// Returns ErrInvalidInput if:
//   - length <= 0
//   - offset < 0
//   - offset >= len(data)
//
// Callers should validate ranges before calling msyncRange. Invalid ranges
// indicate a bug in dirty-range tracking or file layout validation.
func msyncRange(data []byte, offset, length int) error {
	if length <= 0 {
		return fmt.Errorf("msyncRange: length %d <= 0: %w", length, ErrInvalidInput)
	}

	if offset < 0 {
		return fmt.Errorf("msyncRange: offset %d < 0: %w", offset, ErrInvalidInput)
	}

	if offset >= len(data) {
		return fmt.Errorf("msyncRange: offset %d >= data length %d: %w", offset, len(data), ErrInvalidInput)
	}

	// Clamp to data bounds.
	if offset+length > len(data) {
		length = len(data) - offset
	}

	// Page-align: round offset down, round end up.
	alignedStart := (offset / pageSize) * pageSize
	end := offset + length
	alignedEnd := min(
		// Clamp alignedEnd to data bounds.
		((end+pageSize-1)/pageSize)*pageSize, len(data))

	alignedLen := alignedEnd - alignedStart
	if alignedLen <= 0 {
		// This should not happen after the above validation, but guard anyway.
		return fmt.Errorf("msyncRange: aligned length %d <= 0: %w", alignedLen, ErrInvalidInput)
	}

	err := unix.Msync(data[alignedStart:alignedStart+alignedLen], unix.MS_SYNC)
	if err != nil {
		return fmt.Errorf("msync: %w", err)
	}

	return nil
}
