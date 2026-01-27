# Handling SIGBUS/SIGSEGV from mmap in slotcache (Go)

## Problem
With `mmap`-backed files, certain failures can surface as **SIGBUS** (and sometimes SIGSEGV) when user code touches an address that the OS cannot back with valid storage.

Typical causes:
- underlying file was truncated/overwritten (external corruption / admin action)
- filesystem / device I/O error during page-in
- out-of-space / writeback failure on dirty mmap pages

By default, these signals typically **terminate the process**. For a throwaway cache, we'd prefer to:
1) convert the fault into an error,
2) mark the cache as unusable (“faulted”), and
3) let the caller close/reopen/rebuild.

## Go mechanism: `runtime/debug.SetPanicOnFault`
Go provides:

```go
runtime/debug.SetPanicOnFault(true)
```

This changes the runtime behavior for faults at **non-nil addresses**: instead of crashing, the runtime **panics** with a `runtime.Error`.

Important properties:
- **Per-goroutine**: it only applies to the current goroutine.
- The panic value may have an additional method:

```go
Addr() uintptr
```

which (best-effort) returns the faulting address.

## High-level design for slotcache
### Goals
- Do not turn arbitrary panics (bugs) into errors.
- Only recover from mmap faults that are plausibly caused by the cache mapping.
- After the first fault, avoid touching the mapping again (fail-fast).

### Where to store “faulted” state
Store a `faulted` flag in the per-file registry entry (e.g. `fileRegistryEntry`) rather than only on `*Cache`:
- multiple Cache instances can map the same file
- a fault should poison the file mapping for all instances

### Wrapper pattern (rough sketch)
Every exported operation that touches `c.data` should execute under a wrapper:

```go
// faultAddr is implemented by some runtime panic values when panic-on-fault is enabled.
type faultAddr interface{ Addr() uintptr }

func (c *Cache) withMmapFaultAsErr(op string, fn func() error) (err error) {
    // fail fast if we already observed a fault
    if c.registryEntry.faulted.Load() {
        return fmt.Errorf("%s: cache mapping faulted: %w", op, ErrCorrupt)
    }

    prev := debug.SetPanicOnFault(true)
    defer debug.SetPanicOnFault(prev)

    defer func() {
        r := recover()
        if r == nil {
            return
        }

        // Only convert panics that look like mmap faults.
        fa, ok := r.(faultAddr)
        if !ok {
            panic(r) // real bug
        }

        addr := fa.Addr()

        // Optional: best-effort guard: only treat as mmap fault if addr is within our mapping.
        // (Implementation detail: compare against uintptr(unsafe.Pointer(&c.data[0])) .. +len(c.data)).
        // If not within range, re-panic: it is likely memory corruption.

        c.registryEntry.faulted.Store(true)

        err = fmt.Errorf("%s: mmap fault at %#x: %w", op, addr, ErrCorrupt)
    }()

    return fn()
}
```

Notes:
- We enable panic-on-fault **only** for the duration of the operation and restore the previous setting.
- We only recover panics that provide `Addr() uintptr`.
- Everything else re-panics to avoid masking bugs.

### Making it hard to miss
To ensure all operations are protected:
- add a single internal entrypoint used by all exported methods (Get/Scan/Len/UserHeader/etc.)
- or wrap at the top of each exported method (less ideal; easy to forget)

### What to return
Simplest for a throwaway cache:
- return `ErrCorrupt` (or a dedicated `ErrFaulted` that callers treat as “rebuild”).

After returning the error, the caller can:
- close the cache
- delete/recreate the file
- reopen

### Should we `Close()` automatically on fault?
Possibly, but be careful:
- other goroutines may still be running operations
- `Munmap` while other goroutines might touch `c.data` can cause more faults

A safe minimal strategy is:
- set the `faulted` flag
- return an error
- require caller-driven shutdown/reopen

## Testing ideas (non-exhaustive)
Reliable SIGBUS reproduction is platform/filesystem dependent, but common patterns include:
- map a 0-byte file and access non-zero length
- map a file, then truncate it smaller from another FD, then access the truncated region

Tests should validate:
- wrapper converts the fault to an error
- subsequent operations fail fast via `faulted` flag

## Limitations / caveats
- `Addr()` is best-effort and may not be available on all platforms.
- A genuine memory corruption bug could also fault; the “addr within mapping” check helps avoid hiding those.
- This does not prevent the fault; it only makes it recoverable and allows graceful rebuild behavior.
