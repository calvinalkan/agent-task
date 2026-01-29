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

// errOpt configures an [Error] during construction via [wrap].
type errOpt func(*Error)

// withID attaches a document ID to the error.
//
// Use when:
//   - The document's ID is known (parsed from frontmatter)
//   - Looking up a document by ID (use the requested ID)
//   - The ID helps locate which document failed
//
// Example:
//
//	return wrap(err, withID(doc.ID()))
//	return wrap(ErrNotFound, withID(requestedID))
func withID(id string) errOpt {
	return func(e *Error) { e.ID = id }
}

// withPath attaches a document's relative path to the error.
//
// Use when:
//   - The document's path is known (relative to data directory)
//   - Scanning/parsing files where path is available before ID
//   - The path helps locate which file failed
//
// This should be the relative path (e.g., "tickets/abc.md"), not the
// absolute filesystem path. Filesystem errors (os.PathError) already
// contain the absolute path in their message.
//
// Example:
//
//	return wrap(err, withPath(relPath))
//	return wrap(err, withID(id), withPath(relPath))
func withPath(path string) errOpt {
	return func(e *Error) { e.Path = path }
}

// wrap creates an [*Error] with optional document context.
//
// Use at API boundaries to attach document context to errors. The context
// helps users identify which document caused the failure.
//
// Behavior:
//   - Returns nil if err is nil
//   - Returns err unchanged if it's already [*Error] with no new options
//   - Inherits ID/Path from inner [*Error] when wrapping (can override)
//   - Unwraps inner [*Error] to avoid duplicate suffixes in message
//
// When to use:
//
//	// At public API boundaries - attach context you have
//	func (db *MDDB) Get(id string) (*Doc, error) {
//	    doc, err := db.load(id)
//	    if err != nil {
//	        return nil, wrap(err, withID(id))
//	    }
//	    return doc, nil
//	}
//
//	// When parsing - path first, ID when available
//	func parse(path string, data []byte) error {
//	    id, err := parseID(data)
//	    if err != nil {
//	        return wrap(err, withPath(path))  // ID not yet known
//	    }
//	    if err := validate(data); err != nil {
//	        return wrap(err, withID(id), withPath(path))  // both known
//	    }
//	    return nil
//	}
//
//	// Propagating without adding context (ensures *Error type)
//	if err != nil {
//	    return wrap(err)
//	}
//
// Context inheritance:
//
//	inner := wrap(err, withPath("foo.md"))
//	outer := wrap(inner, withID("abc"))
//	// outer.Error() = "something failed (doc_id=abc doc_path=foo.md)"
//
// Overriding inherited context:
//
//	inner := wrap(err, withID("old"))
//	outer := wrap(inner, withID("new"))
//	// outer.ID = "new" (overridden)
func wrap(err error, opts ...errOpt) error {
	if err == nil {
		return nil
	}

	// Check if err is directly *Error (not buried in chain via fmt.Errorf etc).
	// Use type assertion, not errors.As, to avoid finding *Error through wrappers.
	existing := &Error{}
	isDirectError := errors.As(err, &existing)

	// Don't double-wrap if already our error type and no new context.
	if isDirectError && len(opts) == 0 {
		return existing
	}

	e := &Error{Err: err}

	// Inherit context from direct *Error and unwrap to avoid duplication.
	// We copy the context (ID, Path) and point to its cause, skipping
	// the intermediate *Error wrapper.
	if isDirectError {
		e.ID = existing.ID
		e.Path = existing.Path
		e.Err = existing.Err
	}

	// Apply caller's options (can override inherited values).
	for _, opt := range opts {
		opt(e)
	}

	return e
}
