package mddb

import (
	"errors"
	"strings"
)

// Error is the uniform error type returned by all public mddb APIs.
//
// Provides structured document context (ID, Path) appended to error messages.
// The underlying error message appears first, followed by document context:
//
//	read /data/docs/foo.md: permission denied (doc_id=abc123 doc_path=docs/foo.md)
//
// Use [errors.As] to extract structured fields:
//
//	var mErr *mddb.Error
//	if errors.As(err, &mErr) {
//	    fmt.Printf("failed for document %s at %s\n", mErr.ID, mErr.Path)
//	}
//
// Use [errors.Is] to check for sentinel errors:
//
//	if errors.Is(err, mddb.ErrNotFound) { ... }
type Error struct {
	// ID is the document identifier. May be:
	//   - The document's actual ID (from frontmatter) when known
	//   - The requested/expected ID (for lookups that failed)
	ID string

	// Path is the document's relative path (relative to data directory).
	// This is NOT the absolute filesystem path - that appears in the
	// underlying error (e.g., from os.PathError).
	Path string

	// Err is the underlying cause.
	Err error
}

// Error formats as "<cause> (doc_id=X doc_path=Y)".
func (e *Error) Error() string {
	if e == nil {
		return ""
	}

	cause := e.cause()
	suffix := e.suffix()

	if suffix == "" {
		return cause
	}

	if cause == "" {
		return suffix
	}

	return cause + " " + suffix
}

// String implements fmt.Stringer.
func (e *Error) String() string {
	return e.Error()
}

// Unwrap returns the underlying error for use with [errors.Is] and [errors.As].
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.Err
}

// suffix builds the "(doc_id=X doc_path=Y)" portion.
func (e *Error) suffix() string {
	var parts []string

	if e.ID != "" {
		parts = append(parts, "doc_id="+e.ID)
	}

	if e.Path != "" {
		parts = append(parts, "doc_path="+e.Path)
	}

	if len(parts) == 0 {
		return ""
	}

	return "(" + strings.Join(parts, " ") + ")"
}

// cause returns the underlying error message.
func (e *Error) cause() string {
	if e.Err == nil {
		return ""
	}

	return e.Err.Error()
}

// withContext attaches document context at API boundaries and returns *Error.
// If err is already *Error, missing fields are filled in-place (existing values preserved).
func withContext(err error, id string, path string) error {
	if err == nil {
		return nil
	}

	existing := &Error{}
	if errors.As(err, &existing) {
		if existing.ID == "" && id != "" {
			existing.ID = id
		}

		if existing.Path == "" && path != "" {
			existing.Path = path
		}

		return existing
	}

	return &Error{ID: id, Path: path, Err: err}
}
