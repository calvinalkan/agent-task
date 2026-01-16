package testutil

// Shared test helpers for state-model property and metamorphic tests.
// These helpers compare OBSERVABLE BEHAVIOR ONLY - they do not access internal state.

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil/model"
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

// ToEntries converts model entries to slotcache entries.
func ToEntries(entries []model.Entry) []slotcache.Entry {
	return toEntriesInto(nil, entries)
}

func toEntriesInto(dst []slotcache.Entry, entries []model.Entry) []slotcache.Entry {
	if len(entries) == 0 {
		return dst[:0]
	}

	if cap(dst) < len(entries) {
		dst = make([]slotcache.Entry, len(entries))
	} else {
		dst = dst[:len(entries)]
	}

	for i, e := range entries {
		dst[i] = toSlotcacheEntry(e)
	}

	return dst
}

func ensureEntriesCapacity(dst []slotcache.Entry, capacity int) []slotcache.Entry {
	if cap(dst) < capacity {
		return make([]slotcache.Entry, 0, capacity)
	}

	return dst[:0]
}

// -----------------------------------------------------------------------------
// Scan collection helpers
// -----------------------------------------------------------------------------

func collectSeqInto(dst []slotcache.Entry, cursor *slotcache.Cursor) []slotcache.Entry {
	// Keep nil cursor behavior compatible with Collect().
	if cursor == nil {
		return dst[:0]
	}

	dst = dst[:0]
	for entry := range cursor.Seq() {
		dst = append(dst, entry)
	}

	return dst
}

// CollectInto drains a cursor into dst and returns (entries, err).
//
// dst is reused if it has enough capacity.
func CollectInto(dst []slotcache.Entry, cursor *slotcache.Cursor) ([]slotcache.Entry, error) {
	entries := collectSeqInto(dst, cursor)
	if cursor == nil {
		return entries, nil
	}

	err := cursor.Err()
	if err != nil {
		return entries, fmt.Errorf("cursor error: %w", err)
	}

	return entries, nil
}

// Collect drains a cursor and returns its entries and error.
func Collect(cursor *slotcache.Cursor) ([]slotcache.Entry, error) {
	return CollectInto(nil, cursor)
}

func scanRealInto(tb testing.TB, cache slotcache.Cache, opts slotcache.ScanOptions, dst []slotcache.Entry) ([]slotcache.Entry, error) {
	tb.Helper()

	return CollectInto(dst, cache.Scan(opts))
}

func scanRealPrefixInto(tb testing.TB, cache slotcache.Cache, prefix []byte, opts slotcache.ScanOptions, dst []slotcache.Entry) ([]slotcache.Entry, error) {
	tb.Helper()

	return CollectInto(dst, cache.ScanPrefix(prefix, opts))
}

func scanModelInto(tb testing.TB, cache *model.CacheModel, opts slotcache.ScanOptions, dst []slotcache.Entry) ([]slotcache.Entry, error) {
	tb.Helper()

	entries, err := cache.Scan(opts)
	if err != nil {
		return nil, fmt.Errorf("model scan: %w", err)
	}

	return toEntriesInto(dst, entries), nil
}

