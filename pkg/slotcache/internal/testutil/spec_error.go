package testutil

import "fmt"

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
