package slotcache

import (
	"encoding/binary"
	"hash/crc32"
)

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
	maxInt   = int(^uint(0) >> 1)
	maxInt64 = int64(^uint64(0) >> 1)
)

// safeIntToUint32 converts int to uint32, clamping to valid range.
// Returns 0 if negative, maxUint32 if too large.
func safeIntToUint32(v int) uint32 {
	if v < 0 {
		return 0
	}

	// Convert through uint64 to avoid gosec G115 warning.
	// The range check ensures this is safe.
	u64 := uint64(v)

	const maxUint32Val = uint64(^uint32(0))
	if u64 > maxUint32Val {
		return ^uint32(0)
	}

	return uint32(u64)
}

// safeUint64ToInt64 converts uint64 to int64, clamping to maxInt64 if overflow.
// For file sizes, this is safe as files can't exceed int64 max.
func safeUint64ToInt64(v uint64) int64 {
	if v > uint64(maxInt64) {
		return maxInt64
	}

	return int64(v)
}

// safeUint64ToInt converts uint64 to int, clamping to maxInt if overflow.
func safeUint64ToInt(v uint64) int {
	if v > uint64(maxInt) {
		return maxInt
	}

	return int(v)
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
	offMagic            = 0x000 // [4]byte
	offVersion          = 0x004 // uint32
	offHeaderSize       = 0x008 // uint32
	offKeySize          = 0x00C // uint32
	offIndexSize        = 0x010 // uint32
	offSlotSize         = 0x014 // uint32
	offHashAlg          = 0x018 // uint32
	offFlags            = 0x01C // uint32
	offSlotCapacity     = 0x020 // uint64
	offSlotHighwater    = 0x028 // uint64
	offLiveCount        = 0x030 // uint64
	offUserVersion      = 0x038 // uint64
	offGeneration       = 0x040 // uint64
	offBucketCount      = 0x048 // uint64
	offBucketUsed       = 0x050 // uint64
	offBucketTombstones = 0x058 // uint64
	offSlotsOffset      = 0x060 // uint64
	offBucketsOffset    = 0x068 // uint64
	offHeaderCRC32C     = 0x070 // uint32
	offReservedU32      = 0x074 // uint32
	offReservedStart    = 0x078 // reserved bytes through 0x0FF
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
	ReservedU32      uint32
	// Reserved bytes from 0x078 to 0x0FF (136 bytes) are implicitly zero.
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

	// Reserved fields are zero (already zero in the slice).
	binary.LittleEndian.PutUint32(buf[offReservedU32:], header.ReservedU32)

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

// hasReservedBytesSet checks if any reserved bytes (0x078-0x0FF) are non-zero.
func hasReservedBytesSet(buf []byte) bool {
	for i := offReservedStart; i < slc1HeaderSize; i++ {
		if buf[i] != 0 {
			return true
		}
	}

	return false
}

// computeSlotSize calculates the slot size per spec:
// slot_size = align8( meta(8) + key_size + key_pad + revision(8) + index_size )
// where key_pad = (8 - (key_size % 8)) % 8.
func computeSlotSize(keySize, indexSize uint32) uint32 {
	keyPad := (8 - (keySize % 8)) % 8
	unaligned := 8 + keySize + keyPad + 8 + indexSize // meta + key + pad + revision + index

	return align8(unaligned)
}

// align8 rounds x up to the next multiple of 8.
func align8(x uint32) uint32 {
	return (x + 7) &^ 7
}

// computeBucketCount calculates the bucket count for a given slot capacity and load factor.
// bucket_count = nextPow2( ceil(slot_capacity / load_factor) )
// bucket_count must be >= 2 and a power of two.
func computeBucketCount(slotCapacity uint64, loadFactor float64) uint64 {
	if slotCapacity == 0 || loadFactor <= 0 || loadFactor >= 1 {
		return 2 // minimum valid bucket count
	}

	// Calculate ceiling of capacity divided by load factor.
	needed := max(uint64(float64(slotCapacity)/loadFactor+0.999999999), 2)

	return nextPow2(needed)
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
)

// encodeSlot serializes a slot record to a fixed-size byte slice.
// The slot layout per spec:
//   - meta (8 bytes, uint64)
//   - key (keySize bytes)
//   - key_pad (padding to align revision to 8 bytes)
//   - revision (8 bytes, int64)
//   - index (indexSize bytes)
//   - padding (to slotSize)
func encodeSlot(key []byte, isLive bool, revision int64, index []byte, keySize, indexSize uint32) []byte {
	slotSize := computeSlotSize(keySize, indexSize)
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
func newHeader(keySize, indexSize uint32, slotCapacity, userVersion uint64, loadFactor float64, orderedKeys bool) slc1Header {
	slotSize := computeSlotSize(keySize, indexSize)
	bucketCount := computeBucketCount(slotCapacity, loadFactor)
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
		HeaderCRC32C:     0, // computed during encode
		ReservedU32:      0,
	}
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
