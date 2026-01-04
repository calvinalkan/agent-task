---
schema_version: 1
id: d5dcbpr
status: open
blocked-by: []
created: 2026-01-04T19:56:11Z
type: feature
priority: 2
assignee: Calvin Alkan
---
# Add bitmap indexes for fast multi-field filtering

Add bitmap indexes to the binary cache for status, priority, and type fields. This enables O(1) multi-field queries like `status=open AND priority=1` instead of O(n) scans.

## Motivation

Current cache requires O(n) scan for filtering. With 100k tickets:
- Current: ~280ms for multi-field filter
- With bitmaps: ~1ms (bitwise AND operations)

## Design

### File Format

```
┌─────────────────────────────────────────────────────────┐
│ Header (32 bytes)                                       │
│   magic: "TKC1"                                         │
│   version: u16 (bump on format change → triggers rebuild│
│   ticket_count: u32                                     │
│   bitmap_section_offset: u32                            │
│   reserved: [20]byte                                    │
├─────────────────────────────────────────────────────────┤
│ Primary Index (existing - 48 bytes × N)                 │
│   sorted by filename for O(log n) lookup by ID          │
├─────────────────────────────────────────────────────────┤
│ Data Section (existing - variable length)               │
│   ticket summaries (title, assignee, blocked-by, etc)   │
├─────────────────────────────────────────────────────────┤
│ Bitmap Section (new)                                    │
│                                                         │
│   Status bitmaps:                                       │
│     [name_len: u8]["open"][bitmap_len: u32][bits...]    │
│     [name_len: u8]["closed"][bitmap_len: u32][bits...]  │
│     [name_len: u8]["in_progress"][...][bits...]         │
│     [0] ← end of section                                │
│                                                         │
│   Priority bitmaps:                                     │
│     [name_len: u8]["1"][bitmap_len: u32][bits...]       │
│     [name_len: u8]["2"][bitmap_len: u32][bits...]       │
│     [name_len: u8]["3"][bitmap_len: u32][bits...]       │
│     [name_len: u8]["4"][bitmap_len: u32][bits...]       │
│     [0] ← end of section                                │
│                                                         │
│   Type bitmaps:                                         │
│     [name_len: u8]["bug"][bitmap_len: u32][bits...]     │
│     [name_len: u8]["feature"][bitmap_len: u32][bits...] │
│     [name_len: u8]["task"][bitmap_len: u32][bits...]    │
│     [name_len: u8]["epic"][bitmap_len: u32][bits...]    │
│     [name_len: u8]["chore"][bitmap_len: u32][bits...]   │
│     [0] ← end of section                                │
│                                                         │
└─────────────────────────────────────────────────────────┘
```

### Bitmap Encoding

Simple bit vector, 1 bit per ticket:
- Bit position = ticket index in primary index
- Bit value: 1 = ticket has this value, 0 = doesn't
- Packed into bytes, little-endian

```go
// 100k tickets = 12.5 KB per bitmap
// 12 bitmaps total (3 status + 4 priority + 5 type) = 150 KB
```

### Self-Describing Format

No hardcoded counts. Each section uses length-prefixed names with zero terminator:
- Adding new status/type = append new bitmap entry
- Format changes = delete cache, rebuild (no version migration during dev)

### API

```go
type BitmapIndex struct {
    values  []string  // ["open", "closed", "in_progress"]
    bitmaps [][]byte  // packed bit vectors
}

// Query returns indices of matching tickets
func (bi *BitmapIndex) Query(value string) *BitSet

// And combines multiple bitmaps
func (bs *BitSet) And(other *BitSet) *BitSet

// Iterate over set bits
func (bs *BitSet) ForEach(fn func(idx int))
```

### BinaryCache Extensions

