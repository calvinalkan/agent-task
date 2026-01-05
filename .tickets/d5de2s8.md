---
schema_version: 1
id: d5de2s8
status: closed
closed: 2026-01-05T15:40:25Z
blocked-by: []
created: 2026-01-04T19:24:00Z
type: feature
priority: 1
---
# Write-through cache with directory mtime validation

Replace the current per-file mtime validation with a write-through cache that only checks directory mtime. This eliminates O(n) stat() calls on every `tk ls` while maintaining correctness.

## Problem

Current system does O(n) stat() calls on every `tk ls` to validate cache entries against file mtimes. For 10K tickets, this is ~10ms of syscall overhead. The bitmap index optimization was attempted but added ~1,250 lines of complexity for marginal gain since we still stat() all files.

## Solution

1. **Write-through cache**: Every `tk` command that modifies frontmatter updates the cache atomically
2. **Directory mtime validation**: Only check if directory changed (files added/deleted), not individual files
3. **Trust cache for existing files**: If directory unchanged, cache is authoritative

## Design

### Constraints

- **Frontmatter is only modified through `tk` commands** - this is the key assumption
- **Body content (description, design, etc.) can be edited freely** - doesn't affect cache
- **External frontmatter edits cause stale cache** - documented, acceptable tradeoff

### Cache File Format (v4)

```
┌─────────────────────────────────────────────────────────────────┐
│ HEADER (32 bytes)                                               │
├─────────────────────────────────────────────────────────────────┤
│ [0:4]   magic "TKC1"                                            │
│ [4:6]   version (4)                                             │
│ [6:10]  entry count                                             │
│ [10:32] reserved                                                │
└─────────────────────────────────────────────────────────────────┘

Note: We don't store dir mtime in the cache. Instead, we compare:
  - stat(.tickets/) → dir mtime  
  - stat(.tickets/.cache) → cache file mtime
If dir mtime > cache mtime, files were added/deleted externally → reconcile.
┌─────────────────────────────────────────────────────────────────┐
│ INDEX ENTRIES (56 bytes each, sorted by filename)               │
├─────────────────────────────────────────────────────────────────┤
│ [0:32]  filename (null-padded)                                  │
│ [32:40] mtime (unix nano)                                       │
│ [40:44] data offset                                             │
│ [44:46] data length                                             │
│ [46]    status   (0=open, 1=in_progress, 2=closed)              │
│ [47]    priority (1-4)                         ← NEW            │
│ [48]    type     (0=bug,1=feature,2=task,3=epic,4=chore) ← NEW  │
│ [49:56] reserved                                                │
└─────────────────────────────────────────────────────────────────┘
┌─────────────────────────────────────────────────────────────────┐
│ DATA SECTION (variable length summaries)                        │
├─────────────────────────────────────────────────────────────────┤
│ id, title, created, closed, assignee, blocked-by, path, etc.   │
└─────────────────────────────────────────────────────────────────┘
```

### Read Path (`tk ls`)

```
tk ls --status open --limit 100
              │
              ▼
┌─────────────────────────────────┐
│ 1. Load cache                   │
│    - mmap .cache file           │
│    - if missing/corrupt → COLD  │
└───────────────┬─────────────────┘
                ▼
┌─────────────────────────────────┐
│ 2. Compare mtimes               │
│    stat(.tickets/) vs           │
│    stat(.tickets/.cache)        │
│    - if dir > cache → RECONCILE │
│    - else → TRUST CACHE         │
└───────────────┬─────────────────┘
                ▼
┌─────────────────────────────────┐
│ 3. Scan index entries           │  ← No per-file stat!
│    for i := 0; i < count; i++ { │
│      if status != open: skip    │
│      if matches >= 100: break   │
│      results = append(...)      │
│    }                            │
└───────────────┬─────────────────┘
                ▼
┌─────────────────────────────────┐
│ 4. Load data for matches        │
│    Read title, etc. from data   │
│    section for matched entries  │
└───────────────┬─────────────────┘
                ▼
            RETURN
```

### Write Path (`tk close abc`)

