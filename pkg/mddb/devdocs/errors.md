# Error Handling Guidelines

This document describes the error handling conventions for the mddb package.

## Gold Standard

```
read document: fs: open: no such file (doc_id=abc123 doc_path=tickets/abc.md)
      ^        ^                              ^             ^
      |        |                              |             |
    verb    subsystem                     public API    public API
  (caller)  (external)                   (withContext)  (withContext)
```

```
recover wal: replay ops: sqlite: UNIQUE constraint failed
     ^           ^          ^
     |           |          |
   verb        verb      subsystem
 (caller)    (caller)    (external)
```

- **Verb first** - what operation was being attempted
- **Subsystem prefix** - which external system failed
- **Document context at end** - structured fields for programmatic access
- **No duplication** - context added once at the right level

## Core Principles

### 1. Callers wrap what they call (verb prefix)

When calling an internal function, wrap with a verb describing what you were doing:

```go
// ✓ Good - caller describes what it was doing
doc, err := mddb.readDocumentFile(id, path)
if err != nil {
    return nil, fmt.Errorf("read document: %w", err)
}

// ✓ Good - verb describes the action
doc, err := mddb.parseDocument(relPath, data, mtime, size, expectedID)
if err != nil {
    return nil, fmt.Errorf("parse document: %w", err)
}

// ✓ Good - helper calling helper, no wrap needed if it doesn't add useful context
func (mddb *MDDB[T]) fillBatchUpsertArgs(docs []IndexableDocument, dest []any) error {
    for i := range docs {
        err := mddb.fillDocArgs(&docs[i], dest[i*colCount:(i+1)*colCount])
        if err != nil {
            return err  // pass through, wrapping adds no useful info
        }
    }
    return nil
}
```

### 2. Children never add their own name

A function should never prefix errors with its own name - that's the caller's job.

### 3. First function to receive data adds context

When data (like a path, ID, etc.) is passed from parent to child, only the **first function** to receive it should add it to errors. Children should not re-add context they received from their caller:

```go
// ✓ Good - parent adds path context, child does not repeat it
func (mddb *MDDB[T]) deriveAndValidate(id string, ...) (string, string, error) {
    path := mddb.cfg.RelPathFromID(id)
    if err := mddb.validateRelPath(path); err != nil {
        return "", "", fmt.Errorf("invalid path %q: %w", path, err)  // first to have path
    }
}

func (mddb *MDDB[T]) validateRelPath(path string) error {
    if path == "" {
        return errors.New("empty")  // no path in message - caller adds it
    }
    if !strings.HasSuffix(path, ".md") {
        return errors.New("must end with .md")  // no path - caller adds it
    }
}

// ✗ Bad - child repeats path that parent will also add
func (mddb *MDDB[T]) validateRelPath(path string) error {
    if path == "" {
        return fmt.Errorf("path %q: empty", path)  // parent already adds this!
    }
}
```

**Exception:** External libraries (stdlib, third-party) may already include context like paths in their errors. Don't duplicate it:

```go
// ✓ Good - os.ReadFile already includes path in error, don't add again
data, err := os.ReadFile(absPath)
if err != nil {
    return fmt.Errorf("fs: %w", err)  // just subsystem prefix, path is in err
}

// ✗ Bad - duplicates path that os.ReadFile already included
data, err := os.ReadFile(absPath)
if err != nil {
    return fmt.Errorf("fs: read %s: %w", absPath, err)  // path appears twice!
}
```

```go
// ✗ Bad - function adding its own name
func (mddb *MDDB[T]) readDocumentFile(...) (*T, error) {
    if err != nil {
        return nil, fmt.Errorf("readDocumentFile: %w", err)
    }
}

// ✗ Bad - "open" is semantically the function's name (openSqlite)
func openSqlite(ctx context.Context, path string) (*sql.DB, error) {
    db, err := sql.Open("sqlite3", path)
    if err != nil {
        return nil, fmt.Errorf("sqlite: open: %w", err)
    }
    
    // ✓ Good - "apply pragmas" is a distinct operation within the function
    _, err = db.ExecContext(ctx, "PRAGMA busy_timeout = ...")
    if err != nil {
        return nil, fmt.Errorf("sqlite: apply pragmas: %w", err)
    }
}

// ✓ Good - function returns error with subsystem prefix, caller adds verb
func (mddb *MDDB[T]) readDocumentFile(...) (*T, error) {
    data, err := mddb.fs.ReadFile(absPath)
    if err != nil {
        return nil, fmt.Errorf("fs: %w", err)  // subsystem prefix only
    }
}
```

### 4. External calls get subsystem prefix

When calling external packages or distinct subsystems, add a subsystem prefix:

```go
// Calling frontmatter package
fm, err := frontmatter.ParseBytes(content)
if err != nil {
    return fmt.Errorf("frontmatter: %w", err)
}

// Calling sqlite
row := db.QueryRowContext(ctx, query, id)
if err := row.Scan(&path); err != nil {
    return fmt.Errorf("sqlite: %w", err)
}

// Calling filesystem
data, err := os.ReadFile(path)
if err != nil {
    return fmt.Errorf("fs: %w", err)
}
```

**Subsystem prefixes:**
- `frontmatter:` - YAML/frontmatter parsing
- `sqlite:` - database operations  
- `fs:` - filesystem operations
- `wal:` - write-ahead log operations
- `lock:` - file locking operations
- `json:` - JSON encoding/decoding

