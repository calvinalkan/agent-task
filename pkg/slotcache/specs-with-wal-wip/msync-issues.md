# msync Error-Swallowing Edge Case

> msync lets you force a flush so you can control the latest possible moment for a writeout. But the OS can flush before that, and you have no way to detect or control that. So you can only control the late side of the timing, not the early side. And in databases, you usually need writes to be persisted in a specific order; early writes are just as harmful as late writes.
https://news.ycombinator.com/item?id=36567667

## Summary

LMDB docs don't explicitly document this - it's a Linux kernel behavior. This document collects the key sources and explains the issue.

## What LMDB Docs Say

- **Default mode**: uses `pwrite()` + `fdatasync()` (safe)
- **MDB_WRITEMAP**: uses writable mmap (faster but riskier)
- **Warning**: "Do not mix processes with and without MDB_WRITEMAP... this can defeat durability"

## The Kernel Issue: How Error-Swallowing Happens

```
1. App writes to mmap'd region
2. Kernel's pdflush/writeback thread tries to flush page to disk
3. Disk returns I/O error
4. Linux marks the page CLEAN anyway (to free memory)
5. App calls msync() later
6. msync() sees "nothing dirty" → returns SUCCESS
7. Data is gone, app doesn't know
```

## Why LMDB is "Safe" by Default

```c
// Default mode (no MDB_WRITEMAP):
pwrite(fd, data, len, offset);  // Explicit write
fdatasync(fd);                   // If this fails, we KNOW immediately
// → Transaction aborted on ANY error

// With MDB_WRITEMAP:
memcpy(mmap_ptr, data, len);    // Write to mmap
msync(mmap_ptr, len, MS_SYNC);  // Might miss errors from background flush!
```

Howard Chu's philosophy: **"Assume everything failed if fsync/fdatasync fails"** - don't retry, just abort.

## Key Quote from Howard Chu (LMDB Author)

> "By default, LMDB doesn't use msync, so this isn't really a realistic scenario.
> If there is an I/O error that the OS does not report, then sure, it's possible
> for LMDB to have a corrupted view of the world. But that would be an OS bug
> in the first place."

> "The spec leaves the system condition undefined after an fsync failure. The safe
> thing to do is assume everything failed and nothing was written. That's what
> LMDB does. Expecting anything else would be relying on implementation-specific
> knowledge, which is always a bad idea."

## Resources

### Hacker News Discussion (Feb 2019)

- **Main thread**: https://news.ycombinator.com/item?id=19119991
  - "PostgreSQL used fsync incorrectly for 20 years"
- **Howard Chu's comment on LMDB**: https://news.ycombinator.com/item?id=19127650

### LWN Articles (Definitive Technical Writeups)

- https://lwn.net/Articles/752063/ - PostgreSQL fsync issues explained
- https://lwn.net/Articles/718734/ - Error handling in Linux fsync
- https://lwn.net/Articles/752105/ - Linux's position on data loss

### PostgreSQL Documentation

- https://wiki.postgresql.org/wiki/Fsync_Errors - Documents OS behaviors across platforms (FreeBSD & Illumos do it right, Linux/macOS don't)

### LMDB Documentation

- http://www.lmdb.tech/doc/ - Main documentation
- http://www.lmdb.tech/doc/group__mdb__env.html - Environment flags (MDB_WRITEMAP, MDB_MAPASYNC, etc.)
- https://raw.githubusercontent.com/LMDB/lmdb/mdb.master/libraries/liblmdb/lmdb.h - Header file with detailed comments

### Linux Kernel Source

- `mm/msync.c` - msync implementation
- `mm/page-writeback.c` - where dirty flags get cleared
- Browse at: https://elixir.bootlin.com/linux/latest/source/mm/msync.c

### Other Relevant Resources

- https://danluu.com/file-consistency/ - File consistency across filesystems
- http://pages.cs.wisc.edu/~remzi/Classes/736/Papers/iron.pdf - "Iron File Systems" paper on filesystem error handling
- https://www.jwz.org/doc/worse-is-better.html - "Worse is Better" essay (explains why Unix APIs like write() require loops)

### Video

- https://www.youtube.com/watch?v=74c19hwY2oE - Tomas Vondra's talk on fsync error reporting bugs

## OS Behavior Summary

| OS | Behavior | Safe? |
|----|----------|-------|
| FreeBSD | Keeps pages dirty on error, re-reports error | ✅ Yes |
| Illumos | Keeps pages dirty on error, re-reports error | ✅ Yes |
| Linux | Marks pages clean on error, error may be lost | ❌ No |
| macOS | Similar to Linux | ❌ No |

## Practical Implications

1. **Don't use MDB_WRITEMAP** unless you understand the risks
2. **Don't retry fsync** - if it fails, abort the transaction
3. **Use direct I/O** for databases if you need guaranteed durability
4. **FreeBSD/ZFS** are safer choices for data integrity
