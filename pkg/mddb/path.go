package mddb

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrInvalidPath indicates a document path failed validation.
var ErrInvalidPath = errors.New("invalid path")

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
