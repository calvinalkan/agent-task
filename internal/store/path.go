package store

import (
	"fmt"
	"path/filepath"

	"github.com/google/uuid"
)

// TicketPath derives the canonical ticket location for a UUIDv7 relative to the ticket dir.
// We key the directory by the embedded UTC timestamp to keep file layout stable.
func TicketPath(id uuid.UUID) (string, error) {
	err := validateUUIDv7(id)
	if err != nil {
		return "", fmt.Errorf("derive path: %w", err)
	}

	shortID := shortIDFromUUIDBits(id)
	createdAt := uuidV7Time(id)
	relDir := createdAt.Format(pathDateLayout)

	return filepath.Join(relDir, shortID+".md"), nil
}

const pathDateLayout = "2006/01-02" // YYYY/MM-DD (example: 2026/01-31)
