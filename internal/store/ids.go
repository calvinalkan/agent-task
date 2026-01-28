// Package store provides storage primitives for tk.
package store

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// NewUUIDv7 generates time-ordered IDs so later path derivation can rely on the
// embedded timestamp without extra metadata.
func NewUUIDv7() (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("new uuidv7: %w", err)
	}

	return id, nil
}

const (
	shortIDLength = 12
	crockfordBase = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
)

// ShortIDFromUUID derives a stable, 12-char base32 (Crockford) ID from the
// UUIDv7 random bits so filenames stay stable even if timestamps are identical.
func ShortIDFromUUID(id uuid.UUID) (string, error) {
	err := validateUUIDv7(id)
	if err != nil {
		return "", fmt.Errorf("short id: %w", err)
	}

	// UUIDv7 layout (RFC 9562): 48-bit time, 4-bit version, 12-bit rand_a,
	// 2-bit variant, 62-bit rand_b. We use the high 60 random bits for short IDs.
	randA := (uint16(id[6]&0x0f) << 8) | uint16(id[7])
	randB := (uint64(id[8]&0x3f) << 56) |
		(uint64(id[9]) << 48) |
		(uint64(id[10]) << 40) |
		(uint64(id[11]) << 32) |
		(uint64(id[12]) << 24) |
		(uint64(id[13]) << 16) |
		(uint64(id[14]) << 8) |
		uint64(id[15])

	top60 := (uint64(randA) << 48) | (randB >> 14)

	return encodeCrockfordBase32(top60), nil
}

func encodeCrockfordBase32(value uint64) string {
	var buf [shortIDLength]byte
	for i := shortIDLength - 1; i >= 0; i-- {
		buf[i] = crockfordBase[value&0x1f]
		value >>= 5
	}

	return string(buf[:])
}

func uuidV7Time(id uuid.UUID) time.Time {
	sec, nsec := id.Time().UnixTime()

	return time.Unix(sec, nsec).UTC()
}

func validateUUIDv7(id uuid.UUID) error {
	if id.Version() != 7 {
		return fmt.Errorf("invalid uuidv7: version %d", id.Version())
	}

	if id.Variant() != uuid.RFC4122 {
		return fmt.Errorf("invalid uuidv7: variant %d", id.Variant())
	}

	return nil
}
