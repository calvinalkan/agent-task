# Document format

Each mddb document is a UTF-8 text file with extension `.mddb.md`:

```text
---
id: <document-id>
<other frontmatter fields>
---
<content>
```

## YAML subset

mddb uses a **restricted subset of YAML** to enable simple, fast parsing. Full YAML is not supported in v1.

### Supported

- **Scalars**: strings, integers, booleans
- **Flat string lists**: sequences of strings
- **Flat objects**: single-level maps with string keys and scalar values

### Not supported

- Nested objects (objects inside objects)
- Lists of objects
- Lists of integers or booleans
- YAML anchors and aliases (`&`, `*`)
- YAML tags (`!!str`, `!!int`, etc.)

### Example

```yaml
---
id: task-001

# Scalars
title: Fix login bug
priority: 3
done: false
created_at: 2024-01-15T10:30:00Z

# Flat string list
tags:
  - urgent
  - backend

assignees:
  - alice
  - bob

# Flat object (string → scalar)
metadata:
  env: production
  region: us-east
  version: 42
---

Content goes here. mddb does not interpret this section.
```

## YAML frontmatter

Frontmatter is delimited by `---` fences:

- The file MUST start with a line that is exactly `---`.
- Frontmatter MUST end with a line that is exactly `---`.
- The YAML between the fences MUST parse to a mapping.

If frontmatter is missing or cannot be parsed, the document is invalid.

### Reserved field: `id`

The frontmatter key `id` is reserved.

- mddb writers MUST inject `id: <value>` when writing a document.
- Caller-provided frontmatter MUST NOT include the key `id`.
- During reads and rebuilds, mddb MUST parse and validate `id`.

### Indexed vs freeform fields

- **Indexed fields**: declared in the index schema, must be scalars or flat string lists (see [Index schema](006-index-schema.md))
- **Freeform fields**: any other keys, may use the full YAML subset (including flat objects)

## Content

Everything after the FIRST closing `---` fence is content. mddb stores it verbatim and does not interpret it.

## Canonical serialization

To support idempotent WAL replay and stable Git diffs, mddb writers MUST serialize documents deterministically:

1. Emit `---\n`
2. Emit `id` first, then other keys in deterministic order (e.g., lexicographic)
3. Emit `---\n`
4. Emit content exactly as provided

The exact YAML formatting (indentation, quoting) is implementation-defined but MUST be deterministic for the same logical mapping.

## Validation summary

A document is valid for indexing if:

- Frontmatter fences are present and well-formed
- Frontmatter parses within the YAML subset
- `id` exists and validates under the configured ID rules
- All indexed fields validate under the index schema

Implementations SHOULD enforce a maximum scan length for finding the closing fence.
