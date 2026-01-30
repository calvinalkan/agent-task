# Error Handling Guidelines

This document describes the error handling conventions for the mddb package.

## Gold Standard

```
frontmatter: invalid yaml (doc_id=abc123 doc_path=tickets/abc.md)
           ^               ^             ^
           |               |             |
     lower level       public API    public API
     (subsystem)     (withContext)  (withContext)
```

- **Error message first** - what went wrong, with subsystem prefix
- **Document context at end** - structured fields for programmatic access
- **No duplication** - context added once at the right level

## Two-Level Pattern

### Lower Levels (internal helpers)

Use `fmt.Errorf` with subsystem prefix. No `withContext()`.

```go
// parseIndexable - internal helper
func (mddb *MDDB[T]) parseIndexable(...) (IndexableDocument, error) {
    fm, tail, err := frontmatter.ParseBytes(content, ...)
    if err != nil {
        return IndexableDocument{}, fmt.Errorf("frontmatter: %w", err)
    }
    
    if !hasID {
        return IndexableDocument{}, fmt.Errorf("frontmatter: %w", ErrMissingID)
    }
    
    if len(titleBytes) == 0 {
        return IndexableDocument{}, fmt.Errorf("frontmatter: %w", ErrEmptyTitle)
    }
    
    // validation error from another internal helper
    if err := mddb.validatePath(path); err != nil {
        return IndexableDocument{}, err  // pass through, already has context
    }
    
    return doc, nil
}
```

**Subsystem prefixes:**
- `frontmatter:` - YAML/frontmatter parsing
- `sqlite:` - database operations  
- `fs:` - filesystem operations
- `wal:` - write-ahead log operations
- `lock:` - file locking operations
- `json:` - JSON encoding/decoding

### Public API (exported methods)

Use `withContext()` with id/path at the boundary. Values must be **known and valid**.

Behavior:
- If err is already `*Error`, `withContext()` fills missing fields only (does not overwrite).

```go
// Get - public API
func (mddb *MDDB[T]) Get(ctx context.Context, id string) (*T, error) {
    // id = what caller asked for (known, valid)
    // path = from sqlite lookup (known, valid)
    
    path, err := mddb.lookupPath(id)
    if err != nil {
        return nil, withContext(err, id, "")
    }
    
    doc, err := mddb.readDocumentFile(id, path)
    if err != nil {
        return nil, withContext(err, id, path)
    }
    
    return doc, nil
}
```

## Rules

### 1. Subsystem prefix at the call site

When calling into a subsystem (external package or distinct internal module), add the prefix:

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

### 2. withContext() only at public API boundaries

```go
// ✓ Good - public API adds context
func (mddb *MDDB[T]) Get(id string) (*T, error) {
    doc, err := mddb.internal(id, path)
    if err != nil {
        return nil, withContext(err, id, path)
    }
    return doc, nil
}

// ✗ Bad - internal helper using withContext
func (mddb *MDDB[T]) internal(id, path string) (*T, error) {
    if err != nil {
        return nil, withContext(err, id, path)  // Don't do this
    }
}
```

### 3. withContext uses validated values only

The ID and Path in `withContext()` should be values the caller knows and trusts:

```go
// ✓ Good - id is what caller requested
func (mddb *MDDB[T]) Get(id string) (*T, error) {
    return nil, withContext(err, id, "")  // id came from caller
}

// ✓ Good - path is from trusted source (sqlite index)
path := lookupPathFromIndex(id)
return nil, withContext(err, "", path)

// ✗ Bad - id parsed from untrusted file content
id := parseIDFromFile(content)  // might be invalid/malformed
return nil, withContext(err, id, "")  // Don't use unvalidated values
```

For unvalidated/malformed values, include them in the error **message** instead:

```go
// ✓ Good - malformed value in message, not in structured field
rawID := parseIDFromFile(content)
if !isValid(rawID) {
    return fmt.Errorf("frontmatter: invalid id format %q", rawID)
}
```

### 4. Pass through when context already present

If a lower level already added appropriate context, just return the error:

```go
func (mddb *MDDB[T]) parseDocument(...) (*T, error) {
    indexable, err := mddb.parseIndexable(...)
    if err != nil {
        return nil, err  // Already has subsystem prefix, pass through
    }
    // ...
}
```

### 5. Reindex/scanning - only path available

During scanning, there's no "requested ID" - only the file path:

```go
func (mddb *MDDB[T]) scanFiles(ctx context.Context) ([]*Error, error) {
    var issues []*Error
    
    for _, path := range files {
        doc, err := mddb.parseIndexable(path, content)
        if err != nil {
            // Only path is known/valid - no requested ID exists
            issues = append(issues, &Error{Path: path, Err: err})
        }
    }
    
    return issues, nil
}
```

## Error Type

```go
type Error struct {
    ID   string  // Document ID (validated, from caller or index)
    Path string  // Document path relative to data dir (validated)
    Err  error   // Underlying cause with subsystem prefix
}
```

Output format: `<cause> (doc_id=X doc_path=Y)`

## Examples

### Get by ID - found but parse fails

```go
// User calls: db.Get(ctx, "abc123")
// Path looked up from sqlite: "tickets/abc.md"
// File exists but has invalid YAML

// Lower level returns:
"frontmatter: yaml: line 5: mapping values not allowed"

// Public API wraps:
withContext(err, "abc123", "tickets/abc.md")

// Final output:
"frontmatter: yaml: line 5: mapping values not allowed (doc_id=abc123 doc_path=tickets/abc.md)"
```

### Get by ID - not found

```go
// User calls: db.Get(ctx, "xyz789")  
// sqlite lookup returns no rows

// Lower level returns:
"sqlite: no rows"

// Public API wraps:
withContext(ErrNotFound, "xyz789", "")

// Final output:
"not found (doc_id=xyz789)"
```

### Reindex - file with invalid frontmatter

```go
// Scanning finds file: "tickets/broken.md"
// No requested ID - we're discovering files

// Lower level returns:
"frontmatter: missing id"

// Reindex collects:
&Error{Path: "tickets/broken.md", Err: err}

// Final output:
"frontmatter: missing id (doc_path=tickets/broken.md)"
```

### Create - duplicate ID

```go
// User calls: tx.Create(&doc) where doc.ID() = "abc123"
// Check finds ID already exists

// Public API:
withContext(ErrAlreadyExists, "abc123", "tickets/abc.md")

// Final output:
"already exists (doc_id=abc123 doc_path=tickets/abc.md)"
```

## Checking Errors

Use standard Go error handling:

```go
// Check for sentinel errors
if errors.Is(err, mddb.ErrNotFound) {
    // handle not found
}

// Extract structured context
var mErr *mddb.Error
if errors.As(err, &mErr) {
    fmt.Printf("failed for doc %s at %s\n", mErr.ID, mErr.Path)
}
```
