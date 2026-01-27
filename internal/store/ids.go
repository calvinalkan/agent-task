// Package store provides storage primitives for tk.
package store

import (
	"fmt"

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