func scanModelPrefixInto(tb testing.TB, cache *model.CacheModel, prefix []byte, opts slotcache.ScanOptions, dst []slotcache.Entry) ([]slotcache.Entry, error) {
	tb.Helper()

	entries, err := cache.ScanPrefix(prefix, opts)
	if err != nil {
		return nil, fmt.Errorf("model scan prefix: %w", err)
	}

	return toEntriesInto(dst, entries), nil
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

func entriesSliceEqual(leftEntries, rightEntries []slotcache.Entry) bool {
	if len(leftEntries) != len(rightEntries) {
		return false
	}

	for i := range leftEntries {
		if !entriesEqual(leftEntries[i], rightEntries[i]) {
			return false
		}
	}

	return true
}

// DiffEntries returns a human-readable diff if entries differ, empty string if equal.
// Uses fast equality check first to avoid reflection overhead on success.
func DiffEntries(leftEntries, rightEntries []slotcache.Entry) string {
	if entriesSliceEqual(leftEntries, rightEntries) {
		return ""
	}

	return cmp.Diff(leftEntries, rightEntries, cmpopts.EquateEmpty())
}

// DiffEntry returns a human-readable diff if entries differ, empty string if equal.
func DiffEntry(leftEntry, rightEntry slotcache.Entry) string {
	if entriesEqual(leftEntry, rightEntry) {
		return ""
	}

	return cmp.Diff(leftEntry, rightEntry, cmpopts.EquateEmpty())
}

// -----------------------------------------------------------------------------
// Observable state comparison
// -----------------------------------------------------------------------------

// CompareState compares all publicly observable state between model and real.
//
// Performs redundant checks intentionally:
// - Len() must match
// - Forward scan must match
// - Reverse scan must match
// - Forward/reverse must be exact reverses of each other
// - Get() for each observed key must match
// - Prefix scans for each observed key's first byte must match.
func CompareState(tb testing.TB, harness *Harness) {
	tb.Helper()

	// Compare Len().
	mLen, mLenErr := harness.Model.Cache.Len()
	rLen, rLenErr := harness.Real.Cache.Len()

	if !errorsMatch(mLenErr, rLenErr) {
		tb.Fatalf("Len() error mismatch\nmodel=%v\nreal=%v", mLenErr, rLenErr)
	}

	if mLenErr != nil {
		return // Both closed: no more observable state to compare.
	}

	if mLen != rLen {
		tb.Fatalf("Len() value mismatch\nmodel=%d\nreal=%d", mLen, rLen)
	}

	// Compare forward scans.
	fwdOpts := slotcache.ScanOptions{Reverse: false, Offset: 0, Limit: 0}
	revOpts := slotcache.ScanOptions{Reverse: true, Offset: 0, Limit: 0}

	expectedEntries := mLen

	harness.Scratch.modelFwd = ensureEntriesCapacity(harness.Scratch.modelFwd, expectedEntries)
	harness.Scratch.realFwd = ensureEntriesCapacity(harness.Scratch.realFwd, expectedEntries)

	mFwd, mFwdErr := scanModelInto(tb, harness.Model.Cache, fwdOpts, harness.Scratch.modelFwd)
	rFwd, rFwdErr := scanRealInto(tb, harness.Real.Cache, fwdOpts, harness.Scratch.realFwd)

	harness.Scratch.modelFwd = mFwd
	harness.Scratch.realFwd = rFwd

	if !errorsMatch(mFwdErr, rFwdErr) {
		tb.Fatalf("Scan(forward) error mismatch\nmodel=%v\nreal=%v", mFwdErr, rFwdErr)
	}

	if diff := DiffEntries(mFwd, rFwd); diff != "" {
		tb.Fatalf("Scan(forward) entries mismatch (-model +real):\n%s", diff)
	}

	// Verify Len() matches Scan() count.
	if mLen != len(mFwd) {
		tb.Fatalf("model: Len()=%d but Scan(forward) returned %d entries", mLen, len(mFwd))
	}

	if rLen != len(rFwd) {
		tb.Fatalf("real: Len()=%d but Scan(forward) returned %d entries", rLen, len(rFwd))
	}

	// Compare reverse scans.
	harness.Scratch.modelRev = ensureEntriesCapacity(harness.Scratch.modelRev, expectedEntries)
	harness.Scratch.realRev = ensureEntriesCapacity(harness.Scratch.realRev, expectedEntries)

	mRev, mRevErr := scanModelInto(tb, harness.Model.Cache, revOpts, harness.Scratch.modelRev)
	rRev, rRevErr := scanRealInto(tb, harness.Real.Cache, revOpts, harness.Scratch.realRev)

	harness.Scratch.modelRev = mRev
	harness.Scratch.realRev = rRev

	if !errorsMatch(mRevErr, rRevErr) {
		tb.Fatalf("Scan(reverse) error mismatch\nmodel=%v\nreal=%v", mRevErr, rRevErr)
	}

	if diff := DiffEntries(mRev, rRev); diff != "" {
		tb.Fatalf("Scan(reverse) entries mismatch (-model +real):\n%s", diff)
	}

	// Verify forward/reverse are exact reverses.
	if !entriesAreReverse(mFwd, mRev) {
		tb.Fatal("model: reverse scan is not the exact reverse of forward scan")
	}

	if !entriesAreReverse(rFwd, rRev) {
		tb.Fatal("real: reverse scan is not the exact reverse of forward scan")
	}

	// Filter cross-check: Scan with KeyPrefixEq filter must match ScanPrefix
	if len(mFwd) > 0 {
		var prefixArr [1]byte

		prefixArr[0] = mFwd[0].Key[0]
		prefix := prefixArr[:]
		spec := FilterSpec{Kind: FilterKeyPrefixEq, Prefix: prefix}

		filteredOpts := fwdOpts
		filteredOpts.Filter = BuildFilter(spec)

		harness.Scratch.modelTmp1 = ensureEntriesCapacity(harness.Scratch.modelTmp1, expectedEntries)
		harness.Scratch.realTmp1 = ensureEntriesCapacity(harness.Scratch.realTmp1, expectedEntries)

		mFiltered, mFilteredErr := scanModelInto(tb, harness.Model.Cache, filteredOpts, harness.Scratch.modelTmp1)
		rFiltered, rFilteredErr := scanRealInto(tb, harness.Real.Cache, filteredOpts, harness.Scratch.realTmp1)

		harness.Scratch.modelTmp1 = mFiltered
		harness.Scratch.realTmp1 = rFiltered

		if !errorsMatch(mFilteredErr, rFilteredErr) {
			tb.Fatalf("Scan(filter=%s) error mismatch\nmodel=%v\nreal=%v", spec.String(), mFilteredErr, rFilteredErr)
		}

		harness.Scratch.modelTmp2 = ensureEntriesCapacity(harness.Scratch.modelTmp2, expectedEntries)
		harness.Scratch.realTmp2 = ensureEntriesCapacity(harness.Scratch.realTmp2, expectedEntries)

		mPfx, mPfxErr := scanModelPrefixInto(tb, harness.Model.Cache, prefix, fwdOpts, harness.Scratch.modelTmp2)
		rPfx, rPfxErr := scanRealPrefixInto(tb, harness.Real.Cache, prefix, fwdOpts, harness.Scratch.realTmp2)

		harness.Scratch.modelTmp2 = mPfx
		harness.Scratch.realTmp2 = rPfx

		if !errorsMatch(mPfxErr, rPfxErr) {
			tb.Fatalf("ScanPrefix(%x) error mismatch\nmodel=%v\nreal=%v", prefix, mPfxErr, rPfxErr)
		}

		if mFilteredErr == nil && mPfxErr == nil {
			if diff := DiffEntries(mFiltered, mPfx); diff != "" {
				tb.Fatalf("model: Scan(filter=%s) != ScanPrefix(%x):\n%s", spec.String(), prefix, diff)
			}

			if diff := DiffEntries(rFiltered, rPfx); diff != "" {
				tb.Fatalf("real: Scan(filter=%s) != ScanPrefix(%x):\n%s", spec.String(), prefix, diff)
			}
		}
	}

	// Compare some paging variants.
	pageOpts := []slotcache.ScanOptions{
		{Reverse: false, Offset: 0, Limit: 1},
		{Reverse: false, Offset: 1, Limit: 2},
		{Reverse: true, Offset: 1, Limit: 2},
	}

	for _, opts := range pageOpts {
		harness.Scratch.modelTmp1 = ensureEntriesCapacity(harness.Scratch.modelTmp1, expectedEntries)
		harness.Scratch.realTmp1 = ensureEntriesCapacity(harness.Scratch.realTmp1, expectedEntries)

		mPage, mPageErr := scanModelInto(tb, harness.Model.Cache, opts, harness.Scratch.modelTmp1)
		rPage, rPageErr := scanRealInto(tb, harness.Real.Cache, opts, harness.Scratch.realTmp1)

		harness.Scratch.modelTmp1 = mPage
		harness.Scratch.realTmp1 = rPage

		if !errorsMatch(mPageErr, rPageErr) {
			tb.Fatalf("Scan(%+v) error mismatch\nmodel=%v\nreal=%v", opts, mPageErr, rPageErr)
		}

		if diff := DiffEntries(mPage, rPage); diff != "" {
			tb.Fatalf("Scan(%+v) entries mismatch (-model +real):\n%s", opts, diff)
		}
	}

	// Cross-check Get() for all keys observed by Scan.
	//
	// Use byte slices directly to avoid []byte <-> string allocations.
	harness.Scratch.keysTmp = harness.Scratch.keysTmp[:0]
	for _, e := range mFwd {
		harness.Scratch.keysTmp = append(harness.Scratch.keysTmp, e.Key)
	}

	for _, e := range rFwd {
		harness.Scratch.keysTmp = append(harness.Scratch.keysTmp, e.Key)
	}

	sort.Slice(harness.Scratch.keysTmp, func(i, j int) bool {
		return bytes.Compare(harness.Scratch.keysTmp[i], harness.Scratch.keysTmp[j]) < 0
	})

	deduped := harness.Scratch.keysTmp[:0]
	for _, key := range harness.Scratch.keysTmp {
		if len(deduped) > 0 && bytes.Equal(deduped[len(deduped)-1], key) {
			continue
		}

		deduped = append(deduped, key)
	}

	harness.Scratch.keysTmp = deduped

	for _, key := range harness.Scratch.keysTmp {
		mEntry, mOk, mErr := harness.Model.Cache.Get(key)
		rEntry, rOk, rErr := harness.Real.Cache.Get(key)

		if !errorsMatch(mErr, rErr) {
			tb.Fatalf("Get(%x) error mismatch\nmodel=%v\nreal=%v", key, mErr, rErr)
		}

		if mOk != rOk {
			tb.Fatalf("Get(%x) exists mismatch\nmodel=%v\nreal=%v", key, mOk, rOk)
		}

		if diff := DiffEntry(toSlotcacheEntry(mEntry), rEntry); diff != "" {
			tb.Fatalf("Get(%x) entry mismatch (-model +real):\n%s", key, diff)
		}
	}

	// Prefix scan cross-check: for each distinct 1-byte prefix, ScanPrefix and ScanMatch must agree.
	//
	// We intentionally de-duplicate prefixes here: running the same prefix scan repeatedly for
	// multiple keys with the same first byte provides no additional coverage, but is very costly.
	var seenPrefixes [256]bool

	for _, e := range mFwd {
		if len(e.Key) < 1 {
			continue
		}

		prefixByte := e.Key[0]
		if seenPrefixes[prefixByte] {
			continue
		}

		seenPrefixes[prefixByte] = true

		var prefixArr [1]byte

		prefixArr[0] = prefixByte
		prefix := prefixArr[:]

		harness.Scratch.modelTmp1 = ensureEntriesCapacity(harness.Scratch.modelTmp1, expectedEntries)
		harness.Scratch.realTmp1 = ensureEntriesCapacity(harness.Scratch.realTmp1, expectedEntries)

		mPfx, mPfxErr := scanModelPrefixInto(tb, harness.Model.Cache, prefix, fwdOpts, harness.Scratch.modelTmp1)
		rPfx, rPfxErr := scanRealPrefixInto(tb, harness.Real.Cache, prefix, fwdOpts, harness.Scratch.realTmp1)

		harness.Scratch.modelTmp1 = mPfx
		harness.Scratch.realTmp1 = rPfx

		if !errorsMatch(mPfxErr, rPfxErr) {
			tb.Fatalf("ScanPrefix(%x) error mismatch\nmodel=%v\nreal=%v", prefix, mPfxErr, rPfxErr)
		}

		if diff := DiffEntries(mPfx, rPfx); diff != "" {
			tb.Fatalf("ScanPrefix(%x) entries mismatch (-model +real):\n%s", prefix, diff)
		}

		// ScanMatch with a byte-aligned, offset=0 Prefix must be equivalent to ScanPrefix.
		spec := slotcache.Prefix{Offset: 0, Bits: 0, Bytes: prefix}

		harness.Scratch.modelTmp2 = ensureEntriesCapacity(harness.Scratch.modelTmp2, expectedEntries)
		mMatch, mMatchErr := harness.Model.Cache.ScanMatch(spec, fwdOpts)

		var mMatchEntries []slotcache.Entry
		if mMatchErr == nil {
			mMatchEntries = toEntriesInto(harness.Scratch.modelTmp2, mMatch)
			harness.Scratch.modelTmp2 = mMatchEntries
		}

		harness.Scratch.realTmp2 = ensureEntriesCapacity(harness.Scratch.realTmp2, expectedEntries)
		rMatchEntries, rMatchErr := CollectInto(harness.Scratch.realTmp2, harness.Real.Cache.ScanMatch(spec, fwdOpts))
		harness.Scratch.realTmp2 = rMatchEntries

		if !errorsMatch(mMatchErr, rMatchErr) {
			tb.Fatalf("ScanMatch(%+v) error mismatch\nmodel=%v\nreal=%v", spec, mMatchErr, rMatchErr)
		}

		if mMatchErr == nil {
			harness.Scratch.modelTmp2 = mMatchEntries

			if diff := DiffEntries(mMatchEntries, rMatchEntries); diff != "" {
				tb.Fatalf("ScanMatch(%+v) entries mismatch (-model +real):\n%s", spec, diff)
			}

			if diff := DiffEntries(mPfx, mMatchEntries); diff != "" {
				tb.Fatalf("model: ScanPrefix(%x) != ScanMatch(%+v):\n%s", prefix, spec, diff)
			}

			if diff := DiffEntries(rPfx, rMatchEntries); diff != "" {
				tb.Fatalf("real: ScanPrefix(%x) != ScanMatch(%+v):\n%s", prefix, spec, diff)
			}
		}
	}

	// ScanRange cross-check: unbounded range must either equal Scan() (ordered mode)
	// or return ErrUnordered (unordered mode).
	mRange, mRangeErr := harness.Model.Cache.ScanRange(nil, nil, fwdOpts)

	harness.Scratch.realTmp1 = ensureEntriesCapacity(harness.Scratch.realTmp1, expectedEntries)
	rRangeEntries, rRangeErr := CollectInto(harness.Scratch.realTmp1, harness.Real.Cache.ScanRange(nil, nil, fwdOpts))
	harness.Scratch.realTmp1 = rRangeEntries

	if !errorsMatch(mRangeErr, rRangeErr) {
		tb.Fatalf("ScanRange(nil,nil) error mismatch\nmodel=%v\nreal=%v", mRangeErr, rRangeErr)
	}

	if mRangeErr != nil {
		if harness.Model.File.OrderedKeys {
			tb.Fatalf("ScanRange(nil,nil) unexpected error in ordered mode: %v", mRangeErr)
		}

		if !errors.Is(mRangeErr, slotcache.ErrUnordered) {
			tb.Fatalf("ScanRange(nil,nil) unexpected error: %v", mRangeErr)
		}

		return
	}

	harness.Scratch.modelTmp1 = ensureEntriesCapacity(harness.Scratch.modelTmp1, expectedEntries)
	mRangeEntries := toEntriesInto(harness.Scratch.modelTmp1, mRange)
	harness.Scratch.modelTmp1 = mRangeEntries

	if diff := DiffEntries(mFwd, mRangeEntries); diff != "" {
		tb.Fatalf("model: ScanRange(nil,nil) != Scan():\n%s", diff)
	}

	if diff := DiffEntries(rFwd, rRangeEntries); diff != "" {
		tb.Fatalf("real: ScanRange(nil,nil) != Scan():\n%s", diff)
	}

	if len(mFwd) > 0 {
		end := mFwd[len(mFwd)/2].Key

		mHead, mHeadErr := harness.Model.Cache.ScanRange(nil, end, fwdOpts)

		harness.Scratch.realTmp2 = ensureEntriesCapacity(harness.Scratch.realTmp2, expectedEntries)
		rHeadEntries, rHeadErr := CollectInto(harness.Scratch.realTmp2, harness.Real.Cache.ScanRange(nil, end, fwdOpts))
		harness.Scratch.realTmp2 = rHeadEntries

		if !errorsMatch(mHeadErr, rHeadErr) {
			tb.Fatalf("ScanRange(nil,%x) error mismatch\nmodel=%v\nreal=%v", end, mHeadErr, rHeadErr)
		}

		if mHeadErr == nil {
			harness.Scratch.modelTmp2 = ensureEntriesCapacity(harness.Scratch.modelTmp2, expectedEntries)
			mHeadEntries := toEntriesInto(harness.Scratch.modelTmp2, mHead)
			harness.Scratch.modelTmp2 = mHeadEntries
			expected := make([]slotcache.Entry, 0, len(mFwd))

			for _, e := range mFwd {
				if bytes.Compare(e.Key, end) < 0 {
					expected = append(expected, e)
				}
			}

			if diff := DiffEntries(expected, mHeadEntries); diff != "" {
				tb.Fatalf("model: ScanRange(nil,%x) mismatch with filtered Scan():\n%s", end, diff)
			}

			if diff := DiffEntries(expected, rHeadEntries); diff != "" {
				tb.Fatalf("real: ScanRange(nil,%x) mismatch with filtered Scan():\n%s", end, diff)
			}
		}
	}
}
