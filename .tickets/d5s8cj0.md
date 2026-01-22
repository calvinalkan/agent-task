---
schema_version: 1
id: d5s8cj0
status: open
blocked-by: []
created: 2026-01-22T20:19:20Z
type: feature
priority: 2
---
# slotcache: Design vacuum/rebuild system for reclaiming tombstones and growth

Design and implement a system for slotcache to reclaim slot tombstones (compaction) and optionally grow capacity.

## Background

SLC1/v1 has two kinds of tombstones:
- **Bucket tombstones**: Already handled via rehash at commit time (rehashThreshold=0.25)
- **Slot tombstones**: Append-only slot IDs are never reused; deletes just flip USED=0

Slot tombstones permanently consume capacity. For long-lived caches with churn, this eventually leads to ErrFull even when live_count << slot_capacity.

## Two approaches

### 1. In-place vacuum (preserve inode)
- Rewrite slots so live entries are packed into 0..live_count-1
- Set slot_highwater = live_count
- Rebuild buckets, bucket_tombstones = 0
- **Preserves inode, ownership, permissions**
- **Cannot grow** (file layout is fixed)

Requires spec change: relax "slot IDs append-only / never reused" rule for vacuum operation.

### 2. Rebuild+swap (new inode)
- Create temp file with new capacity
- Copy live entries from old → new
- Invalidate old file (STATE_INVALIDATED)
- rename(temp, path) atomically
- **Supports both compaction and growth**
- **Must explicitly preserve permissions** (stat old → chmod temp before rename)

## Key gotchas to address

### Concurrency / locking
- Vacuum/rebuild needs exclusive writer-style coordination
- Cannot use existing Invalidate() mid-operation (it's terminal and grabs same guard)
- Must be a dedicated exclusive operation that inlines invalidation logic

### Auto-reopen on ErrInvalidated
After rebuild+swap, existing Cache handles have old inode mapped. Options:
1. Caller reopens manually (current spec)
2. Handle/Manager wrapper with atomic.Pointer[*Cache] that auto-reopens on ErrInvalidated
3. True in-place hot-swap (invasive, requires tightening all locking)

**Recommendation**: Handle wrapper pattern - doesn't mutate live Cache; swaps pointer to new instance.

### Options.SlotCapacity compatibility
Currently Open() requires exact SlotCapacity match. For growth to work:
- Change to MinSlotCapacity, or
- SlotCapacity=0 means "accept existing", or
- Add EnforceSlotCapacity bool

### Ordered-keys mode
- In-place vacuum: copying in slot-id order preserves sorted invariant
- Rebuild: same - scan old slots in order, write sequentially

### Crash windows
- Copy → invalidate → rename sequence
- If crash after invalidate but before rename: path points to invalidated file
- Next Open() returns ErrInvalidated; caller/library can recreate
- Acceptable for "throwaway cache" semantics

### Permission preservation (rebuild+swap only)
- Current createNewCache() always uses 0600
- Must fstat old file, apply mode (and best-effort uid/gid) to temp before rename
- Matters if admin pre-created file with specific perms

## Design

## Proposed API

### New Cache method (or standalone function)
```go
// RebuildOptions configures a vacuum/rebuild operation.
type RebuildOptions struct {
    // NewCapacity sets the slot_capacity for the rebuilt cache.
    // If 0, keeps existing capacity (vacuum only).
    NewCapacity uint64
}

// Rebuild compacts the cache, reclaiming slot tombstones.
// If NewCapacity > current capacity, also grows the cache.
// 
// After rebuild, existing Cache handles (including this one) will see
// ErrInvalidated and must reopen. Use Handle for automatic reopen.
func (c *Cache) Rebuild(opts RebuildOptions) error
```

### Handle wrapper for auto-reopen
```go
// Handle wraps a Cache with automatic reopen on ErrInvalidated.
type Handle struct {
    opts     Options
    cur      atomic.Pointer[Cache]
    reopenMu sync.Mutex
    closed   atomic.Bool
}

func OpenHandle(opts Options) (*Handle, error)
func (h *Handle) Get(key []byte) (Entry, bool, error)  // auto-reopen
func (h *Handle) Scan(opts ScanOptions) ([]Entry, error) // auto-reopen
func (h *Handle) Writer() (*Writer, error) // NO auto-reopen
func (h *Handle) Cache() *Cache // escape hatch
func (h *Handle) Close() error
```

Auto-reopen policy: retry at most once per call. Writers never auto-reopen.

### Options change for growth support
```go
type Options struct {
    // SlotCapacity: if > 0, minimum required capacity.
    // Open succeeds if file capacity >= SlotCapacity.
    // If 0, accept any existing capacity.
    SlotCapacity uint64
    // ... rest unchanged
}
```

## Spec changes required

### 003-semantics.md
- Add Vacuum/Rebuild operation under "Write operations"
- Relax "slot IDs append-only" to allow vacuum rewriting
- Document Handle wrapper semantics (optional, may be impl-only)

### 002-format.md  
- No format changes needed (v1 compatible)

## Implementation location
- `pkg/slotcache/rebuild.go` - Rebuild() implementation
- `pkg/slotcache/handle.go` - Handle wrapper
- `pkg/slotcache/open.go` - Relax SlotCapacity validation

## Acceptance Criteria

- [ ] Spec changes reviewed and approved
- [ ] In-place vacuum works: reclaims slot tombstones, preserves inode/perms
- [ ] Rebuild+swap works: supports growth, preserves permissions explicitly  
- [ ] Handle wrapper auto-reopens on ErrInvalidated (reads only)
- [ ] Options.SlotCapacity changed to minimum-or-accept semantics
- [ ] Ordered-keys mode maintains sorted invariant after vacuum/rebuild
- [ ] Crash safety: no partial/corrupt files left on failure
- [ ] Tests: vacuum on full cache, grow, concurrent readers see ErrInvalidated, permission preservation
