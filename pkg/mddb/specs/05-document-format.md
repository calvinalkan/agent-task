# Document format

Each document is a UTF-8 text file with extension `.mddb.md`.

## Structure

A document has two logical parts:

1. **YAML frontmatter** (optional, but usually present)
2. **Content** (markdown by convention, but treated as opaque text)

Example:

```markdown
---
status: open
priority: 1
blocked: false
tags: [bug, urgent]
custom_field: anything
---
# Title

Content here...
```

## Frontmatter

### Delimiters

- Frontmatter is delimited by `---` lines.
- If the file begins with `---` on the first line, mddb treats everything up to the next `---` line as YAML.
- If the file does **not** begin with `---`, the frontmatter is treated as empty.

### Parsing

- Frontmatter is YAML only (v1).
- Parsed with `gopkg.in/yaml.v3` into a `map[string]any`.
- mddb does not require that all frontmatter keys are indexed.
- Unknown/unindexed fields are preserved on read, and SHOULD be preserved on write.

### Updates and merge semantics

For `Update(key, patch)`:

- Updates are a **shallow merge** at the top-level YAML mapping.
- For each top-level key `k` in `patch.Frontmatter`:
  - if `patch.Frontmatter[k] == nil`: delete `k` from the stored frontmatter
  - else: set/replace `k` with the provided value

Nested objects are replaced, not deep-merged.

## Content

- Content is everything after the closing frontmatter fence (or the whole file if there is no frontmatter).
- Content is stored only in the `.mddb.md` file.
- The cache does not store content.

## Keys

A key is derived from the filename: `<key>.mddb.md`.

Key validity rules (v1):

- MUST not be empty.
- MUST be **<= 64 bytes** when encoded as UTF-8.
- MUST NOT contain path separators (`/` or `\`).
- MUST NOT contain NUL (`\x00`).

Keys are case-sensitive and are treated as opaque bytes beyond the above constraints.

## Canonicalization

mddb MAY rewrite documents (e.g., on `Update`) and therefore does not guarantee preservation of:

- YAML comments
- YAML key ordering
- whitespace or formatting

The logical YAML mapping and content bytes are preserved.