```
tk close abc
      │
      ▼
┌─────────────────────────────────┐
│ 1. Update ticket file           │
│    - WithTicketLock()           │
│    - atomic write               │
└───────────────┬─────────────────┘
                ▼
┌─────────────────────────────────┐
│ 2. Update cache                 │
│    - Load cache                 │
│    - Update entry for abc.md    │
│    - Save cache (atomic)        │
│                                 │
│    If fails → delete cache      │
│    (safe: rebuild on next read) │
└───────────────┬─────────────────┘
                ▼
            RETURN
```

### Cold Start Path (no cache)

```
tk ls (no cache exists)
      │
      ▼
┌─────────────────────────────────┐
│ 1. ReadDir(.tickets/)           │
└───────────────┬─────────────────┘
                ▼
┌─────────────────────────────────┐
│ 2. Parallel parse (worker pool) │
│    16 workers                   │
│    - stat + parse each .md      │
│    - extract frontmatter        │
└───────────────┬─────────────────┘
                ▼
┌─────────────────────────────────┐
│ 3. Build + save cache           │
│    - sort by filename           │
│    - write .cache atomically    │
└───────────────┬─────────────────┘
                ▼
┌─────────────────────────────────┐
│ 4. Apply filters + return       │
└─────────────────────────────────┘
```

### Reconcile Path (directory mtime changed)

When directory mtime differs from cached, files were added or deleted externally:

```
┌─────────────────────────────────┐
│ 1. ReadDir to get current files │
└───────────────┬─────────────────┘
                ▼
┌─────────────────────────────────┐
│ 2. Compare with cache entries   │
│    - New files: parse + add     │
│    - Deleted files: remove      │
│    - Existing: keep (trusted)   │
└───────────────┬─────────────────┘
                ▼
┌─────────────────────────────────┐
│ 3. Save updated cache           │
│    (file mtime auto-updates)    │
└─────────────────────────────────┘
```

### Error Handling

#### Write-through failure modes

The cache update can fail after the file write succeeds. Handle gracefully:

```go
func updateTicketWithCache(ticketDir, path string, updateFn func([]byte) ([]byte, TicketSummary, error)) error {
    var newSummary TicketSummary
    
    // 1. Update ticket file (source of truth)
    err := WithTicketLock(path, func(content []byte) ([]byte, error) {
        newContent, summary, err := updateFn(content)
        if err != nil {
            return nil, err
        }
        newSummary = summary
        return newContent, nil
    })
    if err != nil {
        return err
    }
    
    // 2. Update cache (best effort, fail safe)
    filename := filepath.Base(path)
    if cacheErr := UpdateCacheEntry(ticketDir, filename, newSummary); cacheErr != nil {
        // Cache update failed - try to delete cache
        cachePath := filepath.Join(ticketDir, ".cache")
        if rmErr := os.Remove(cachePath); rmErr != nil && !os.IsNotExist(rmErr) {
            // Both failed - return detailed error with fix instructions
            return fmt.Errorf("updating cache: %w\n\n"+
                "The ticket was saved, but the cache is now stale.\n"+
                "  cache write: %v\n"+
                "  cache delete: %v\n\n"+
                "To fix, manually delete the cache file:\n"+
                "  rm %s\n\n"+
                "It will be rebuilt on the next command.",
                cacheErr, cacheErr, rmErr, cachePath)
        }
        // Cache deleted successfully - will rebuild on next read
    }
    
    return nil
}
```

#### Error message format

Format: `<context>: <what happened>` with optional fix instructions.

```
<doing what>: <what went wrong>

<how to fix>
```

#### Error messages

| Scenario | Output |
|----------|--------|
| Cache load fails (corrupt) | `loading cache: invalid format, rebuilding` |
| Cache update fails, delete succeeds | (silent - rebuilds on next read) |
| Cache update fails, delete fails | See below |
| Reconcile finds invalid file | `parsing .tickets/foo.md: missing required field: status` |
| Dir stat fails | `reading ticket directory: permission denied` |

**Cache update + delete both fail:**
```
updating cache: write failed: disk full

The ticket was saved, but the cache is now stale.
  cache write: disk full
  cache delete: permission denied

To fix, manually delete the cache file:
  rm .tickets/.cache

It will be rebuilt on the next command.
```

### Performance

