# IMPLEMENTATION_PLAN.md (slotcache)

- [x] Performance audit (optional): WritebackSync currently calls `msyncRange(data, 0, len(data))` (whole mapping). The kernel usually skips clean pages, but if this is a bottleneck for large caches, track touched page ranges (slots/buckets + header) and msync only those (still page-aligned) while preserving correctness.
- [x] Maintenance (optional): avoid unbounded growth of the in-process `(dev,inode) → registry entry` map by pruning entries once the last cache handle closes.