Only use a subsystem prefix when you actually called that subsystem:

```go
// ✗ Bad - no sqlite call was made, this is just validation
if rows <= 0 {
    return nil, errors.New("sqlite: delete rows must be positive")
}

// ✓ Good - validation error, no prefix needed
if rows <= 0 {
    return nil, errors.New("delete rows must be positive")
}
```

### 5. withContext() only at public API boundaries

`withContext()` adds document ID and path. Only use it at public API boundaries, and only when you have valid values to add:

```go
// ✓ Good - public API adds document context
func (mddb *MDDB[T]) Get(ctx context.Context, id string) (*T, error) {
    doc, err := mddb.readDocumentFile(id, path)
    if err != nil {
        return nil, withContext(fmt.Errorf("read document: %w", err), id, path)
    }
    return doc, nil
}

// ✗ Bad - withContext with empty strings (pointless)
return nil, withContext(err, "", "")  // Don't do this

// ✗ Bad - internal helper using withContext
func (mddb *MDDB[T]) internal(...) (*T, error) {
    return nil, withContext(err, id, path)  // Don't do this
}
```

## Complete Example

Here's how errors flow through the call stack:

```go
// Public API - adds document context
func (mddb *MDDB[T]) Get(ctx context.Context, id string) (*T, error) {
    // ... lookup path from sqlite ...
    
    doc, err := mddb.readDocumentFile(id, path)
    if err != nil {
        // Wrap with verb, then add document context
        return nil, withContext(fmt.Errorf("read document: %w", err), id, path)
    }
    return doc, nil
}

// Internal helper - uses subsystem prefixes, no withContext
func (mddb *MDDB[T]) readDocumentFile(expectedID string, relPath string) (*T, error) {
    absPath := filepath.Join(mddb.dataDir, relPath)

    info, err := mddb.fs.Stat(absPath)
    if err != nil {
        return nil, fmt.Errorf("fs: %w", err)  // subsystem prefix
    }

    data, err := mddb.fs.ReadFile(absPath)
    if err != nil {
        return nil, fmt.Errorf("fs: %w", err)  // subsystem prefix
    }

    doc, err := mddb.parseDocument(relPath, data, mtimeNS, sizeBytes, expectedID)
    if err != nil {
        return nil, fmt.Errorf("parse document: %w", err)  // verb prefix for internal call
    }

    return doc, nil
}

// Lower-level helper - subsystem prefixes only
func (mddb *MDDB[T]) parseDocument(...) (*T, error) {
    fm, tail, err := frontmatter.ParseBytes(content, ...)
    if err != nil {
        return nil, fmt.Errorf("frontmatter: %w", err)
    }
    
    doc, err := mddb.cfg.DocumentFrom(...)
    if err != nil {
        return nil, fmt.Errorf("DocumentFrom: %w", err)
    }
    
    return doc, nil
}
```

**Final error output:**
```
read document: parse document: frontmatter: yaml: line 5: invalid syntax (doc_id=abc123 doc_path=tickets/abc.md)
```

### Operational errors (no document context)

```go
// Store is closed - no document involved
return zero, ErrClosed

// Lock acquisition failed - no document involved  
return zero, err  // err already has "lock: write: ..." prefix

// Context canceled - no document involved
return zero, fmt.Errorf("canceled: %w", context.Cause(ctx))
```

## Joining Multiple Errors

Use `errors.Join` when a path can produce multiple errors (e.g., partial cleanup, multiple validations):

```go
// ✓ Good - collecting errors during cleanup
func (mddb *MDDB[T]) Close() error {
    var sqlErr, walErr error
    
    if mddb.sql != nil {
        sqlErr = mddb.sql.Close()
    }
    if mddb.wal != nil {
        walErr = mddb.wal.Close()
    }
    
    return errors.Join(sqlErr, walErr)  // nil if both nil
}

// ✓ Good - operation failed, cleanup also failed
result, err := doOperation()
if err != nil {
    cleanupErr := cleanup()
    return errors.Join(err, cleanupErr)
}
```

## Sentinel Errors

Only use sentinel errors when the caller can take **specific action** based on them:

```go
// ✓ Good - caller can handle "not found" differently (e.g., create new)
var ErrNotFound = errors.New("not found")

// ✓ Good - caller can handle "closed" differently (e.g., reopen)
var ErrClosed = errors.New("closed")

// ✓ Good - caller can handle "already exists" differently (e.g., update instead)
var ErrAlreadyExists = errors.New("already exists")

// ✗ Bad - caller can't do anything useful with this
var ErrInvalidYAML = errors.New("invalid yaml")  // Just let the error bubble up

// ✗ Bad - too specific, caller can't act on it
var ErrMissingTitle = errors.New("missing title")  // Use fmt.Errorf instead
```

**Rules:**
- Never export sentinel errors unless callers need `errors.Is()` checks
- Prefer `fmt.Errorf("frontmatter: %w", err)` over custom sentinels for parse/validation errors
- If you can't imagine a caller's `if errors.Is(err, X)` block doing something useful, don't create the sentinel

## Checking Errors

Use standard Go error handling:

```go
// Check for sentinel errors (only when you can act on them)
if errors.Is(err, mddb.ErrNotFound) {
    // handle not found - e.g., create new document
}

if errors.Is(err, mddb.ErrClosed) {
    // handle closed - e.g., reopen store
}
```