| Operation | Old (stat all) | New (write-through) |
|-----------|---------------|---------------------|
| `tk ls` (10K tickets) | O(n) stats ~10ms | O(1) dir stat ~0.1ms |
| `tk ls --status open` | O(n) stats ~10ms | O(n) index scan ~0.5ms |
| `tk close foo` | O(1) ~1ms | O(1) + cache write ~5ms |
| Cold start | O(n) parse | O(n) parse (parallel) |

### Cache Size Limits

The binary format has hard limits that must be validated:

| Field | Max | Reason |
|-------|-----|--------|
| Filename | 32 bytes | Fixed-size index slot |
| ID | 255 chars | 1-byte length prefix |
| Type | 255 chars | 1-byte length prefix |
| Created | 255 chars | 1-byte length prefix |
| Closed | 255 chars | 1-byte length prefix |
| Assignee | 255 chars | 1-byte length prefix |
| Title | 65,535 chars | 2-byte length prefix |
| Path | 65,535 chars | 2-byte length prefix |
| BlockedBy count | 255 | 1-byte count |
| Each blocker ID | 255 chars | 1-byte length prefix |
| Total entry data | 65,535 bytes | 2-byte dataLength |

If any limit is exceeded, cache update should fail with a clear error message and the cache should be deleted (forcing rebuild on next read). The ticket file itself is unaffected.

### Invariants

1. **File is source of truth** - cache is optimization only
2. **Write-through** - every tk write updates cache
3. **Fail safe** - if cache update fails, delete cache (rebuild on next read)
4. **Dir mtime guards new/deleted** - only need to reconcile when dir changes
5. **No per-file stat on read** - trust cache for existing files
6. **Validate limits** - reject entries exceeding size limits with clear errors

## Files to Change

### cache_binary.go

1. Bump `cacheVersionNum` from 3 to 4
2. Update `indexEntrySize` from 48 to 56
3. Add priority byte to index entry (byte 47)
4. Add type byte to index entry (byte 48)
5. Add type encoding/decoding helpers (`typeStringToByte`, `typeByteToString`)
6. Add `FilterEntries(status, priority, ticketType, limit, offset int) []int` method
7. Add `UpdateCacheEntry(ticketDir, filename string, summary TicketSummary) error` function
8. Add `DeleteCacheEntry(ticketDir, filename string) error` function
9. Update `writeBinaryCache` to write new fields

### ticket.go

1. Update `ListTicketsOptions` to include `Status`, `Priority`, `Type` filters (already in ls.go)
2. Simplify `ListTickets` to use directory mtime check instead of per-file stats
3. Add `matchesFilter(data []byte, offset int, opts ListTicketsOptions) bool` helper
4. Add `reconcileCache(cache *BinaryCache, entries []os.DirEntry) error` function
5. Add `buildCacheParallel(ticketDir string) (*BinaryCache, error)` function
6. Remove per-file mtime checking logic from read path

### close.go

Add write-through cache update after file update:
```go
// After WithTicketLock succeeds:
if err := UpdateCacheEntry(ticketDir, ticketID+".md", newSummary); err != nil {
    // Handle cache update failure
}
```

### start.go

Add write-through cache update (status: open → in_progress)

### reopen.go

Add write-through cache update (status: closed → open, remove closed timestamp)

### create.go

Add write-through cache update for new ticket

### block.go

Add write-through cache update (blocked-by list change)

### unblock.go

Add write-through cache update (blocked-by list change)

### errors.go

Add new errors:
```go
var (
    errCacheUpdateFailed = errors.New("cache update failed")
    errCacheStale        = errors.New("cache is stale")
)
```

## New Functions

### cache_binary.go

```go
// Type encoding
const (
    typeByteBug     = 0
    typeByteFeature = 1
    typeByteTask    = 2
    typeByteEpic    = 3
    typeByteChore   = 4
)

func typeStringToByte(s string) uint8
func typeByteToString(b uint8) string

// Filtering (scans index entries directly, no data section access)
func (bc *BinaryCache) FilterEntries(status, priority, ticketType int, limit, offset int) []int

// Single-entry update (load, modify, save)
func UpdateCacheEntry(ticketDir, filename string, summary TicketSummary) error

// Single-entry delete (for reconciliation)  
func DeleteCacheEntry(ticketDir, filename string) error

// Get entry by index (for reading matched entries)
func (bc *BinaryCache) GetEntryByIndex(idx int) (filename string, summary TicketSummary)
```

