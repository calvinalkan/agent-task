package store

import (
	"path/filepath"

	"github.com/google/uuid"
)

// TicketPath derives the canonical ticket location for a UUIDv7.
// We key the directory by the embedded UTC timestamp to keep file layout stable.
func TicketPath(id uuid.UUID) (string, error) {
	err := validateUUIDv7(id)
	if err != nil {
		return "", err
	}

	shortID, err := ShortIDFromUUID(id)
	if err != nil {
		return "", err
	}

	createdAt := uuidV7Time(id)
	relDir := createdAt.Format(pathDateLayout)

	return filepath.Join(".tickets", relDir, shortID+".md"), nil
}

const pathDateLayout = "2006/01-02" // YYYY/MM-DD (example: 2026/01-31)
