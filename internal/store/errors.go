package store

import "errors"

// ErrWALCorrupt reports a committed WAL with a mismatched checksum.
// Callers should use errors.Is(err, ErrWALCorrupt).
var ErrWALCorrupt = errors.New("wal corrupt")

// ErrWALReplay reports WAL validation or replay failures.
// Callers should use errors.Is(err, ErrWALReplay).
var ErrWALReplay = errors.New("wal replay")

// ErrIndexUpdate reports failures updating the SQLite index from WAL ops.
// Callers should use errors.Is(err, ErrIndexUpdate).
var ErrIndexUpdate = errors.New("index update")
