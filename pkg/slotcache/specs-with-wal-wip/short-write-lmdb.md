# Short Writes in LMDB: Why Not Retrying is Correct

## Background

LMDB treats short writes as fatal errors rather than retrying. At first glance, this seems wrong since POSIX allows `write()` to return fewer bytes than requested. However, for **regular files** (which LMDB uses), short writes have specific semantics that make LMDB's approach correct.

## LMDB's Short Write Handling

### Data Pages (`mdb_page_flush`, mdb.c ~line 3885)

```c
#ifdef MDB_USE_PWRITEV
    wres = pwritev(fd, iov, n, wpos);
#else
    if (n == 1) {
        wres = pwrite(fd, iov[0].iov_base, wsize, wpos);
    } else {
        // ... lseek + writev fallback
    }
#endif
    if (wres != wsize) {
        if (wres < 0) {
            rc = ErrCode();
            if (rc == EINTR)
                goto retry_write;  // Only retry on signal interrupt
            DPRINTF(("Write error: %s", strerror(rc)));
        } else {
            rc = EIO; /* TODO: Use which error code? */
            DPUTS("short write, filesystem full?");
        }
        return rc;  // Fail the transaction
    }
```

### Meta Page (`mdb_env_write_meta`, mdb.c ~line 4445)

```c
retry_write:
    rc = pwrite(mfd, ptr, len, off);
    if (rc != len) {
        rc = rc < 0 ? ErrCode() : EIO;
        if (rc == EINTR)
            goto retry_write;  // Only retry EINTR
        DPUTS("write failed, disk error?");
        
        // Attempt to write old data back to prevent corruption
        meta.mm_last_pg = metab.mm_last_pg;
        meta.mm_txnid = metab.mm_txnid;
        r2 = pwrite(env->me_fd, ptr, len, off);
        (void)r2;  // Best effort, ignore result
        
        env->me_flags |= MDB_FATAL_ERROR;
        return rc;
    }
```

## Why This is Correct: POSIX Semantics

### Regular Files vs Pipes/Sockets

The key insight is that **short writes mean different things for different file types**.

From [POSIX write() specification](https://pubs.opengroup.org/onlinepubs/9699919799/functions/write.html):

> If a write() requests that more bytes be written than there is room for (for example, the file size limit of the process or **the physical end of a medium**), only as many bytes as there is room for shall be written. For example, suppose there is space for 20 bytes more in a file before reaching a limit. **A write of 512 bytes will return 20. The next write of a non-zero number of bytes would give a failure return**.

For **pipes and sockets** with `O_NONBLOCK`:

> If O_NONBLOCK is set and part of the buffer has been written while a condition in which the STREAM cannot accept additional data occurs, write() shall terminate and return the number of bytes written.

### Summary Table

| File Type | O_NONBLOCK | Short Write Meaning | Correct Action |
|-----------|------------|---------------------|----------------|
| Regular file | clear (LMDB's case) | Disk full or file size limit hit | Fail (retrying won't help) |
| Regular file | set | Same as above | Fail |
| Pipe/FIFO | clear | N/A (blocks until complete) | N/A |
| Pipe/FIFO | set | Buffer full, try again later | Retry |
| Socket | set | Buffer full, flow control | Retry |

### Key POSIX Quotes

On regular file behavior:

> On a regular file or other file capable of seeking, the actual writing of data shall proceed from the position in the file indicated by the file offset associated with fildes. Before successful return from write(), the file offset shall be incremented by the number of bytes actually written.

On the only case where short writes happen for regular files:

> If a write() requests that more bytes be written than there is room for [...] only as many bytes as there is room for shall be written.

On blocking behavior for regular files:

> When attempting to write to a file descriptor (other than a pipe or FIFO) that supports non-blocking writes and cannot accept the data immediately:
> - If the O_NONBLOCK flag is clear, write() shall block the calling thread until the data can be accepted.

This means for regular files in blocking mode (LMDB's case), `write()` either:
1. Completes fully, OR
2. Returns a short write because of a hard limit (disk full, file size limit), OR  
3. Returns -1 with an error

## LMDB's Error Handling Strategy

| Condition | LMDB Action |
|-----------|-------------|
| `EINTR` (signal interrupted) | Retry the write |
| `wres < 0` (error) | Return error code, fail transaction |
| `0 < wres < requested` (short write) | Return `EIO`, fail transaction |
| Write succeeds | Continue |

On meta page write failure, LMDB also:
1. Attempts to write old (valid) data back to prevent the corrupted partial write from being used
2. Sets `MDB_FATAL_ERROR` flag on the environment

## Conclusion

LMDB's comment `"short write, filesystem full?"` is exactly correct. For a regular file in blocking mode:

- A short write indicates an **unrecoverable condition** (out of disk space, hit file size limit)
- The next write would fail with `ENOSPC` or `EFBIG`
- Retrying is pointless and could make things worse

This is different from sockets/pipes where short writes are normal flow control and retrying is expected.

## References

- [POSIX write() specification](https://pubs.opengroup.org/onlinepubs/9699919799/functions/write.html)
- [LMDB source code](https://github.com/LMDB/lmdb) - `libraries/liblmdb/mdb.c`
- LMDB version examined: `mdb.master` branch (January 2026)