### ticket.go

```go
// Filter matching (works directly on mmap'd bytes)
func matchesFilter(status, priority, ticketType byte, opts ListTicketsOptions) bool

// Cache reconciliation
func reconcileCache(ticketDir string, cache *BinaryCache, entries []os.DirEntry) error

// Parallel cache building
func buildCacheParallel(ticketDir string, entries []os.DirEntry) (*BinaryCache, error)
```

## Tests

### cache_binary_test.go

**Format and encoding:**
1. `TestCacheVersion4Format` - verify header: magic, version=4, entry count
2. `TestIndexEntrySize56` - verify index entries are 56 bytes
3. `TestPriorityByteEncoding` - verify priority 1-4 stored at byte 47
4. `TestTypeByteEncoding` - verify type 0-4 stored at byte 48 (bug=0, feature=1, etc.)
5. `TestBinarySearchWith56ByteEntries` - verify lookup still works with new entry size

**Filtering:**
7. `TestFilterEntriesByStatus` - single status filter
8. `TestFilterEntriesByPriority` - single priority filter
9. `TestFilterEntriesByType` - single type filter
10. `TestFilterEntriesMultiple` - status + priority + type together (AND)
11. `TestFilterEntriesWithLimit` - verify early exit when limit reached
12. `TestFilterEntriesWithOffset` - verify offset skipping
13. `TestFilterEntriesWithLimitAndOffset` - both together
14. `TestFilterEntriesNoMatches` - returns empty, not error

**Single-entry operations:**
15. `TestUpdateCacheEntry` - update existing entry
16. `TestUpdateCacheEntryNewFile` - add new entry to existing cache
17. `TestDeleteCacheEntry` - remove entry from cache
18. `TestUpdateCacheEntryEmptyCache` - update when cache doesn't exist yet

**Migration:**
19. `TestCacheVersion3TrigersRebuild` - v3 cache returns errVersionMismatch
20. `TestCacheCorruptMagic` - invalid magic returns error
21. `TestCacheTruncated` - truncated file returns error
22. `TestCacheEmptyFile` - empty file returns error

**Size limit validation:**
23. `TestCacheRejectsLongFilename` - filename > 32 chars returns error
24. `TestCacheRejectsLongAssignee` - assignee > 255 chars returns error
25. `TestCacheRejectsLongBlockerID` - blocker ID > 255 chars returns error
26. `TestCacheRejectsTooManyBlockers` - > 255 blockers returns error
27. `TestCacheRejectsOversizedEntry` - entry > 65KB returns error

### ticket_test.go

**Directory mtime vs cache mtime:**
1. `TestListTicketsDirMtimeNewerTriggersReconcile` - dir mtime > cache mtime → reconcile
2. `TestListTicketsDirMtimeEqualNoReconcile` - dir mtime == cache mtime → no reconcile
3. `TestListTicketsDirMtimeOlderNoReconcile` - dir mtime < cache mtime → no reconcile (normal after tk write)
4. `TestListTicketsReconcileAddsNewFile` - os.WriteFile new file → detected and added
5. `TestListTicketsReconcileRemovesDeletedFile` - os.Remove file → removed from cache
6. `TestListTicketsReconcileKeepsExisting` - existing files NOT re-parsed during reconcile
7. `TestListTicketsNoPerFileStats` - verify no stat() on individual files when cache valid

**Cold start:**
7. `TestListTicketsColdStartBuildsCache` - cache built when missing
8. `TestListTicketsColdStartParallel` - uses worker pool
9. `TestListTicketsColdStartCorruptCache` - rebuilds on corrupt cache

**Filtering integration:**
10. `TestListTicketsFilterStatus` - status filter works end-to-end
11. `TestListTicketsFilterPriority` - priority filter works
12. `TestListTicketsFilterType` - type filter works
13. `TestListTicketsFilterCombined` - multiple filters together
14. `TestListTicketsFilterWithLimitOffset` - filter + pagination

