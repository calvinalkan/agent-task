# mmap tuning for slotcache

This document collects **operational performance issues** observed with `mmap` (as summarized in Crotty/Leis/Pavlo, CIDR'22) and **practical tuning guidance** for slotcache.

## Important: Counterpoints

https://www.symas.com/post/are-you-sure-you-want-to-use-mmap-in-your-dbms

---

## 1) The mmap performance problems (why tuning is needed)

### 1.1 I/O stalls (page-fault latency)
With `mmap`, reads are ordinary memory loads/stores. If the referenced page is not resident, the CPU triggers a **page fault** and the kernel must load the page from storage.

Implications:
- A read that "should be a few CPU instructions" can become a **blocking storage read** (major fault).
- There is no natural async I/O scheduling like `io_uring`/`libaio`/`pread` pipelines; faults are synchronous.

Where it hurts most in slotcache:
- Cold **`Get(key)`** (random touches across buckets + slot pages)
- Reverse/backward scans (readahead is usually forward; backwards can behave random-ish)

### 1.2 Eviction-regime throughput cliff
Once the mapped working set exceeds RAM and the OS must actively evict pages, throughput can collapse. CIDR'22 attributes this to (Linux terminology):

- **Page table contention**: page-fault handling updates VM structures and can serialize under high concurrency.
- **Single-threaded/serialized reclaim**: a lot of page reclaim work funnels through limited kernel paths (e.g. `kswapd` / reclaim), which can become CPU-bound.
- **TLB shootdowns**: evicting mapped pages requires invalidating TLB entries across cores (inter-processor interrupts), which is expensive and scales poorly.

This is the main reason mmap can be 2–20× slower than direct I/O in the paper once eviction starts.

### 1.3 Readahead mismatch (wasted I/O)
The OS tries to detect sequential access and does **readahead** (prefetching adjacent pages). This helps forward scans, but can be harmful for:
- random access (hash-table probes)
- reverse scans

Symptoms:
- wasted I/O bandwidth (reading pages you never touch)
- cache pollution (evicting useful pages sooner)

---

## 2) Observability: how to tell you're in trouble

Common indicators:
- Rising **major page faults** (Linux: `/proc/<pid>/stat`, `perf stat`, `vmstat 1`)
- High disk reads while CPU is not saturated (I/O bound)
- Stall spikes / tail latency spikes for `Get` operations
- On Linux, visible reclaim/kswapd CPU time under memory pressure

---

## 3) Practical tuning knobs for slotcache

### 3.1 Prefer forward sequential scans
Forward scans give the OS the best chance to do effective readahead. For reverse output order, consider:
- forward scan + reverse in-memory, or
- forward scan + ring-buffer of the last N matches

This avoids reverse paging behavior (which can defeat readahead when cold).

### 3.2 Use `madvise` by region (best when sections are page-aligned)
`madvise` is advisory (the kernel may ignore it), but it can materially reduce wasted readahead and cache thrash.

Concepts:
- `MADV_SEQUENTIAL`: help streaming scans
- `MADV_RANDOM`: disable aggressive readahead ("no-readahead" behavior, similar to LMDB's `MDB_NORDAHEAD`)
- `MADV_DONTNEED`: drop clean pages from the page cache sooner (Linux; macOS behavior differs)

**Important:** `madvise` works at page granularity. Always page-align address/range (round start down, end up).

#### Recommended static hints (set once at Open)
For typical slotcache workloads:
- **buckets / hash tables (random probes)**: `MADV_RANDOM`
- **WAL index (random probes)**: `MADV_RANDOM`
- **header / reader slot region**: `MADV_RANDOM` (small/hot anyway)
- **slots array**: leave `MADV_NORMAL` by default

Rationale: buckets/index lookups are random; readahead is mostly wasted.

#### Dynamic hints for large scans
For large forward scans:
- `MADV_SEQUENTIAL` on the scanned slots range
- (WAL-first format) `MADV_SEQUENTIAL` on the WAL scan range while building the overlay map

Optional (workload dependent):
- after a full scan, `MADV_DONTNEED` on the scanned slots range to avoid polluting cache for subsequent random `Get`s.

#### Avoid per-Get toggling
Do **not** switch `madvise` mode per `Get`:
- it's a syscall and can dominate hot-cache `Get` latency
- it affects the mapping range for the whole process (can interfere with concurrent scans)

Instead, set stable advice per region or per coarse operation (scan/checkpoint).

### 3.3 Keep the WAL small (WAL-first formats)
In WAL-overlay designs, scans often do:
1) scan WAL to build overlay map
2) scan base slots

If the WAL grows large:
- scans pay extra CPU and touch more pages (more faults / eviction work)
- overlay map allocation may become a hotspot

Operational guidance:
- checkpoint when WAL bytes/records exceed a threshold
- checkpoint on a cadence
- checkpoint opportunistically when free space is low

### 3.4 Limit concurrent full scans
Multiple concurrent full scans over a mapping larger than RAM often do not scale linearly; they can amplify eviction pressure and page-fault contention.

Practical guidance:
- treat full scans as a "heavy" operation
- consider limiting scans to 1 per process (or small N)

### 3.5 Size the cache to the machine
If `file_size >> RAM`, mmap will eventually operate in the eviction regime.

Guidance:
- keep the working set (or common hot subset) within RAM when possible
- set expectations: mmap + random lookups on a much-larger-than-RAM cache will have stall spikes and may not scale under high concurrency

---

## 4) Implementation notes for Go

### 4.1 Page-aligning ranges
Even if the on-disk sections are not page-aligned, syscalls can be applied safely by rounding:

- `alignedStart = (start/pageSize)*pageSize`
- `alignedEnd   = ceil(end/pageSize)*pageSize`

This may cause a one-page overlap across adjacent sections; that is usually acceptable.

### 4.2 Where to hook tuning
Recommended hook points:
- `Open()`: apply static `madvise` per region
- `Scan(...)`: before scanning, apply `MADV_SEQUENTIAL`; optionally `MADV_DONTNEED` after
- `Checkpoint()`: apply `MADV_SEQUENTIAL` on WAL/base ranges it will touch

---

## 5) Limits of tuning (what we cannot fix)

Even with perfect hints:
- page faults are still synchronous stalls
- eviction under high concurrency can still trigger OS-level bottlenecks (page table contention, reclaim serialization, TLB shootdowns)

If the workload is dominated by **cold random `Get`** over a dataset far larger than RAM, and it must scale to many cores, then mmap may not be the right tool (paper conclusion).
