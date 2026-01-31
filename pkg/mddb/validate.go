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
	errEmptyID      = errors.New("id is empty")
	errEmptyShortID = errors.New("short id is empty")
	errEmptyPath    = errors.New("derived path is empty")
	errEmptyTitle   = errors.New("title is empty")
)

// validateDocument checks a Document before Create/Update.
// Returns validated ID and path (path only when valid).
func (mddb *MDDB[T]) validateDocument(d Document) (string, string, error) {
	id := d.ID()
	if id == "" {
		return "", "", errEmptyID
	}

	if d.Title() == "" {
		return id, "", errEmptyTitle
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
func (mddb *MDDB[T]) deriveAndValidate(id string, fsPath []byte) (string, string, error) {
	if mddb.cfg.RelPathFromID == nil {
		return "", "", errors.New("Config.RelPathFromID is nil")
	}

	path := mddb.cfg.RelPathFromID(id)
	if path == "" {
		return "", "", errEmptyPath
	}

	if err := mddb.validateRelPath(path); err != nil {
		return "", "", err
	}

	// Compare without allocating: unsafe.String creates a view, not a copy.
	// Safe because expectPath outlives this comparison and result isn't stored.
	if len(fsPath) > 0 && path != unsafe.String(unsafe.SliceData(fsPath), len(fsPath)) {
		return "", "", fmt.Errorf("path mismatch: derived %q", path)
	}

	if mddb.cfg.ShortIDFromID == nil {
		return "", "", errors.New("Config.ShortIDFromID is nil")
	}

	shortID := mddb.cfg.ShortIDFromID(id)
	if shortID == "" {
		return "", "", errEmptyShortID
	}

	return path, shortID, nil
}

// validateRelPath checks path format: relative, clean, ends with .md, no escape.
func (mddb *MDDB[T]) validateRelPath(path string) error {
	if path == "" {
		return errors.New("is empty")
	}

	if filepath.IsAbs(path) {
		return errors.New("must be relative")
	}

	if filepath.Clean(path) != path {
		return errors.New("must be clean")
	}

	if path == "." || path == ".." {
		return errors.New("must be a file")
	}

	if strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return errors.New("escapes data dir")
	}

	if !strings.HasSuffix(path, ".md") {
		return errors.New("must end with .md")
	}

	absPath := filepath.Join(mddb.dataDir, path)

	rel, err := filepath.Rel(mddb.dataDir, absPath)
	if err != nil {
		return fmt.Errorf("fs: %w", err)
	}

	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("escapes data dir")
	}

	return nil
}
