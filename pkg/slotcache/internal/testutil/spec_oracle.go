package testutil

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math/bits"
	"os"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

// ValidateFile reads the file at filePath and validates
// that it matches the slotcache v1 file-format invariants.
//
// This is a *test-only* oracle. It must NOT call into slotcache internals.
func ValidateFile(filePath string, options slotcache.Options) error {
	fileBytes, readError := os.ReadFile(filePath)
	if readError != nil {
		return fmt.Errorf("speccheck: read file: %w", readError)
	}

	return validateSlotcacheBytesAgainstOptions(fileBytes, options)
}

// SpecErrorKind classifies the type of invariant violation detected by the
// spec_oracle validator.
//
// These are intended for fuzz tests that want to react to specific categories
// of corruption (e.g. perform targeted probing).
//
// NOTE: This is test-only code.
type SpecErrorKind uint8

const (
	// SpecErrUnknown is a catch-all for unclassified spec_oracle failures.
	SpecErrUnknown SpecErrorKind = iota

	// SpecErrBucketSlotOutOfRange indicates a bucket references a slot_id >= slot_highwater.
	SpecErrBucketSlotOutOfRange

	// SpecErrBucketPointsToNonLiveSlot indicates a bucket references a slot that is not live.
	SpecErrBucketPointsToNonLiveSlot

	// SpecErrBucketHashMismatchForSlot indicates a bucket's stored hash does not match the
	// hash of the key bytes in the referenced slot.
	SpecErrBucketHashMismatchForSlot

	// SpecErrNoEmptyBuckets indicates the on-disk bucket table has no EMPTY buckets
	// (all buckets are FULL or TOMBSTONE).
	//
	// This is directly observable via Cache.Get() on a non-existent key: the lookup
	// will probe bucket_count entries without encountering EMPTY and must treat
	// that as corruption.
	SpecErrNoEmptyBuckets

	// SpecErrKeyNotFindable indicates a live key is not findable via the bucket probe sequence.
	SpecErrKeyNotFindable
)

func (k SpecErrorKind) String() string {
	switch k {
	case SpecErrBucketSlotOutOfRange:
		return "BucketSlotOutOfRange"
	case SpecErrBucketPointsToNonLiveSlot:
		return "BucketPointsToNonLiveSlot"
	case SpecErrBucketHashMismatchForSlot:
		return "BucketHashMismatchForSlot"
	case SpecErrNoEmptyBuckets:
		return "NoEmptyBuckets"
	case SpecErrKeyNotFindable:
		return "KeyNotFindable"
	default:
		return "Unknown"
	}
}

// SpecError is a structured error returned by spec_oracle for certain classes
// of failures.
//
// It intentionally includes enough metadata for fuzz tests to craft targeted
// read operations.
type SpecError struct {
	Kind SpecErrorKind

	BucketIndex uint64
	BucketCount uint64

	// BucketFull and BucketTombstones are counts observed by the oracle while
	// scanning buckets (not header counters). They are only set for certain
	// error kinds.
	BucketFull       uint64
	BucketTombstones uint64

	SlotID        uint64
	SlotHighwater uint64

	// Key holds the relevant key bytes when available (copied).
	Key []byte

	StoredHash   uint64
	ComputedHash uint64
}

func (e *SpecError) Error() string {
	switch e.Kind {
	case SpecErrBucketSlotOutOfRange:
		return fmt.Sprintf("speccheck: bucket %d points to slot %d, but slot_highwater=%d", e.BucketIndex, e.SlotID, e.SlotHighwater)
	case SpecErrBucketPointsToNonLiveSlot:
		return fmt.Sprintf("speccheck: bucket %d points to non-live slot %d", e.BucketIndex, e.SlotID)
	case SpecErrBucketHashMismatchForSlot:
		return fmt.Sprintf("speccheck: bucket %d hash mismatch for slot %d: computed=0x%016X stored=0x%016X", e.BucketIndex, e.SlotID, e.ComputedHash, e.StoredHash)
	case SpecErrNoEmptyBuckets:
		return fmt.Sprintf("speccheck: hash table has no EMPTY buckets: bucket_count=%d full=%d tombstones=%d", e.BucketCount, e.BucketFull, e.BucketTombstones)
	case SpecErrKeyNotFindable:
		return fmt.Sprintf("speccheck: key not findable via probe sequence (slot %d, key %x)", e.SlotID, e.Key)
	default:
		return "speccheck: validation failed"
	}
}

