# Directory layout

An mddb database is a **single existing directory** containing:

```
<data-dir>/
  .wal              # write-ahead log (exists only during an in-flight commit or after a crash)
  .cache            # slotcache (SLC1) index cache
  .locks/           # lock files (writer lock lives here)
  <key>.mddb.md     # document files
  <key>.mddb.md
  ...
```

## Directory creation

- mddb **does not create** the data directory.
- The directory **MUST already exist**.
- mddb creates `.locks/`, `.wal`, and `.cache` inside it as needed.

## File naming

- Documents MUST use the extension `.mddb.md`.
- The **key** is the filename without the `.mddb.md` suffix.

Examples:

- `0001.mddb.md` → key = `0001`
- `2024-01-15-fix-bug.mddb.md` → key = `2024-01-15-fix-bug`

## Lock files

- mddb uses file-based locking (e.g. `flock`) to enforce **single-writer** semantics.
- Lock files MUST live in `.locks/` to avoid modifying the parent directory mtime during lock/unlock.

A typical layout:

```
.locks/
  write.lock
```

Exact lock filenames are an implementation detail, but the lock scope is not: the writer lock covers **WAL replay** and **transactions**.
