package store

import "errors"

// ErrWALCorrupt reports a committed WAL with a mismatched checksum.
var ErrWALCorrupt = errors.New("wal corrupt")

// ErrWALReplay reports WAL validation or replay failures.
var ErrWALReplay = errors.New("wal replay")

// ErrIndexUpdate reports failures updating the SQLite index from WAL ops.
var ErrIndexUpdate = errors.New("index update")
