// Package fmcache provides a fast, indexed key-value cache backed by a binary file.
//
// This file defines the cache spec: an in-memory model of correct behavior.
// The real implementation must behave identically.
//
// Design principles:
//   - Simple enough to be obviously correct by inspection.
//   - No tests needed for the spec itself.
//   - Returns errors for invalid operations (ErrClosed, ErrInvalidKey, etc.)
//
// Persistence model:
//   - disk = committed state (what's on file)
//   - mem = current session (uncommitted changes)
//   - Commit: mem → disk
//   - Reopen: disk → mem (simulates close + open)
//
// Ordering:
//   - AllEntries() returns entries in key order (alphabetical), with optional reverse/paging.
//   - FilterEntries() returns entries in key order (alphabetical), with optional reverse/paging.
package fmcache

import (
	"iter"
	"slices"
)

// Entry is a cached item.
type Entry[T any] struct {
	Key      string
	Revision int64
	Value    T
}

func cloneEntry[T any](e Entry[T]) Entry[T] {
	return Entry[T]{
		Key:      e.Key,
		Revision: e.Revision,
		Value:    e.Value,
	}
}

// Spec is the in-memory model of the cache.
type Spec[T any] struct {
	disk   map[string]Entry[T] // committed state
	mem    map[string]Entry[T] // current session
	closed bool
}

// NewSpec creates a new spec with empty disk and an open session.
func NewSpec[T any]() *Spec[T] {
	return &Spec[T]{
		disk: make(map[string]Entry[T]),
		mem:  make(map[string]Entry[T]),
	}
}

// Len returns entry count.
func (s *Spec[T]) Len() (int, error) {
	if s.closed {
		return 0, ErrClosed
	}

	return len(s.mem), nil
}

// Get retrieves an entry by key.
func (s *Spec[T]) Get(key string) (Entry[T], bool, error) {
	if s.closed {
		return Entry[T]{}, false, ErrClosed
	}

	if key == "" || slices.Contains([]byte(key), 0) {
		return Entry[T]{}, false, ErrInvalidKey
	}

	e, ok := s.mem[key]
	if !ok {
		return Entry[T]{}, false, nil
	}

	return cloneEntry(e), true, nil
}

// Put adds or updates an entry.
func (s *Spec[T]) Put(key string, revision int64, value T) error {
	if s.closed {
		return ErrClosed
	}

	if key == "" || slices.Contains([]byte(key), 0) {
		return ErrInvalidKey
	}

	s.mem[key] = Entry[T]{
		Key:      key,
		Revision: revision,
		Value:    value,
	}

	return nil
}

// Delete removes an entry. Returns true if it existed.
func (s *Spec[T]) Delete(key string) (bool, error) {
	if s.closed {
		return false, ErrClosed
	}

	_, ok := s.mem[key]
	delete(s.mem, key)

	return ok, nil
}

// AllEntries iterates all entries in key order, with optional reverse/paging.
func (s *Spec[T]) AllEntries(opts FilterOpts) (iter.Seq[Entry[T]], error) {
	return s.FilterEntries(opts, func(Entry[T]) bool { return true })
}

// FilterEntries iterates entries in key order and applies match.
//
// If opts.Offset or opts.Limit is negative, it returns ErrInvalidFilterOpts.
// If opts.Offset is beyond the number of matches, it returns ErrOffsetOutOfBounds.
func (s *Spec[T]) FilterEntries(opts FilterOpts, match func(Entry[T]) bool) (iter.Seq[Entry[T]], error) {
	if s.closed {
		return nil, ErrClosed
	}

	if opts.Offset < 0 || opts.Limit < 0 {
		return nil, ErrInvalidFilterOpts
	}

	keys := make([]string, 0, len(s.mem))
	for k := range s.mem {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	if opts.Reverse {
		slices.Reverse(keys)
	}

	// Collect matching entries in key order.
	matches := make([]Entry[T], 0, len(keys))
	for _, k := range keys {
		e := cloneEntry(s.mem[k])
		if match(e) {
			matches = append(matches, e)
		}
	}

	if opts.Offset > len(matches) {
		return nil, ErrOffsetOutOfBounds
	}

	start := opts.Offset

	end := len(matches)
	if opts.Limit > 0 && start+opts.Limit < end {
		end = start + opts.Limit
	}

	matches = matches[start:end]

	return func(yield func(Entry[T]) bool) {
		for _, e := range matches {
			if !yield(e) {
				return
			}
		}
	}, nil
}

// Commit flushes mem to disk.
func (s *Spec[T]) Commit() error {
	if s.closed {
		return ErrClosed
	}

	s.disk = make(map[string]Entry[T], len(s.mem))
	for k, v := range s.mem {
		s.disk[k] = cloneEntry(v)
	}

	return nil
}

// Reopen simulates close + open: returns new spec with mem loaded from disk.
func (s *Spec[T]) Reopen() (*Spec[T], error) {
	if s.closed {
		return nil, ErrClosed
	}

	s.closed = true

	mem := make(map[string]Entry[T], len(s.disk))
	for k, v := range s.disk {
		mem[k] = cloneEntry(v)
	}

	return &Spec[T]{
		disk: s.disk,
		mem:  mem,
	}, nil
}

// Close closes the spec.
func (s *Spec[T]) Close() error {
	s.closed = true

	return nil
}
