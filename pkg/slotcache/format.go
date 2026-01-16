//go:build slotcache_impl

package slotcache

import (
	"encoding/binary"
	"hash/crc32"
)

// SLC1 file format constants.
const (
	// Magic bytes at the start of every SLC1 file.
	slc1Magic = "SLC1"

	// File format version.
	slc1Version = 1

	// Fixed header size in bytes.
	slc1HeaderSize = 256

	// Hash algorithm identifier (FNV-1a 64-bit).
	slc1HashAlgFNV1a64 = 1

	// Bucket sentinel values.
	bucketEmpty     = 0
	bucketTombstone = 0xFFFFFFFFFFFFFFFF

	// Maximum slot capacity (bucket sentinel constraint).
	maxSlotCapacity = 0xFFFFFFFFFFFFFFFE
)

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
func encodeHeader(h *slc1Header) []byte {
	buf := make([]byte, slc1HeaderSize)

	// Magic and fixed fields.
	copy(buf[offMagic:], h.Magic[:])
	binary.LittleEndian.PutUint32(buf[offVersion:], h.Version)
	binary.LittleEndian.PutUint32(buf[offHeaderSize:], h.HeaderSize)
	binary.LittleEndian.PutUint32(buf[offKeySize:], h.KeySize)
	binary.LittleEndian.PutUint32(buf[offIndexSize:], h.IndexSize)
	binary.LittleEndian.PutUint32(buf[offSlotSize:], h.SlotSize)
	binary.LittleEndian.PutUint32(buf[offHashAlg:], h.HashAlg)
	binary.LittleEndian.PutUint32(buf[offFlags:], h.Flags)

	// 64-bit fields.
	binary.LittleEndian.PutUint64(buf[offSlotCapacity:], h.SlotCapacity)
	binary.LittleEndian.PutUint64(buf[offSlotHighwater:], h.SlotHighwater)
	binary.LittleEndian.PutUint64(buf[offLiveCount:], h.LiveCount)
	binary.LittleEndian.PutUint64(buf[offUserVersion:], h.UserVersion)
	binary.LittleEndian.PutUint64(buf[offGeneration:], h.Generation)
	binary.LittleEndian.PutUint64(buf[offBucketCount:], h.BucketCount)
	binary.LittleEndian.PutUint64(buf[offBucketUsed:], h.BucketUsed)
	binary.LittleEndian.PutUint64(buf[offBucketTombstones:], h.BucketTombstones)
	binary.LittleEndian.PutUint64(buf[offSlotsOffset:], h.SlotsOffset)
	binary.LittleEndian.PutUint64(buf[offBucketsOffset:], h.BucketsOffset)

	// Reserved fields are zero (already zero in the slice).
	binary.LittleEndian.PutUint32(buf[offReservedU32:], h.ReservedU32)

	// Compute CRC with generation and crc fields zeroed.
	crc := computeHeaderCRC(buf)
	binary.LittleEndian.PutUint32(buf[offHeaderCRC32C:], crc)

	return buf
}

// decodeHeader deserializes a 256-byte slice into a header struct.
// Returns the header without validating CRC (caller should validate separately).
func decodeHeader(buf []byte) slc1Header {
	var h slc1Header

	copy(h.Magic[:], buf[offMagic:offMagic+4])
	h.Version = binary.LittleEndian.Uint32(buf[offVersion:])
	h.HeaderSize = binary.LittleEndian.Uint32(buf[offHeaderSize:])
	h.KeySize = binary.LittleEndian.Uint32(buf[offKeySize:])
	h.IndexSize = binary.LittleEndian.Uint32(buf[offIndexSize:])
	h.SlotSize = binary.LittleEndian.Uint32(buf[offSlotSize:])
	h.HashAlg = binary.LittleEndian.Uint32(buf[offHashAlg:])
	h.Flags = binary.LittleEndian.Uint32(buf[offFlags:])

	h.SlotCapacity = binary.LittleEndian.Uint64(buf[offSlotCapacity:])
	h.SlotHighwater = binary.LittleEndian.Uint64(buf[offSlotHighwater:])
	h.LiveCount = binary.LittleEndian.Uint64(buf[offLiveCount:])
	h.UserVersion = binary.LittleEndian.Uint64(buf[offUserVersion:])
	h.Generation = binary.LittleEndian.Uint64(buf[offGeneration:])
	h.BucketCount = binary.LittleEndian.Uint64(buf[offBucketCount:])
	h.BucketUsed = binary.LittleEndian.Uint64(buf[offBucketUsed:])
	h.BucketTombstones = binary.LittleEndian.Uint64(buf[offBucketTombstones:])
	h.SlotsOffset = binary.LittleEndian.Uint64(buf[offSlotsOffset:])
	h.BucketsOffset = binary.LittleEndian.Uint64(buf[offBucketsOffset:])

	h.HeaderCRC32C = binary.LittleEndian.Uint32(buf[offHeaderCRC32C:])
	h.ReservedU32 = binary.LittleEndian.Uint32(buf[offReservedU32:])

	return h
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
// where key_pad = (8 - (key_size % 8)) % 8
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

	// ceil(slotCapacity / loadFactor)
	needed := uint64(float64(slotCapacity)/loadFactor + 0.999999999)
	if needed < 2 {
		needed = 2
	}

	return nextPow2(needed)
}

// nextPow2 returns the smallest power of two >= x.
func nextPow2(x uint64) uint64 {
	if x == 0 {
		return 1
	}
	x--
	x |= x >> 1
	x |= x >> 2
	x |= x >> 4
	x |= x >> 8
	x |= x >> 16
	x |= x >> 32
	return x + 1
}

// newHeader creates a header for a new cache file with the given options.
func newHeader(keySize, indexSize uint32, slotCapacity, userVersion uint64, loadFactor float64) slc1Header {
	slotSize := computeSlotSize(keySize, indexSize)
	bucketCount := computeBucketCount(slotCapacity, loadFactor)
	slotsOffset := uint64(slc1HeaderSize)
	bucketsOffset := slotsOffset + slotCapacity*uint64(slotSize)

	return slc1Header{
		Magic:            [4]byte{'S', 'L', 'C', '1'},
		Version:          slc1Version,
		HeaderSize:       slc1HeaderSize,
		KeySize:          keySize,
		IndexSize:        indexSize,
		SlotSize:         slotSize,
		HashAlg:          slc1HashAlgFNV1a64,
		Flags:            0,
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
