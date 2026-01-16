//go:build slotcache_impl

package slotcache_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math/bits"
	"os"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

// validateSlotcacheFileAgainstOptions reads the file at filePath and validates
// that it matches the slotcache v1 file-format invariants.
//
// This is a *test-only* oracle. It must NOT call into slotcache internals.
func validateSlotcacheFileAgainstOptions(filePath string, options slotcache.Options) error {
	fileBytes, readError := os.ReadFile(filePath)
	if readError != nil {
		return fmt.Errorf("speccheck: read file: %w", readError)
	}

	return validateSlotcacheBytesAgainstOptions(fileBytes, options)
}

func validateSlotcacheBytesAgainstOptions(fileBytes []byte, options slotcache.Options) error {
	const headerSizeBytes = 256

	if len(fileBytes) < headerSizeBytes {
		return fmt.Errorf("speccheck: file too small: got %d bytes, need at least %d", len(fileBytes), headerSizeBytes)
	}

	headerBytes := fileBytes[:headerSizeBytes]

	// ---------------------------------------------------------------------
	// Fixed header fields
	// ---------------------------------------------------------------------

	if !bytes.Equal(headerBytes[0x000:0x004], []byte("SLC1")) {
		return fmt.Errorf("speccheck: bad magic: %q", headerBytes[0x000:0x004])
	}

	version := binary.LittleEndian.Uint32(headerBytes[0x004:0x008])
	if version != 1 {
		return fmt.Errorf("speccheck: unsupported version: %d", version)
	}

	headerSize := binary.LittleEndian.Uint32(headerBytes[0x008:0x00C])
	if headerSize != headerSizeBytes {
		return fmt.Errorf("speccheck: bad header_size: got %d want %d", headerSize, headerSizeBytes)
	}

	keySize := binary.LittleEndian.Uint32(headerBytes[0x00C:0x010])
	indexSize := binary.LittleEndian.Uint32(headerBytes[0x010:0x014])
	slotSize := binary.LittleEndian.Uint32(headerBytes[0x014:0x018])

	hashAlgorithm := binary.LittleEndian.Uint32(headerBytes[0x018:0x01C])
	if hashAlgorithm != 1 {
		return fmt.Errorf("speccheck: unsupported hash_alg: got %d want 1 (FNV-1a 64-bit)", hashAlgorithm)
	}

	flags := binary.LittleEndian.Uint32(headerBytes[0x01C:0x020])
	if flags != 0 {
		return fmt.Errorf("speccheck: flags must be 0 in v1: got %d", flags)
	}

	slotCapacity := binary.LittleEndian.Uint64(headerBytes[0x020:0x028])
	slotHighwater := binary.LittleEndian.Uint64(headerBytes[0x028:0x030])
	liveCount := binary.LittleEndian.Uint64(headerBytes[0x030:0x038])
	userVersion := binary.LittleEndian.Uint64(headerBytes[0x038:0x040])

	generation := binary.LittleEndian.Uint64(headerBytes[0x040:0x048])
	if generation%2 == 1 {
		// A committed file should be stable/even.
		return fmt.Errorf("speccheck: generation is odd (unstable): %d", generation)
	}

	bucketCount := binary.LittleEndian.Uint64(headerBytes[0x048:0x050])
	bucketUsed := binary.LittleEndian.Uint64(headerBytes[0x050:0x058])
	bucketTombstones := binary.LittleEndian.Uint64(headerBytes[0x058:0x060])

	slotsOffset := binary.LittleEndian.Uint64(headerBytes[0x060:0x068])
	bucketsOffset := binary.LittleEndian.Uint64(headerBytes[0x068:0x070])

	headerCRC32C := binary.LittleEndian.Uint32(headerBytes[0x070:0x074])

	reservedU32 := binary.LittleEndian.Uint32(headerBytes[0x074:0x078])
	if reservedU32 != 0 {
		return fmt.Errorf("speccheck: reserved_u32 must be 0: got %d", reservedU32)
	}

	for reservedIndex := 0x078; reservedIndex < headerSizeBytes; reservedIndex++ {
		if headerBytes[reservedIndex] != 0 {
			return fmt.Errorf("speccheck: reserved header byte at 0x%X must be 0", reservedIndex)
		}
	}

	// ---------------------------------------------------------------------
	// CRC32-C (Castagnoli) over header, with generation and crc field zeroed.
	// ---------------------------------------------------------------------

	headerBytesForCRC := make([]byte, headerSizeBytes)
	copy(headerBytesForCRC, headerBytes)

	// Zero the CRC field itself.
	for i := 0x070; i < 0x074; i++ {
		headerBytesForCRC[i] = 0
	}

	// Zero generation bytes.
	for i := 0x040; i < 0x048; i++ {
		headerBytesForCRC[i] = 0
	}

	castagnoliTable := crc32.MakeTable(crc32.Castagnoli)
	computedCRC := crc32.Checksum(headerBytesForCRC, castagnoliTable)

	if computedCRC != headerCRC32C {
		return fmt.Errorf("speccheck: header_crc32c mismatch: computed=0x%08X stored=0x%08X", computedCRC, headerCRC32C)
	}

	// ---------------------------------------------------------------------
	// Options compatibility checks (what Open() would enforce).
	// ---------------------------------------------------------------------

	if int(keySize) != options.KeySize {
		return fmt.Errorf("speccheck: key_size mismatch: file=%d options=%d", keySize, options.KeySize)
	}

	if int(indexSize) != options.IndexSize {
		return fmt.Errorf("speccheck: index_size mismatch: file=%d options=%d", indexSize, options.IndexSize)
	}

	if userVersion != options.UserVersion {
		return fmt.Errorf("speccheck: user_version mismatch: file=%d options=%d", userVersion, options.UserVersion)
	}

	if slotCapacity != options.SlotCapacity {
		return fmt.Errorf("speccheck: slot_capacity mismatch: file=%d options=%d", slotCapacity, options.SlotCapacity)
	}

	// ---------------------------------------------------------------------
	// Derived layout checks
	// ---------------------------------------------------------------------

	expectedSlotSize := uint32(derivedSlotSizeBytes(int(keySize), int(indexSize)))
	if slotSize != expectedSlotSize {
		return fmt.Errorf("speccheck: slot_size mismatch: computed=%d stored=%d", expectedSlotSize, slotSize)
	}

	if slotsOffset != headerSizeBytes {
		return fmt.Errorf("speccheck: slots_offset must be %d in v1: got %d", headerSizeBytes, slotsOffset)
	}

	if slotHighwater > slotCapacity {
		return fmt.Errorf("speccheck: slot_highwater out of range: %d > %d", slotHighwater, slotCapacity)
	}

	if liveCount > slotHighwater {
		return fmt.Errorf("speccheck: live_count out of range: %d > %d", liveCount, slotHighwater)
	}

	if bucketCount == 0 || bits.OnesCount64(bucketCount) != 1 {
		return fmt.Errorf("speccheck: bucket_count must be a power of two > 0: got %d", bucketCount)
	}

	if bucketUsed+bucketTombstones > bucketCount {
		return fmt.Errorf("speccheck: bucket_used + bucket_tombstones out of range: used=%d tomb=%d count=%d", bucketUsed, bucketTombstones, bucketCount)
	}

	if bucketUsed != liveCount {
		return fmt.Errorf("speccheck: bucket_used must equal live_count: bucket_used=%d live_count=%d", bucketUsed, liveCount)
	}

	expectedBucketsOffset := slotsOffset + slotCapacity*uint64(slotSize)
	if bucketsOffset != expectedBucketsOffset {
		return fmt.Errorf("speccheck: buckets_offset mismatch: computed=%d stored=%d", expectedBucketsOffset, bucketsOffset)
	}

	expectedFileMinimumSize := bucketsOffset + bucketCount*16
	if uint64(len(fileBytes)) < expectedFileMinimumSize {
		return fmt.Errorf("speccheck: file too small for layout: got=%d need_at_least=%d", len(fileBytes), expectedFileMinimumSize)
	}

	// ---------------------------------------------------------------------
	// Deep slot scan: count used slots and collect live keys
	// ---------------------------------------------------------------------

	slotSizeBytes := uint64(slotSize)

	liveSlotKeysBySlotID := make(map[uint64][]byte)
	seenLiveKeys := make(map[string]bool)

	var countedLiveSlots uint64

	for slotID := range slotHighwater {
		slotOffset := slotsOffset + slotID*slotSizeBytes

		meta := binary.LittleEndian.Uint64(fileBytes[slotOffset : slotOffset+8])

		reservedBits := meta &^ uint64(1)
		if reservedBits != 0 {
			return fmt.Errorf("speccheck: slot %d meta has reserved bits set: meta=0x%016X", slotID, meta)
		}

		isUsed := (meta & 1) == 1
		if !isUsed {
			continue
		}

		keyBytesStart := slotOffset + 8
		keyBytesEnd := keyBytesStart + uint64(keySize)

		if keyBytesEnd > uint64(len(fileBytes)) {
			return fmt.Errorf("speccheck: slot %d key bytes out of range", slotID)
		}

		keyBytes := make([]byte, keySize)
		copy(keyBytes, fileBytes[keyBytesStart:keyBytesEnd])

		keyString := string(keyBytes)
		if seenLiveKeys[keyString] {
			return fmt.Errorf("speccheck: duplicate live key found in slots: %x", keyBytes)
		}

		seenLiveKeys[keyString] = true
		liveSlotKeysBySlotID[slotID] = keyBytes
		countedLiveSlots++
	}

	if countedLiveSlots != liveCount {
		return fmt.Errorf("speccheck: live_count mismatch: header=%d counted_in_slots=%d", liveCount, countedLiveSlots)
	}

	// ---------------------------------------------------------------------
	// Deep bucket scan: validate bucket entries and reachability
	// ---------------------------------------------------------------------

	mask := bucketCount - 1

	var (
		countedFullBuckets      uint64
		countedTombstoneBuckets uint64
	)

	// Each live slot must appear exactly once in buckets.
	slotIDsReferencedByBuckets := make(map[uint64]bool)

	for bucketIndex := range bucketCount {
		bucketOffset := bucketsOffset + bucketIndex*16

		storedHash := binary.LittleEndian.Uint64(fileBytes[bucketOffset : bucketOffset+8])
		slotPlusOne := binary.LittleEndian.Uint64(fileBytes[bucketOffset+8 : bucketOffset+16])

		if slotPlusOne == 0 {
			continue // EMPTY
		}

		if slotPlusOne == ^uint64(0) {
			countedTombstoneBuckets++

			continue
		}

		countedFullBuckets++
		slotID := slotPlusOne - 1

		if slotID >= slotHighwater {
			return fmt.Errorf("speccheck: bucket %d points to slot %d, but slot_highwater=%d", bucketIndex, slotID, slotHighwater)
		}

		keyBytes, isLive := liveSlotKeysBySlotID[slotID]
		if !isLive {
			return fmt.Errorf("speccheck: bucket %d points to non-live slot %d", bucketIndex, slotID)
		}

		computedHash := fnv1a64(keyBytes)
		if computedHash != storedHash {
			return fmt.Errorf("speccheck: bucket %d hash mismatch for slot %d: computed=0x%016X stored=0x%016X", bucketIndex, slotID, computedHash, storedHash)
		}

		if slotIDsReferencedByBuckets[slotID] {
			return fmt.Errorf("speccheck: slot %d is referenced by multiple buckets", slotID)
		}

		slotIDsReferencedByBuckets[slotID] = true

		// Verify that a real lookup would find this key.
		// (Bounded probe and must find before hitting EMPTY.)
		startingIndex := computedHash & mask
		found := false

		for probeCount := range bucketCount {
			probeIndex := (startingIndex + probeCount) & mask
			probeOffset := bucketsOffset + probeIndex*16

			probeHash := binary.LittleEndian.Uint64(fileBytes[probeOffset : probeOffset+8])
			probeSlotPlusOne := binary.LittleEndian.Uint64(fileBytes[probeOffset+8 : probeOffset+16])

			if probeSlotPlusOne == 0 {
				break // hit EMPTY -> key not findable -> corrupt
			}

			if probeSlotPlusOne == ^uint64(0) {
				continue // tombstone
			}

			probeSlotID := probeSlotPlusOne - 1
			if probeHash == computedHash && probeSlotID == slotID {
				found = true

				break
			}
		}

		if !found {
			return fmt.Errorf("speccheck: key not findable via probe sequence (slot %d, key %x)", slotID, keyBytes)
		}
	}

	if countedFullBuckets != bucketUsed {
		return fmt.Errorf("speccheck: bucket_used mismatch: header=%d counted=%d", bucketUsed, countedFullBuckets)
	}

	if countedTombstoneBuckets != bucketTombstones {
		return fmt.Errorf("speccheck: bucket_tombstones mismatch: header=%d counted=%d", bucketTombstones, countedTombstoneBuckets)
	}

	if uint64(len(slotIDsReferencedByBuckets)) != liveCount {
		return fmt.Errorf("speccheck: bucket index does not reference all live slots: referenced=%d live_count=%d", len(slotIDsReferencedByBuckets), liveCount)
	}

	return nil
}

func derivedSlotSizeBytes(keySize int, indexSize int) int {
	// Compute slot size per spec: meta(8) + key + key_pad + revision(8) + index, aligned to 8.
	// key_pad ensures revision is 8-byte aligned.
	keyPaddingBytes := (8 - (keySize % 8)) % 8

	rawSize := 8 + keySize + keyPaddingBytes + 8 + indexSize

	return alignTo8(rawSize)
}

func alignTo8(byteCount int) int {
	remainder := byteCount % 8
	if remainder == 0 {
		return byteCount
	}

	return byteCount + (8 - remainder)
}

// fnv1a64 computes the FNV-1a 64-bit hash over key bytes.
func fnv1a64(keyBytes []byte) uint64 {
	const (
		offsetBasis = 14695981039346656037
		prime       = 1099511628211
	)

	var hash uint64 = offsetBasis

	for _, b := range keyBytes {
		hash ^= uint64(b)
		hash *= prime
	}

	return hash
}
