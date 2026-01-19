package testutil

import (
	"errors"
	"fmt"
)

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
