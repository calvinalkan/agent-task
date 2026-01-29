# MDDB Reindex Performance Benchmarks

## Overview

This document tracks performance benchmarking for the mddb reindex operation using the playground CLI at `cmd/mddb/`.

**Test setup:** 100k markdown files, 90 days distribution, no body (~10 lines each)

**Current performance:** 478ms (209k docs/s)

## Quick Start

```bash
# Build playground CLI
go build -o ./mddb ./cmd/mddb/

# Seed test data (100k files, no body for faster testing)
./mddb seed 100000 --days=90 --clean --no-body

# Run reindex
time ./mddb reindex

# With body (more realistic file size, ~116 lines each)
./mddb seed 100000 --days=90 --clean
./mddb reindex
```

## Benchmark Commands

### Go Benchmarks

```bash
# Run all benchmarks
go test -bench=. -benchmem -benchtime=3x -run=^$ ./cmd/mddb/

# Run specific benchmark
go test -bench=BenchmarkReindexFull -benchmem -benchtime=5x ./cmd/mddb/

# SQLite-specific benchmarks
go test -bench=SQLite -benchmem -benchtime=3x ./cmd/mddb/
```

### Profiling

```bash
# CPU profile via go test (preferred)
go test -bench=BenchmarkReindexFull -cpuprofile=/tmp/cpu.prof -benchtime=3x ./cmd/mddb/
go tool pprof -top /tmp/cpu.prof

# Memory profile via go test
go test -bench=BenchmarkReindexFull -memprofile=/tmp/mem.prof -benchtime=3x ./cmd/mddb/
go tool pprof -top -alloc_space /tmp/mem.prof

# Interactive pprof
go tool pprof -http=:8080 /tmp/cpu.prof

# CPU profile via CLI (alternative)
CPUPROFILE=/tmp/cpu.prof ./mddb reindex
MEMPROFILE=/tmp/mem.prof ./mddb reindex
```

## Timing Breakdown (100k files, no body)

| Phase | Time | Rate | Allocations |
|-------|------|------|-------------|
| ScanStatOnly (no read) | 23ms | 4.4M/s | 202k |
| ScanAndRead (+io.ReadAll) | 53ms | 1.9M/s | 402k |
| ScanReadParse (+frontmatter) | 68ms | 1.5M/s | 2.2M |
| **Full Reindex** | **478ms** | **209k/s** | **5.1M** |
| → SQLite portion | ~410ms | - | - |

## SQLite Insert Performance

### Go (go-sqlite3 with cgo)

| Benchmark | Time | Rate |
|-----------|------|------|
| 100k inserts (7 cols, in-memory) | 246ms | 407k/s |
| 100k inserts (7 cols, file+WAL) | 290ms | 345k/s |
| 100k inserts (2 cols, file) | 189ms | 529k/s |

### Pure C (SQLite C API)

| Benchmark | Time | Rate |
|-----------|------|------|
| 100k inserts (7 cols, in-memory) | 79ms | 1.26M/s |
| 100k inserts (7 cols, file+WAL) | 99ms | 1.0M/s |

**Key finding:** cgo overhead accounts for ~67% of Go's SQLite time. Pure C is 3x faster.

### C Benchmark

```bash
# Build and run C benchmark (uses go-sqlite3 bundled SQLite)
cd /tmp/sqlite_bench
SQLITE_DIR="/home/calvin/go/pkg/mod/github.com/mattn/go-sqlite3@v1.14.33"
gcc -O2 -I"$SQLITE_DIR" -o bench bench.c "$SQLITE_DIR/sqlite3-binding.c" -lpthread -ldl -lm
./bench        # in-memory
./bench_file   # file-backed with WAL
```

### Unsafe Rebuild (temp DB + swap strategy)

Benchmark harness: `/tmp/sql-bench` (standalone, go-sqlite3). All numbers below are for 100k rows.

**Unsafe pragmas used:**
`journal_mode=OFF, synchronous=OFF, locking_mode=EXCLUSIVE, temp_store=MEMORY, foreign_keys=OFF, cache_size=100000`

**Batch size (multi-row INSERT)**

| Batch Size | Avg Time | Rate |
|-----------|----------|------|
| 10 | ~159ms | ~631k rows/s |
| 25 | ~164ms | ~609k rows/s |
| 50 | ~161ms | ~623k rows/s |
| 100 | ~169ms | ~592k rows/s |
| 200 | ~173ms | ~578k rows/s |
| 500 | ~177ms | ~565k rows/s |

**Indexes (4 secondary indexes on short_id/path/mtime/title)**

| Index Strategy | Avg Time | Rate |
|---------------|----------|------|
| Indexes before insert | ~212ms | ~473k rows/s |
| Indexes after insert | ~226ms | ~442k rows/s |