// DescribeSpecOracleError formats a spec_oracle validation error for high-signal
// fuzz failure output.
//
// If err is a *SpecError, this includes the structured fields in addition to
// the human-readable Error() string.
func DescribeSpecOracleError(err error) string {
	if err == nil {
		return ""
	}

	var se *SpecError
	if errors.As(err, &se) {
		return fmt.Sprintf("%v\nkind=%s bucket_index=%d bucket_count=%d bucket_full=%d bucket_tombstones=%d slot_id=%d slot_highwater=%d key=%x stored_hash=0x%016X computed_hash=0x%016X",
			se,
			se.Kind,
			se.BucketIndex,
			se.BucketCount,
			se.BucketFull,
			se.BucketTombstones,
			se.SlotID,
			se.SlotHighwater,
			se.Key,
			se.StoredHash,
			se.ComputedHash,
		)
	}

	return err.Error()
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
	if keySize < 1 {
		return fmt.Errorf("speccheck: key_size must be >= 1: got %d", keySize)
	}

	indexSize := binary.LittleEndian.Uint32(headerBytes[0x010:0x014])
	slotSize := binary.LittleEndian.Uint32(headerBytes[0x014:0x018])

	hashAlgorithm := binary.LittleEndian.Uint32(headerBytes[0x018:0x01C])
	if hashAlgorithm != 1 {
		return fmt.Errorf("speccheck: unsupported hash_alg: got %d want 1 (FNV-1a 64-bit)", hashAlgorithm)
	}

	flags := binary.LittleEndian.Uint32(headerBytes[0x01C:0x020])

	const flagOrderedKeys uint32 = 1 << 0
	if flags&^flagOrderedKeys != 0 {
		return fmt.Errorf("speccheck: unknown flags set: 0x%X", flags)
	}

	isOrdered := (flags & flagOrderedKeys) != 0

	slotCapacity := binary.LittleEndian.Uint64(headerBytes[0x020:0x028])

	const maxSlotCapacity uint64 = 0xFFFFFFFFFFFFFFFE

	if slotCapacity < 1 {
		return fmt.Errorf("speccheck: slot_capacity must be >= 1: got %d", slotCapacity)
	}

	if slotCapacity > maxSlotCapacity {
		return fmt.Errorf("speccheck: slot_capacity exceeds max: got %d max %d", slotCapacity, maxSlotCapacity)
	}

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

	// State field at 0x074: allowed values are 0 (normal) and 1 (invalidated).
	state := binary.LittleEndian.Uint32(headerBytes[0x074:0x078])
	if state > 1 {
		return fmt.Errorf("speccheck: unknown state value at 0x074: got %d (must be 0 or 1)", state)
	}

	// User header bytes (0x078..0x0BF) are caller-owned and may be any value.
	// Reserved tail bytes (0x0C0..0x0FF) MUST be zero.
	const reservedTailStart = 0x0C0
	for reservedIndex := reservedTailStart; reservedIndex < headerSizeBytes; reservedIndex++ {
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

	expectedSlotSize := derivedSlotSizeBytes(keySize, indexSize)
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

	if bucketCount < 2 || bits.OnesCount64(bucketCount) != 1 {
		return fmt.Errorf("speccheck: bucket_count must be a power of two >= 2: got %d", bucketCount)
	}

	if bucketUsed > bucketCount {
		return fmt.Errorf("speccheck: bucket_used out of range: %d > %d", bucketUsed, bucketCount)
	}

	if bucketTombstones > bucketCount {
		return fmt.Errorf("speccheck: bucket_tombstones out of range: %d > %d", bucketTombstones, bucketCount)
	}

	if bucketUsed+bucketTombstones >= bucketCount {
		return fmt.Errorf("speccheck: bucket_used + bucket_tombstones must be < bucket_count: used=%d tomb=%d count=%d", bucketUsed, bucketTombstones, bucketCount)
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
	keySizeBytes := uint64(keySize)
	indexSizeBytes := uint64(indexSize)
	keyPaddingBytes := uint64((8 - (keySize % 8)) % 8)

	slotPayloadSize := uint64(8) + keySizeBytes + keyPaddingBytes + 8 + indexSizeBytes
	if slotPayloadSize > slotSizeBytes {
		return fmt.Errorf("speccheck: slot_size too small for layout: payload=%d slot_size=%d", slotPayloadSize, slotSizeBytes)
	}

	slotTrailingPaddingBytes := slotSizeBytes - slotPayloadSize

	liveSlotKeysBySlotID := make(map[uint64][]byte)
	seenLiveKeys := make(map[string]bool)

	var (
		countedLiveSlots uint64
		prevOrderedKey   []byte
	)

	for slotID := range slotHighwater {
		slotOffset := slotsOffset + slotID*slotSizeBytes
		slotEnd := slotOffset + slotSizeBytes

		if slotEnd > uint64(len(fileBytes)) {
			return fmt.Errorf("speccheck: slot %d extends beyond file length", slotID)
		}

		if slotTrailingPaddingBytes > 0 {
			paddingStart := slotOffset + slotPayloadSize
			for padIndex := paddingStart; padIndex < slotEnd; padIndex++ {
				if fileBytes[padIndex] != 0 {
					return fmt.Errorf("speccheck: slot %d padding byte at 0x%X must be 0", slotID, padIndex)
				}
			}
		}

		meta := binary.LittleEndian.Uint64(fileBytes[slotOffset : slotOffset+8])

		reservedBits := meta &^ uint64(1)
		if reservedBits != 0 {
			return fmt.Errorf("speccheck: slot %d meta has reserved bits set: meta=0x%016X", slotID, meta)
		}

		isUsed := (meta & 1) == 1

		var keyBytes []byte

		if isOrdered {
			keyBytesStart := slotOffset + 8
			keyBytesEnd := keyBytesStart + uint64(keySize)

			if keyBytesEnd > uint64(len(fileBytes)) {
				return fmt.Errorf("speccheck: slot %d key bytes out of range", slotID)
			}

			keyBytes = make([]byte, keySize)
			copy(keyBytes, fileBytes[keyBytesStart:keyBytesEnd])

			if prevOrderedKey != nil && bytes.Compare(keyBytes, prevOrderedKey) < 0 {
				return fmt.Errorf("speccheck: ordered mode violated at slot %d: key %x < prev %x", slotID, keyBytes, prevOrderedKey)
			}

			prevOrderedKey = keyBytes
		}

		if !isUsed {
			continue
		}

		if !isOrdered {
			keyBytesStart := slotOffset + 8
			keyBytesEnd := keyBytesStart + uint64(keySize)

			if keyBytesEnd > uint64(len(fileBytes)) {
				return fmt.Errorf("speccheck: slot %d key bytes out of range", slotID)
			}

			keyBytes = make([]byte, keySize)
			copy(keyBytes, fileBytes[keyBytesStart:keyBytesEnd])
		}

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

	// Slots beyond slot_highwater must be unallocated.
	for slotID := slotHighwater; slotID < slotCapacity; slotID++ {
		slotOffset := slotsOffset + slotID*slotSizeBytes

		slotEnd := slotOffset + slotSizeBytes
		if slotEnd > uint64(len(fileBytes)) {
			return fmt.Errorf("speccheck: slot %d extends beyond file length", slotID)
		}

		meta := binary.LittleEndian.Uint64(fileBytes[slotOffset : slotOffset+8])
		if meta != 0 {
			return fmt.Errorf("speccheck: slot %d beyond highwater has meta set: meta=0x%016X", slotID, meta)
		}
	}

	// ---------------------------------------------------------------------
	// Deep bucket scan: validate bucket entries and reachability
	// ---------------------------------------------------------------------

	mask := bucketCount - 1

	var (
		countedFullBuckets      uint64
		countedTombstoneBuckets uint64
		sawEmpty                bool
	)

	// Each live slot must appear exactly once in buckets.
	slotIDsReferencedByBuckets := make(map[uint64]bool)

	for bucketIndex := range bucketCount {
		bucketOffset := bucketsOffset + bucketIndex*16

		storedHash := binary.LittleEndian.Uint64(fileBytes[bucketOffset : bucketOffset+8])
		slotPlusOne := binary.LittleEndian.Uint64(fileBytes[bucketOffset+8 : bucketOffset+16])

		if slotPlusOne == 0 {
			sawEmpty = true

			continue // EMPTY
		}

		if slotPlusOne == ^uint64(0) {
			countedTombstoneBuckets++

			continue
		}

		countedFullBuckets++
		slotID := slotPlusOne - 1

		if slotID >= slotHighwater {
			return &SpecError{
				Kind:          SpecErrBucketSlotOutOfRange,
				BucketIndex:   bucketIndex,
				BucketCount:   bucketCount,
				SlotID:        slotID,
				SlotHighwater: slotHighwater,
			}
		}

		keyBytes, isLive := liveSlotKeysBySlotID[slotID]
		if !isLive {
			// For targeted probing, we also capture the slot key bytes from disk.
			slotKey := readSlotKey(fileBytes, slotsOffset, slotSizeBytes, keySizeBytes, slotID)
			computedHash := fnv1a64(slotKey)

			return &SpecError{
				Kind:          SpecErrBucketPointsToNonLiveSlot,
				BucketIndex:   bucketIndex,
				BucketCount:   bucketCount,
				SlotID:        slotID,
				SlotHighwater: slotHighwater,
				Key:           slotKey,
				StoredHash:    storedHash,
				ComputedHash:  computedHash,
			}
		}

		computedHash := fnv1a64(keyBytes)
		if computedHash != storedHash {
			return &SpecError{
				Kind:          SpecErrBucketHashMismatchForSlot,
				BucketIndex:   bucketIndex,
				BucketCount:   bucketCount,
				SlotID:        slotID,
				SlotHighwater: slotHighwater,
				Key:           append([]byte(nil), keyBytes...),
				StoredHash:    storedHash,
				ComputedHash:  computedHash,
			}
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
			return &SpecError{
				Kind:        SpecErrKeyNotFindable,
				BucketCount: bucketCount,
				SlotID:      slotID,
				Key:         append([]byte(nil), keyBytes...),
			}
		}
	}

	// If the bucket table has no EMPTY buckets, any Get() miss must probe the
	// full bucket_count range. The runtime detects this as an impossible
	// invariant and returns ErrCorrupt.
	if !sawEmpty {
		return &SpecError{
			Kind:             SpecErrNoEmptyBuckets,
			BucketCount:      bucketCount,
			BucketFull:       countedFullBuckets,
			BucketTombstones: countedTombstoneBuckets,
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

func derivedSlotSizeBytes(keySize uint32, indexSize uint32) uint32 {
	// Compute slot size per spec: meta(8) + key + key_pad + revision(8) + index, aligned to 8.
	// key_pad ensures revision is 8-byte aligned.
	keyPaddingBytes := (8 - (keySize % 8)) % 8

	rawSize := 8 + keySize + keyPaddingBytes + 8 + indexSize

	return alignTo8U32(rawSize)
}

func alignTo8U32(byteCount uint32) uint32 {
	remainder := byteCount % 8
	if remainder == 0 {
		return byteCount
	}

	return byteCount + (8 - remainder)
}

// Helpers used by spec_oracle on rare (error) paths.

func readSlotKey(fileBytes []byte, slotsOffset, slotSizeBytes, keySizeBytes, slotID uint64) []byte {
	if keySizeBytes == 0 {
		return nil
	}

	off := slotsOffset + slotID*slotSizeBytes + 8

	end := off + keySizeBytes
	if end > uint64(len(fileBytes)) {
		return nil
	}

	key := make([]byte, keySizeBytes)
	copy(key, fileBytes[off:end])

	return key
}

// Header state values (mirrors slotcache.stateNormal and stateInvalidated).
const (
	StateNormal      uint32 = 0
	StateInvalidated uint32 = 1
)

// offState is the byte offset of the state field in the SLC1 header.
const offState = 0x074

// ReadHeaderState reads the state field from the header of a slotcache file.
// Returns an error if the file cannot be read or is too small.
//
// This helper is intended for unit tests that need to verify the on-disk state
// after operations like Invalidate().
func ReadHeaderState(filePath string) (uint32, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return 0, fmt.Errorf("open file: %w", err)
	}

	defer func() { _ = f.Close() }()

	buf := make([]byte, 4)

	n, err := f.ReadAt(buf, offState)
	if err != nil || n < 4 {
		return 0, fmt.Errorf("read state field: %w", err)
	}

	return binary.LittleEndian.Uint32(buf), nil
}

// AssertHeaderState reads the state field from a slotcache file and returns
// an error if it doesn't match the expected value.
//
// This helper is intended for unit tests that want a concise assertion.
func AssertHeaderState(filePath string, expectedState uint32) error {
	state, err := ReadHeaderState(filePath)
	if err != nil {
		return err
	}

	if state != expectedState {
		var expectedName, actualName string

		switch expectedState {
		case StateNormal:
			expectedName = "STATE_NORMAL"
		case StateInvalidated:
			expectedName = "STATE_INVALIDATED"
		default:
			expectedName = fmt.Sprintf("unknown(%d)", expectedState)
		}

		switch state {
		case StateNormal:
			actualName = "STATE_NORMAL"
		case StateInvalidated:
			actualName = "STATE_INVALIDATED"
		default:
			actualName = fmt.Sprintf("unknown(%d)", state)
		}

		return fmt.Errorf("state mismatch: expected %s, got %s", expectedName, actualName)
	}

	return nil
}
