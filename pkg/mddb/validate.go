package mddb

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"
)

// Validation errors for document fields.
var (
	ErrMissingID    = errors.New("missing id")
	ErrEmptyID      = errors.New("id is empty")
	ErrEmptyShortID = errors.New("short id is empty")
	ErrEmptyPath    = errors.New("path is empty")
	ErrEmptyTitle   = errors.New("title is empty")
	ErrInvalidPath  = errors.New("invalid path")
)

// validateDocument checks a Document before Create/Update.
// Returns validated ID and path (path only when valid).
func (mddb *MDDB[T]) validateDocument(d Document) (string, string, error) {
	id := d.ID()
	if id == "" {
		return "", "", ErrEmptyID
	}

	if d.Title() == "" {
		return id, "", ErrEmptyTitle
	}

	path, _, err := mddb.deriveAndValidate(id, nil)
	if err != nil {
		return id, "", err
	}

	return id, path, nil
}

// deriveAndValidate derives path and shortID from id, validating both.
// If expectPath is non-empty, also checks that derived path matches (for parse validation).
// Returns derived path and shortID.
//
// expectPath is borrowed and only used for comparison (not stored).
func (mddb *MDDB[T]) deriveAndValidate(id string, expectPath []byte) (string, string, error) {
	if mddb.cfg.RelPathFromID == nil {
		return "", "", errors.New("RelPathFromID is nil")
	}

	path := mddb.cfg.RelPathFromID(id)
	if path == "" {
		return "", "", ErrEmptyPath
	}

	if err := mddb.validateRelPath(path); err != nil {
		return "", "", fmt.Errorf("%w: path %q", err, path)
	}

	// Compare without allocating: unsafe.String creates a view, not a copy.
	// Safe because expectPath outlives this comparison and result isn't stored.
	if len(expectPath) > 0 && path != unsafe.String(unsafe.SliceData(expectPath), len(expectPath)) {
		return "", "", fmt.Errorf("path mismatch: file at %q, derived %q", expectPath, path)
	}

	if mddb.cfg.ShortIDFromID == nil {
		return "", "", errors.New("ShortIDFromID is nil")
	}

	shortID := mddb.cfg.ShortIDFromID(id)
	if shortID == "" {
		return "", "", ErrEmptyShortID
	}

	return path, shortID, nil
}

// validateRelPath checks path format: relative, clean, ends with .md, no escape.
func (mddb *MDDB[T]) validateRelPath(path string) error {
	if path == "" {
		return fmt.Errorf("%w: empty", ErrInvalidPath)
	}

	if filepath.IsAbs(path) {
		return fmt.Errorf("%w: absolute path", ErrInvalidPath)
	}

	if filepath.Clean(path) != path {
		return fmt.Errorf("%w: path must be clean", ErrInvalidPath)
	}

	if path == "." || path == ".." {
		return fmt.Errorf("%w: path must be a file", ErrInvalidPath)
	}

	if strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: path escapes data dir", ErrInvalidPath)
	}

	if !strings.HasSuffix(path, ".md") {
		return fmt.Errorf("%w: path must end with .md", ErrInvalidPath)
	}

	absPath := filepath.Join(mddb.dataDir, path)

	rel, err := filepath.Rel(mddb.dataDir, absPath)
	if err != nil {
		return fmt.Errorf("%w: rel: %w", ErrInvalidPath, err)
	}

	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: path escapes data dir", ErrInvalidPath)
	}

	return nil
}
