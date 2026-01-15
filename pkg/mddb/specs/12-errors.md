# Error model

mddb exposes a small set of sentinel errors for classification.

Implementations MAY wrap these errors with additional context.

Callers MUST use `errors.Is(err, ErrX)` to classify.

## Sentinel errors (v1)

### WAL / recovery

- `ErrWALCorrupt`: `.wal` exists but cannot be verified or replayed.

### Transactions / locking

- `ErrTxClosed`: `Commit()`/`Abort()` already called.
- `ErrTxActive`: `DB.Close()` called with an active transaction.
- `ErrLockTimeout`: could not acquire the writer lock within `LockTimeout`.

### Validation

- `ErrNotFound`: key does not exist (`Update`, `Delete`).
- `ErrExists`: key already exists (`Create`).
- `ErrInvalidKey`: invalid key (see [document format](document-format.md)).
- `ErrFieldValue`: frontmatter does not fit schema.

### Filtering

- `ErrOffsetOutOfBounds`: `FilterOpts.Offset` exceeds result count.

## Cache errors

Cache errors are generally **not user-facing**.

If `.cache` is missing/corrupt/incompatible, mddb SHOULD rebuild it automatically.

If rebuild fails due to doc validation errors, return `ErrFieldValue`.

If rebuild fails due to filesystem errors, those errors MAY be returned (wrapped).

## Filesystem errors

mddb MAY return wrapped OS errors such as:

- `os.ErrNotExist` (data directory missing)
- `os.ErrPermission`

## slotcache errors

slotcache may return errors like `ErrCorrupt`, `ErrBusy`, `ErrIncompatible`, etc.

mddb SHOULD treat these as cache-layer errors and attempt a rebuild unless:

- the error indicates the cache is actively being written by another process (should not happen if mddb writer lock is respected)
- filesystem operations fail

If a rebuild cannot resolve the issue, the underlying slotcache error MAY be wrapped and returned.
