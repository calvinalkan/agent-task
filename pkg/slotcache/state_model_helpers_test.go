//go:build slotcache_impl

package slotcache_test

// Shared test helpers for state-model property and metamorphic tests.
// These helpers compare OBSERVABLE BEHAVIOR ONLY - they do not access internal state.

import (
	"bytes"
	"errors"
	"iter"
	"slices"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/model"
)

// -----------------------------------------------------------------------------
// Entry conversion helpers
// -----------------------------------------------------------------------------

func toSlotcacheEntry(me model.Entry) slotcache.Entry {
	return slotcache.Entry{
		Key:      me.Key,
		Revision: me.Revision,
		Index:    me.Index,
	}
}

func toSlotcacheEntries(entries []model.Entry) []slotcache.Entry {
	// Always return non-nil slice for consistent comparison.
	// The nil vs empty distinction has no semantic meaning.
	out := make([]slotcache.Entry, len(entries))
	for i, e := range entries {
		out[i] = toSlotcacheEntry(e)
	}

	return out
}

// -----------------------------------------------------------------------------
// Scan collection helpers
// -----------------------------------------------------------------------------

func collectSeq(seq slotcache.Seq) []slotcache.Entry {
	result := slices.Collect(iter.Seq[slotcache.Entry](seq))
	// Normalize nil to empty slice for consistent comparison.
	// The Seq abstraction cannot distinguish nil vs empty,
	// and this distinction has no semantic meaning in the API.
	if result == nil {
		return []slotcache.Entry{}
	}
	return result
}

func scanReal(t *testing.T, cache slotcache.Cache, opts slotcache.ScanOpts) ([]slotcache.Entry, error) {
	t.Helper()

	seq, err := cache.Scan(opts)
	if err != nil {
		return nil, err
	}

	return collectSeq(seq), nil
}

func scanRealPrefix(t *testing.T, cache slotcache.Cache, prefix []byte, opts slotcache.ScanOpts) ([]slotcache.Entry, error) {
	t.Helper()

	seq, err := cache.ScanPrefix(prefix, opts)
	if err != nil {
		return nil, err
	}

	return collectSeq(seq), nil
}

func scanModel(t *testing.T, cache *model.CacheModel, opts slotcache.ScanOpts) ([]slotcache.Entry, error) {
	t.Helper()

	entries, err := cache.Scan(opts)
	if err != nil {
		return nil, err
	}

	return toSlotcacheEntries(entries), nil
}

func scanModelPrefix(t *testing.T, cache *model.CacheModel, prefix []byte, opts slotcache.ScanOpts) ([]slotcache.Entry, error) {
	t.Helper()

	entries, err := cache.ScanPrefix(prefix, opts)
	if err != nil {
		return nil, err
	}

	return toSlotcacheEntries(entries), nil
}

// -----------------------------------------------------------------------------
// Error comparison helpers
// -----------------------------------------------------------------------------

// errorsMatch checks if two errors represent the same error class.
// Uses errors.Is bidirectionally because either error may wrap the other.
func errorsMatch(mErr, rErr error) bool {
	if mErr == nil && rErr == nil {
		return true
	}

	if mErr == nil || rErr == nil {
		return false
	}

	return errors.Is(mErr, rErr) || errors.Is(rErr, mErr)
}

// -----------------------------------------------------------------------------
// Entry comparison helpers
// -----------------------------------------------------------------------------

func entriesEqual(a, b slotcache.Entry) bool {
	return a.Revision == b.Revision &&
		bytes.Equal(a.Key, b.Key) &&
		bytes.Equal(a.Index, b.Index)
}

// entriesAreReverse checks if rev is the exact reverse of fwd.
func entriesAreReverse(fwd, rev []slotcache.Entry) bool {
	if len(fwd) != len(rev) {
		return false
	}

	for i := range fwd {
		if !entriesEqual(fwd[i], rev[len(fwd)-1-i]) {
			return false
		}
	}

	return true
}

// -----------------------------------------------------------------------------
// Observable state comparison
// -----------------------------------------------------------------------------