**Edge cases:**
15. `TestListTicketsEmptyDir` - returns empty, cache has 0 entries
16. `TestListTicketsDirNotExist` - returns empty, no error
17. `TestListTicketsOffsetOutOfBounds` - returns error
18. `TestListTicketsLimitZeroMeansAll` - limit=0 returns all

### Integration tests (command tests)

**Write-through cache updates:**
1. `TestCreateAddsCacheEntry` - new ticket in cache immediately
2. `TestStartUpdatesCacheEntry` - status changes to in_progress
3. `TestCloseUpdatesCacheEntry` - status changes to closed, closed timestamp added
4. `TestReopenUpdatesCacheEntry` - status changes to open, closed timestamp removed
5. `TestBlockUpdatesCacheEntry` - blocked-by list updated
6. `TestUnblockUpdatesCacheEntry` - blocked-by list updated
7. `TestSequentialUpdates` - create → start → close, cache correct after each

**Error handling:**
8. `TestCacheUpdateFailureDeletesCache` - cache deleted on update failure
9. `TestCacheRebuildAfterManualDelete` - cache rebuilds after rm .cache
10. `TestCacheUpdateFailureErrorMessage` - error message format is correct

**External modifications (dir mtime vs cache mtime):**
11. `TestExternalFileAddition` - os.WriteFile new .md → dir mtime updates → next ls parses and adds
12. `TestExternalFileDeletion` - os.Remove .md → dir mtime updates → next ls removes from cache
13. `TestExternalFileRename` - os.Rename .md → dir mtime updates → next ls detects
14. `TestDirMtimeUnchangedAfterContentEdit` - os.WriteFile existing → dir mtime unchanged
15. `TestCacheMtimeUpdatesAfterReconcile` - after reconcile, cache mtime >= dir mtime

**Concurrency:**
15. `TestConcurrentLs` - two parallel tk ls don't corrupt
16. `TestConcurrentWrites` - two parallel tk close (different tickets) don't corrupt
17. `TestLsDuringWrite` - tk ls during tk close doesn't corrupt

**Repair command:**
18. `TestRepairRebuildCache` - `tk repair --rebuild-cache` forces rebuild
19. `TestRepairRebuildCacheFixesStale` - stale cache fixed after rebuild

### Benchmark tests

1. `BenchmarkListTicketsWarmCache` - warm cache read, target <1ms for 10K
2. `BenchmarkListTicketsWarmCacheWithFilter` - filtered read, target <2ms for 10K
3. `BenchmarkListTicketsColdStart` - parallel build, target <500ms for 10K
4. `BenchmarkCacheUpdateSingleEntry` - write-through overhead, target <10ms
5. `BenchmarkFilterScan` - raw index scan speed

## Acceptance Criteria

### Functional

**Filtering:**
- [ ] `tk ls --status open` returns only open tickets
- [ ] `tk ls --priority 1` returns only priority 1 tickets
- [ ] `tk ls --type bug` returns only bug tickets
- [ ] `tk ls --status open --priority 1` returns tickets matching BOTH (AND logic)
- [ ] `tk ls --status open --type bug --priority 2` all three filters work together
- [ ] `tk ls --status open --limit 10` stops after 10 matches
- [ ] `tk ls --status open --offset 5` skips first 5 matches
- [ ] `tk ls --status open --limit 10 --offset 5` combines correctly

**Write-through (every command updates cache):**
- [ ] `tk create` adds new entry to cache
- [ ] `tk start` updates cache entry (status: open → in_progress)
- [ ] `tk close` updates cache entry (status: in_progress → closed, adds closed timestamp)
- [ ] `tk reopen` updates cache entry (status: closed → open, removes closed timestamp)
- [ ] `tk block` updates cache entry (blocked-by list)
- [ ] `tk unblock` updates cache entry (blocked-by list)
- [ ] Cache entry update is atomic (no partial writes)

**Directory mtime validation:**
- [ ] `tk ls` compares dir mtime vs cache file mtime (not individual file mtimes)
- [ ] `tk ls` does NOT stat individual files when cache is warm and dir unchanged
- [ ] When dir mtime > cache mtime, cache is reconciled (new files added, deleted files removed)
- [ ] Reconciliation does NOT re-parse existing files (trusts cache)
- [ ] After reconciliation, cache file mtime is naturally updated (by writing cache)

