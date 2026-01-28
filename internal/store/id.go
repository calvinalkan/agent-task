// Package store provides storage primitives for tk.
package store

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// newUUIDv7 generates time-ordered IDs so later path derivation can rely on the
// embedded timestamp without extra metadata.
func newUUIDv7() (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("generate uuidv7: %w", err)
	}

	return id, nil
}

// parseUUIDv7 parses a string as a UUIDv7, returning an error if the string
// is not a valid UUID or not version 7.
func parseUUIDv7(s string) (uuid.UUID, error) {
	if s == "" {
		return uuid.UUID{}, fmt.Errorf("empty id")
	}

	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("invalid id %q: %w", s, err)
	}

	if id.Version() != 7 {
		return uuid.UUID{}, fmt.Errorf("id %q is not UUIDv7", s)
	}

	return id, nil
}

const (
	shortIDLength = 12
	crockfordBase = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
)

// shortIDFromUUID derives a stable, 12-char base32 (Crockford) ID from the
// UUIDv7 random bits so filenames stay stable even if timestamps are identical.
// Caller must ensure id is a valid UUIDv7.
func shortIDFromUUID(id uuid.UUID) (string, error) {
	if id.Version() != 7 {
		return "", fmt.Errorf("expected UUIDv7, got version %d", id.Version())
	}

	return shortIDFromUUIDBits(id), nil
}

// pathFromID derives the canonical ticket location for a UUIDv7 relative to the ticket dir.
// We key the directory by the embedded UTC timestamp to keep file layout stable.
// Caller must ensure id is a valid UUIDv7.
func pathFromID(id uuid.UUID) (string, error) {
	if id.Version() != 7 {
		return "", fmt.Errorf("expected UUIDv7, got version %d", id.Version())
	}

	shortID := shortIDFromUUIDBits(id)
	createdAt := uuidV7Time(id)
	relDir := createdAt.Format(pathDateLayout)

	return filepath.Join(relDir, shortID+".md"), nil
}

const pathDateLayout = "2006/01-02" // YYYY/MM-DD (example: 2026/01-31)

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

func shortIDFromUUIDBits(id uuid.UUID) string {
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

	return encodeCrockfordBase32(top60)
}
