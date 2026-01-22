---
schema_version: 1
id: d5rsemr
status: open
blocked-by: []
created: 2026-01-22T03:19:47Z
type: feature
priority: 3
---
# slotcache: reduce WritebackSync msyncs from 3 to 2 using dual generation markers

## Problem

Currently WritebackSync mode requires 3 msync calls per commit:
1. `msync(header)` - odd generation barrier
2. `msync(data)` - slots + buckets
3. `msync(header)` - even generation publish

This is ~900µs overhead per commit (3 × ~300µs per msync).

## Proposed Solution

Use two generation markers instead of one:

```
┌────────────────────────────┐
│ start_gen (odd marker)     │ ← first 8 bytes of header
├────────────────────────────┤
│ Header (counters, CRC)     │
├────────────────────────────┤
│ Slots                      │
├────────────────────────────┤
│ Buckets                    │
├────────────────────────────┤
│ end_gen (commit marker)    │ ← trailer at EOF
└────────────────────────────┘
```

Commit sequence:
1. `start_gen = N+1` (odd)
2. `msync(header)` ← barrier
3. Write data
4. `end_gen = N+2` (even)
5. `msync(data + trailer)` ← one call covers everything

Validation on open:
- If `start_gen != end_gen + 1` → corrupt (incomplete write)
- If `end_gen` is odd → corrupt
- If `end_gen` is even and matches → valid

## Benefits

- 2 msyncs instead of 3 (~33% faster for sync commits)
- Decouples "write started" from "write finished" markers
- Same crash safety guarantees

## Considerations

- Format change (v2) - not backwards compatible
- Need to update spec docs
- Trailer adds 8 bytes to file size
- Open validation slightly more complex

## Acceptance Criteria

- [ ] Design doc / spec update for dual-gen approach
- [ ] Implement and benchmark vs current 3-msync approach
- [ ] Verify crash safety with fault injection tests