**Cache lifecycle:**
- [ ] Cache is rebuilt from scratch if missing
- [ ] Cache is rebuilt from scratch if corrupt/invalid magic
- [ ] Cache is rebuilt from scratch if version mismatch (v3 → v4)
- [ ] `tk repair --rebuild-cache` forces full cache rebuild

**Cache format (v4):**
- [ ] Index entry is 56 bytes (was 48)
- [ ] Priority byte at offset 47 stores values 1-4
- [ ] Type byte at offset 48 stores values 0-4 (bug=0, feature=1, task=2, epic=3, chore=4)
- [ ] Binary search works correctly with 56-byte entries
- [ ] Dir mtime vs cache file mtime comparison works correctly

**Invariant: file is source of truth:**
- [ ] If cache entry is wrong, `tk show <id>` still shows correct file content
- [ ] After `tk repair --rebuild-cache`, cache matches actual files

### Error Handling

- [ ] Error format: `<context>: <what happened>` (no "error:" prefix)
- [ ] If cache update fails but cache delete succeeds: silent (cache rebuilds on next read)
- [ ] If cache update fails and cache delete fails: shows context, both errors, and fix instructions
- [ ] If cache is corrupt: `loading cache: invalid format, rebuilding`
- [ ] If ticket file parse fails during reconcile: `parsing .tickets/foo.md: <details>`

### Performance

- [ ] `tk ls` on 10K tickets with warm cache: <1ms (excluding output)
- [ ] `tk ls --status open` on 10K tickets: <2ms
- [ ] Cache update overhead per write command: <10ms
- [ ] Cold start on 10K tickets: <500ms (parallel parse)

### Edge Cases

**Basic:**
- [ ] Empty ticket directory works (returns empty, cache has 0 entries)
- [ ] Single ticket works
- [ ] Ticket directory doesn't exist (returns empty, no error)

**Filtering results:**
- [ ] All tickets match filter → returns all
- [ ] No tickets match filter → returns empty list (not error)
- [ ] Offset >= total matching count → returns error
- [ ] Offset >= total count (no filter) → returns error
- [ ] Limit 0 means no limit (returns all matches)

**External modifications (between tk commands):**
- [ ] File added externally → dir mtime > cache mtime → reconcile adds file
- [ ] File deleted externally → dir mtime > cache mtime → reconcile removes file
- [ ] File content edited externally → dir mtime unchanged → cache unaffected
- [ ] Frontmatter edited externally → dir mtime unchanged → cache stale (documented, acceptable)

**Concurrency:**
- [ ] Two concurrent `tk ls` don't corrupt cache
- [ ] Two concurrent `tk close` on different tickets don't corrupt cache
- [ ] `tk close` during `tk ls` doesn't corrupt cache

**File edge cases:**
- [ ] Very long title (>255 chars) handled correctly (uses 2-byte prefix)
- [ ] Ticket with many blockers (>10) handled correctly
- [ ] Filename at max length (32 chars) works
- [ ] Non-.md files in directory are ignored
- [ ] Hidden files (._foo.md) are ignored
- [ ] Subdirectories in .tickets/ are ignored

**Cache size limits (must validate and error clearly):**
- [ ] Filename > 32 chars → `caching ticket: filename too long (max 32 chars): <name>`
- [ ] Assignee > 255 chars → `caching ticket: assignee too long (max 255 chars)`
- [ ] Blocker ID > 255 chars → `caching ticket: blocker ID too long (max 255 chars): <id>`
- [ ] > 255 blockers → `caching ticket: too many blockers (max 255)`
- [ ] Entry data > 65KB → `caching ticket: entry too large (max 65535 bytes)`

**Cache file edge cases:**
- [ ] Cache file exists but is empty → treated as corrupt, rebuild
- [ ] Cache file exists but is truncated → treated as corrupt, rebuild
- [ ] Cache file has wrong permissions → appropriate error message
- [ ] .tickets/.cache.lock left behind → handled gracefully

## Migration

- Cache v3 will trigger automatic rebuild to v4
- No manual migration needed
- First `tk ls` after upgrade will be slower (cold start)