# slotcache Implementation Plan

## Package Structure

```
pkg/slotcache/
├── api.go               # public API: Cache/Writer interfaces, Options, Entry, etc.
├── api_stub.go          # //go:build !slotcache_impl (Open panics)
├── cache_impl.go        # //go:build slotcache_impl (cache struct implementation)
├── writer_impl.go       # //go:build slotcache_impl (writer struct implementation)
├── errors.go            # error codes
├── model/
│   └── model.go
├── specs/
│   └── slotcachev1.md
└── *_test.go            # //go:build slotcache_impl
```

## Design Pattern

Public interfaces with private implementations:

- `Cache` and `Writer` are **interfaces** in `api.go`
- `cache` and `writer` are **unexported structs** in `*_impl.go`
- `Open()` returns `Cache` interface (concrete `*cache` under the hood)
- Build tags control stub vs real implementation (avoids import cycles)

---

## Phase 0: Compile

Make the implementation compile. All methods can panic or return stub errors.

```bash
go test -tags=slotcache_impl ./pkg/slotcache/...
# Must compile
```

- [x] Create `cache_impl.go` with Cache methods
- [x] Create `writer_impl.go` with Writer methods
- [x] All methods panic or return stub error
- [x] Compiles

---

## Phase 1: In-Memory Implementation

Implement a fully working cache using only in-memory state. No file I/O yet.
The model already defines correct semantics—match its behavior exactly.
Reopen must work (via fake persistence or real persistence).

```bash
go test -tags=slotcache_impl -run Test_Slotcache_Matches_Model_When_Random_Operations_Applied ./pkg/slotcache/...
# PASS
```

### 1a: Data Structures

- [x] `slotRecord{key, isLive, revision, index}`
- [x] `bufferedOp{isPut, key, revision, index}`
- [x] `cache{file, isClosed, activeWriter}` (unexported, implements Cache interface)
- [x] `writer{cache, bufferedOps, isClosed}` (unexported, implements Writer interface)

### 1b: Cache Lifecycle

- [x] `Open(opts)` → validate, create empty
- [x] `Close()` → ErrBusy if writer active, set closed
- [x] `Len()` → count live, ErrClosed if closed

### 1c: Writer Lifecycle

- [x] `BeginWrite()` → ErrBusy if exists, ErrClosed if closed
- [x] `Commit()` → apply ops, clear buffer, nil writer
- [x] `Abort()` → discard, nil writer
- [x] `Close()` → alias Abort

### 1d: Put

- [x] Validate key length → ErrInvalidKey
- [x] Validate index length → ErrInvalidIndex
- [x] Check capacity → ErrFull
- [x] Buffer operation
- [x] ErrClosed if closed

### 1e: Delete

- [x] Validate key length → ErrInvalidKey
- [x] Return `existed` (check buffer then committed)
- [x] Buffer operation
- [x] ErrClosed if closed

### 1f: Commit Logic

- [x] `finalOps()` → last op per key, preserve order
- [x] Put existing live → update in place
- [x] Put new/deleted → append slot
- [x] Delete live → tombstone
- [x] Delete missing → no-op

### 1g: Get

- [x] Validate key → ErrInvalidKey
- [x] Find live slot (newest first)
- [x] ErrClosed if closed

### 1h: Scan

- [x] Validate opts → ErrInvalidScanOpts (negative offset/limit)
- [x] Collect live entries
- [x] Apply Reverse
- [x] Check offset > count → ErrOffsetOutOfBounds
- [x] Apply Offset, Limit (0 = unlimited)
- [x] ErrClosed if closed

### 1i: ScanPrefix

- [x] Validate prefix → ErrInvalidPrefix (empty, > keySize)
- [x] Filter by prefix
- [x] Apply pagination (same as Scan)
- [x] ErrClosed if closed

---

## Phase 2: Simple State Persistence

Persist state using simple serialization (gob/JSON). No SLC1 format yet.
This validates reopen semantics without file format complexity.

```bash
go test -tags=slotcache_impl -run "Test_Slotcache_Matches_Model_When_Random_Operations_Applied|Test_Metamorphic" ./pkg/slotcache/...
# PASS
```

### 2a: Serialization

- [ ] Create `impl/file.go`
- [ ] `saveState(path, state) error`
- [ ] `loadState(path) (state, error)`
- [ ] Use gob or JSON

### 2b: Integration

- [ ] `Open()` → load if exists, else empty
- [ ] `Commit()` → save after applying ops
- [ ] Verify KeySize/IndexSize/SlotCapacity → ErrIncompatible

---

## Phase 3: SLC1 File Format

Replace simple serialization with the real SLC1 on-disk format per spec.
Rebuild the entire file on every Commit (no incremental updates).

