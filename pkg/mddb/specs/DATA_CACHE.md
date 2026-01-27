# Optional data caching (non-normative)

This document describes an **optional** data-caching layer that can sit beside the index schema. It is **non-normative** and represents one possible API design.

## Overview

In addition to the index (for filtering), an implementation may cache **derived data** per document. This avoids file I/O when you need more than just the indexed fields.

A typical API exposes a `DataSchema` that extracts data from frontmatter and content.

## Example

```go
// Define data struct
type TicketData struct {
    Title   string
    Preview string
    Author  string
}

// Create data schema with extraction function
var dataSchema = mddb.DataSchema(func(frontmatter map[string]any, content string) TicketData {
    title := ""
    if i := strings.Index(content, "\n"); i > 0 {
        title = strings.TrimPrefix(content[:i], "# ")
    }
    author, _ := frontmatter["author"].(string)

    return TicketData{
        Title:   title,
        Preview: content[:min(200, len(content))],
        Author:  author,
    }
})

// Open with data schema
db, _ := mddb.Open(".tickets", indexSchema, dataSchema)

// Filter returns cached data - no file I/O
matches, _ := db.Filter(opts, Status.Eq("open"))
for _, m := range matches {
    fmt.Println(m.Data.Title)    // cached
    fmt.Println(m.Data.Preview)  // cached
}

// Get() still reads full file
entry, _, _ := db.Get("0001")
fmt.Println(entry.Content)  // full content from disk
```

## Serialization

One approach is to serialize cached data via `encoding/gob`. The data struct can contain any gob-encodable types (strings, slices, maps, nested structs).

## Disabling data caching

If data caching isn’t needed, callers can pass `nil` (or omit the data schema entirely):

```go
db, _ := mddb.Open(".tickets", indexSchema, nil)
// or
db, _ := mddb.Open(".tickets", indexSchema)
```