**Body size impact (batch 50, with indexes)**

| Body Size | /dev/shm | /tmp |
|-----------|----------|------|
| 1KB | ~263k rows/s | ~243k rows/s |
| 8KB | ~79k rows/s | ~63k rows/s |

**mmap_size=2GB**
- Large bodies (~8KB): ~6% faster on both /dev/shm and /tmp.
- Small bodies (~1KB): mixed (slightly better on /dev/shm, slightly worse on /tmp).

**Takeaway:** With unsafe pragmas + batch(10-50), ceiling is ~620-630k rows/s for tiny bodies. Body size dominates; 8KB bodies drop to ~80k rows/s even with unsafe settings.

## CPU Profile Breakdown

| Component | % of CPU |
|-----------|----------|
| Syscalls (I/O + sqlite) | 22% |
| GC (scan + malloc) | 25% |
| frontmatter.parse | 15% |
| cgo overhead | 11% |

## Memory Profile (100k files)

| Allocator | MB | % |
|-----------|-----|---|
| frontmatter.parse | 190 | 24% |
| io.ReadAll | 90 | 11% |
| parseDoc | 88 | 11% |
| sqlite3.bind | 78 | 10% |

**Total: ~400MB, 5.1M allocations**

## What We Tested

| Change | Result |
|--------|--------|
| Remove json.Marshal from Upsert | 632ms → 478ms ✓ |
| PRAGMA synchronous=OFF | No change (already in transaction) |
| PRAGMA journal_mode=MEMORY | No change |
| Batched INSERT (100x1000 rows) | 3.7x slower (query building overhead) |
| In-memory SQLite | 290ms → 246ms (15% faster) |
| Fewer columns (7→2) | 290ms → 189ms (not practical) |

## Bottleneck Analysis

### 1. SQLite/cgo (86% of time)
- Each `ExecContext` call crosses Go↔C boundary (~2-3µs overhead)
- Floor is ~100ms in pure C for 100k inserts
- Go achieves ~290ms = 3x slower than C

### 2. GC Pressure (25% of CPU)
- 5.1M allocations cause significant GC work
- Frontmatter parser is main culprit (190MB, 2.2M allocs)
- Each file parse creates new maps, strings, slices

### 3. Frontmatter Parsing (15% of CPU)
- Creates `Frontmatter` map per file
- Allocates strings for each value
- No buffer reuse

## Optimization Opportunities

### High Impact

1. **Incremental sync (mtime tracking)**
   - Load existing mtimes from SQLite
   - Skip unchanged files (same mtime)
   - Only parse/insert changed files
   - Expected warm run: ~30ms vs 480ms

2. **Pure-Go SQLite (modernc.org/sqlite)**
   - Eliminates cgo overhead entirely
   - May or may not be faster overall (needs testing)

3. **Reduce frontmatter allocations**
   - sync.Pool for Frontmatter maps
   - Reuse line reader buffers
   - Avoid string copies where possible

### Medium Impact

4. **Streaming file read**
   - Don't io.ReadAll entire file
   - Read only until frontmatter closing `---`
   - Saves memory for files with large bodies

5. **Pipeline parse↔insert**
   - Parse files in parallel
   - Feed to single SQLite writer
   - Overlap I/O with CPU work

### Low Impact

6. **SQLite tuning** - Most pragmas don't help within a transaction
7. **Batched INSERT** - Actually slower due to query building overhead

## File Impact (with vs without body)

| File Type | Lines | Reindex Time | Rate |
|-----------|-------|--------------|------|
| No body | ~10 | 478ms | 209k/s |
| 100-line body | ~116 | 4.8s | 21k/s |

Large file bodies significantly impact performance due to io.ReadAll overhead.

## Benchmark Files

- `cmd/mddb/bench_test.go` - Go benchmarks for reindex phases
- `cmd/mddb/bench_sql_test.go` - SQLite insert benchmarks
- `/tmp/sqlite_bench/bench.c` - Pure C SQLite benchmark
- `/tmp/sqlite_bench/bench_file.c` - Pure C with file-backed DB

## Available Benchmarks

```
BenchmarkReindexFull        - Full reindex (scan + parse + SQL)
BenchmarkScanStatOnly       - fileproc stat only, no file read
BenchmarkScanAndRead        - fileproc + io.ReadAll
BenchmarkScanReadParse      - fileproc + read + frontmatter parse
BenchmarkFrontmatterParseSingle - Single frontmatter parse
BenchmarkSQLiteInsertRaw/*  - Various SQLite insert patterns
```

## Next Steps

1. Implement incremental sync with mtime tracking
2. Benchmark warm sync (0 changes vs N changes)
4. Profile and optimize frontmatter parser allocations