// compareObservableState compares all publicly observable state between model and real.
//
// Performs redundant checks intentionally:
// - Len() must match
// - Forward scan must match
// - Reverse scan must match
// - Forward/reverse must be exact reverses of each other
// - Get() for each observed key must match
// - Prefix scans for each observed key's first byte must match.
func compareObservableState(t *testing.T, h *harness) {
	t.Helper()

	// Compare Len().
	mLen, mLenErr := h.model.cache.Len()
	rLen, rLenErr := h.real.cache.Len()

	if !errorsMatch(mLenErr, rLenErr) {
		t.Fatalf("Len() error mismatch\nmodel=%v\nreal=%v", mLenErr, rLenErr)
	}

	if mLenErr != nil {
		return // Both closed: no more observable state to compare.
	}

	if mLen != rLen {
		t.Fatalf("Len() value mismatch\nmodel=%d\nreal=%d", mLen, rLen)
	}

	// Compare forward scans.
	fwdOpts := slotcache.ScanOpts{Reverse: false, Offset: 0, Limit: 0}
	revOpts := slotcache.ScanOpts{Reverse: true, Offset: 0, Limit: 0}

	mFwd, mFwdErr := scanModel(t, h.model.cache, fwdOpts)
	rFwd, rFwdErr := scanReal(t, h.real.cache, fwdOpts)

	if !errorsMatch(mFwdErr, rFwdErr) {
		t.Fatalf("Scan(forward) error mismatch\nmodel=%v\nreal=%v", mFwdErr, rFwdErr)
	}

	if diff := cmp.Diff(mFwd, rFwd); diff != "" {
		t.Fatalf("Scan(forward) entries mismatch (-model +real):\n%s", diff)
	}

	// Verify Len() matches Scan() count.
	if mLen != len(mFwd) {
		t.Fatalf("model: Len()=%d but Scan(forward) returned %d entries", mLen, len(mFwd))
	}

	if rLen != len(rFwd) {
		t.Fatalf("real: Len()=%d but Scan(forward) returned %d entries", rLen, len(rFwd))
	}

	// Compare reverse scans.
	mRev, mRevErr := scanModel(t, h.model.cache, revOpts)
	rRev, rRevErr := scanReal(t, h.real.cache, revOpts)

	if !errorsMatch(mRevErr, rRevErr) {
		t.Fatalf("Scan(reverse) error mismatch\nmodel=%v\nreal=%v", mRevErr, rRevErr)
	}

	if diff := cmp.Diff(mRev, rRev); diff != "" {
		t.Fatalf("Scan(reverse) entries mismatch (-model +real):\n%s", diff)
	}

	// Verify forward/reverse are exact reverses.
	if !entriesAreReverse(mFwd, mRev) {
		t.Fatalf("model: reverse scan is not the exact reverse of forward scan")
	}

	if !entriesAreReverse(rFwd, rRev) {
		t.Fatalf("real: reverse scan is not the exact reverse of forward scan")
	}

	// Compare some paging variants.
	pageOpts := []slotcache.ScanOpts{
		{Reverse: false, Offset: 0, Limit: 1},
		{Reverse: false, Offset: 1, Limit: 2},
		{Reverse: true, Offset: 1, Limit: 2},
	}

	for _, opts := range pageOpts {
		mPage, mPageErr := scanModel(t, h.model.cache, opts)
		rPage, rPageErr := scanReal(t, h.real.cache, opts)

		if !errorsMatch(mPageErr, rPageErr) {
			t.Fatalf("Scan(%+v) error mismatch\nmodel=%v\nreal=%v", opts, mPageErr, rPageErr)
		}

		if diff := cmp.Diff(mPage, rPage); diff != "" {
			t.Fatalf("Scan(%+v) entries mismatch (-model +real):\n%s", opts, diff)
		}
	}

	// Cross-check Get() for all keys observed by Scan.
	keys := make(map[string][]byte)
	for _, e := range mFwd {
		keys[string(e.Key)] = e.Key
	}

	for _, e := range rFwd {
		keys[string(e.Key)] = e.Key
	}

	// Sort keys for deterministic iteration order.
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}

	sort.Strings(sorted)

	for _, k := range sorted {
		key := keys[k]

		mEntry, mOk, mErr := h.model.cache.Get(key)
		rEntry, rOk, rErr := h.real.cache.Get(key)

		if !errorsMatch(mErr, rErr) {
			t.Fatalf("Get(%x) error mismatch\nmodel=%v\nreal=%v", key, mErr, rErr)
		}

		if mOk != rOk {
			t.Fatalf("Get(%x) exists mismatch\nmodel=%v\nreal=%v", key, mOk, rOk)
		}

		if diff := cmp.Diff(toSlotcacheEntry(mEntry), rEntry); diff != "" {
			t.Fatalf("Get(%x) entry mismatch (-model +real):\n%s", key, diff)
		}
	}

	// Prefix scan cross-check: for each live key, a 1-byte prefix scan must include it.
	for _, e := range mFwd {
		if len(e.Key) < 1 {
			continue
		}

		prefix := e.Key[:1]

		mPfx, mPfxErr := scanModelPrefix(t, h.model.cache, prefix, fwdOpts)
		rPfx, rPfxErr := scanRealPrefix(t, h.real.cache, prefix, fwdOpts)

		if !errorsMatch(mPfxErr, rPfxErr) {
			t.Fatalf("ScanPrefix(%x) error mismatch\nmodel=%v\nreal=%v", prefix, mPfxErr, rPfxErr)
		}

		if diff := cmp.Diff(mPfx, rPfx); diff != "" {
			t.Fatalf("ScanPrefix(%x) entries mismatch (-model +real):\n%s", prefix, diff)
		}
	}
}