```go
type BinaryCache struct {
    // existing fields...
    data       []byte
    entryCount int
    updates    map[string]CacheEntry
    
    // new bitmap indexes
    statusIndex   *BitmapIndex
    priorityIndex *BitmapIndex
    typeIndex     *BitmapIndex
}

// Fast multi-field query
func (bc *BinaryCache) QueryMulti(status, priority, ticketType string) []int {
    result := bc.statusIndex.Query(status)
    if priority != "" {
        result = result.And(bc.priorityIndex.Query(priority))
    }
    if ticketType != "" {
        result = result.And(bc.typeIndex.Query(ticketType))
    }
    return result.ToIndices()
}
```

## Implementation

### New file: bitmap.go

```go
// BitSet for efficient bitmap operations
type BitSet struct {
    bits []uint64
    len  int
}

func NewBitSet(size int) *BitSet
func (bs *BitSet) Set(i int)
func (bs *BitSet) Get(i int) bool
func (bs *BitSet) And(other *BitSet) *BitSet
func (bs *BitSet) Or(other *BitSet) *BitSet
func (bs *BitSet) Count() int
func (bs *BitSet) ForEach(fn func(idx int))
func (bs *BitSet) ToBytes() []byte
func BitSetFromBytes(data []byte, len int) *BitSet

// BitmapIndex maps values to bitsets
type BitmapIndex struct {
    values  []string
    bitmaps []*BitSet
}

func (bi *BitmapIndex) Query(value string) *BitSet
func (bi *BitmapIndex) Set(value string, idx int)
func (bi *BitmapIndex) ToBytes() []byte
func BitmapIndexFromBytes(data []byte, ticketCount int) (*BitmapIndex, int)
```

### Changes to cache_binary.go

1. Add bitmap fields to `BinaryCache` struct
2. Extend `LoadBinaryCache` to read bitmap section
3. Extend `writeBinaryCache` to write bitmap section
4. Add `QueryMulti` method

### Changes to ls.go

1. Add `--priority` and `--type` flags
2. Use `QueryMulti` for filtered queries

## Acceptance Criteria

### Core

- [ ] Bitmap indexes built during cache save
- [ ] Bitmap indexes loaded during cache load
- [ ] `QueryMulti(status, priority, type)` returns correct results

### CLI

- [ ] `tk ls --status=open --priority=1` uses bitmap AND
- [ ] `tk ls --type=bug` uses bitmap query
- [ ] Combined filters work: `--status=open --priority=1 --type=feature`

### Performance

- [ ] Multi-field query < 5ms for 100k tickets (vs ~280ms current)
- [ ] Cache size increase < 200KB for 100k tickets
- [ ] Cache build time increase < 10%

### Format

- [ ] Self-describing: no hardcoded value counts
- [ ] Zero-terminated sections

## Tests

### bitmap_test.go

- [ ] BitSet.Set/Get roundtrip
- [ ] BitSet.And correctness
- [ ] BitSet.Or correctness
- [ ] BitSet.Count accuracy
- [ ] BitSet.ForEach visits all set bits
- [ ] BitSet serialization roundtrip
- [ ] BitmapIndex.Query returns correct bitset
- [ ] BitmapIndex with unknown value returns empty bitset
- [ ] BitmapIndex serialization roundtrip

### cache_binary_test.go (additions)

- [ ] Cache loads bitmap indexes
- [ ] QueryMulti single field matches scan results
- [ ] QueryMulti two fields matches scan results
- [ ] QueryMulti three fields matches scan results
- [ ] Empty result when no matches

### ls_test.go (additions)

- [ ] `--priority=N` flag parsed correctly
- [ ] `--type=X` flag parsed correctly
- [ ] Combined flags produce correct filtered output

## Benchmarks

Add to bench_rg_test.go:

```go
func BenchmarkBitmapQuery(b *testing.B) {
    bc := loadCache()
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        results := bc.QueryMulti("open", "1", "")
        if len(results) == 0 {
            b.Fatal("no results")
        }
    }
}

func BenchmarkBitmapQueryThreeFields(b *testing.B) {
    bc := loadCache()
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        results := bc.QueryMulti("open", "1", "feature")
        if len(results) == 0 {
            b.Fatal("no results")
        }
    }
}
```

Target: < 1ms for any combination of filters on 100k tickets.