```bash
go test -tags=slotcache_impl -run "Test_Slotcache|Test_Metamorphic" ./pkg/slotcache/...
# PASS
```

### 3a: Header

- [ ] Create `impl/format.go`
- [ ] 256 bytes, little-endian
- [ ] Magic "SLC1", version 1
- [ ] All fields per spec offsets
- [ ] Reserved bytes = 0

### 3b: CRC32-C

- [ ] Zero generation and crc fields before computing
- [ ] `crc32.Checksum(header, crc32.MakeTable(crc32.Castagnoli))`
- [ ] Validate on read → ErrCorrupt

### 3c: Slot Layout

- [ ] `keyPad = (8 - (keySize % 8)) % 8`
- [ ] `slotSize = align8(8 + keySize + keyPad + 8 + indexSize)`
- [ ] meta(u64) + key + pad + revision(i64) + index + pad
- [ ] meta bit 0 = USED

### 3d: Hash Table

- [ ] Create `impl/hash.go`
- [ ] FNV-1a 64-bit (offset=14695981039346656037, prime=1099511628211)
- [ ] `bucketCount = nextPow2(ceil(slotCapacity / HashLoadFactor))`
- [ ] Bucket: hash64(u64) + slot_plus1(u64)
- [ ] EMPTY=0, TOMBSTONE=0xFFFFFFFFFFFFFFFF, FULL=slot_id+1
- [ ] Linear probe: `(i + 1) & (bucketCount - 1)`

### 3e: Write File

- [ ] Write to temp file
- [ ] Write header (generation even)
- [ ] Write slots (0..slot_highwater)
- [ ] Rebuild hash table from live slots
- [ ] Write buckets
- [ ] Compute CRC, patch header
- [ ] Rename to target path

### 3f: Read File

- [ ] Validate header (magic, version, flags, hash_alg)
- [ ] Validate CRC
- [ ] Check reserved = 0 → ErrIncompatible
- [ ] Match KeySize/IndexSize/UserVersion/SlotCapacity → ErrIncompatible
- [ ] Verify slot_size matches computed
- [ ] Verify offsets and file length
- [ ] Verify counters (slot_highwater, live_count, bucket_*)
- [ ] Read slots, build liveIndex
- [ ] If generation odd → ErrBusy

---

## Phase 4: Fuzz Hardening

Run fuzz tests to discover edge cases and bugs. Fix all failures.
Note: FuzzSpec_* tests require SLC1 format (Phase 3) and are gated until then.

```bash
go test -tags=slotcache_impl -fuzz=FuzzBehavior_ModelVsReal -fuzztime=60s ./pkg/slotcache/...
# After Phase 3:
go test -tags=slotcache_impl -fuzz=FuzzSpec_GenerativeUsage -fuzztime=60s ./pkg/slotcache/...
go test -tags=slotcache_impl -fuzz=FuzzSpec_OpenAndReadRobustness -fuzztime=60s ./pkg/slotcache/...
# No failures
```

- [ ] Run FuzzBehavior_ModelVsReal 60s
- [ ] Run FuzzSpec_GenerativeUsage 60s (requires SLC1)
- [ ] Run FuzzSpec_OpenAndReadRobustness 60s (requires SLC1)
- [ ] Fix all failures
- [ ] No panics
- [ ] No infinite loops
- [ ] Open returns only ErrCorrupt/ErrIncompatible/ErrBusy for bad files

---

## Phase 5: mmap + Seqlock

Replace file I/O with mmap. Implement proper seqlock generation protocol
for lock-free readers. Add writeback modes and file locking.

```bash
go test ./pkg/slotcache/...
# PASS
```

### 5a: mmap

- [ ] Replace file read with mmap
- [ ] Map file for readers

### 5b: Seqlock

- [ ] Atomic generation access (`sync/atomic`)
- [ ] Reader: load g1, read, load g2, retry if g1≠g2 or odd
- [ ] Writer: increment to odd, write, increment to even
- [ ] Bounded retries → ErrBusy

### 5c: Writeback

- [ ] WritebackNone (default)
- [ ] WritebackAsync (MS_ASYNC)
- [ ] WritebackSync + AfterPublish/BeforePublish
- [ ] ErrWriteback with Published flag

### 5d: Locking

- [ ] LockFlock (flock for writer)
- [ ] LockNone
- [ ] Odd generation at Open → acquire lock or ErrBusy/ErrCorrupt

---

## Progress

| Phase | Description | Status |
|-------|-------------|--------|
| 0 | Compile | ✅ |
| 1 | In-memory implementation | ✅ |
| 2 | Simple state persistence | ⬜ |
| 3 | SLC1 file format | ⬜ |
| 4 | Fuzz hardening | ⬜ |
| 5 | mmap + seqlock | ⬜ |
