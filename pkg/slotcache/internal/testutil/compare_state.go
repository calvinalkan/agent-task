// compare_state.go provides helpers for comparing model vs real cache state.

package testutil

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

// CompareState performs exhaustive comparison of all observable state.
//
// Checks (intentionally redundant for thoroughness):
//   - Len() values and errors match
//   - Forward Scan() entries match
//   - Reverse Scan() entries match
//   - Forward/reverse are exact reverses of each other
//   - Get() for each observed key matches
//   - ScanPrefix() for each distinct first-byte prefix matches
//   - ScanMatch() equivalence with ScanPrefix()
//   - ScanRange() in ordered mode
//   - Paging (offset/limit) produces correct slices
//   - Filter cross-checks
//   - UserHeader() matches
func CompareState(tb testing.TB, harness *Harness) {
	tb.Helper()

	sliceByOffsetLimit := func(base []slotcache.Entry, opts slotcache.ScanOptions) []slotcache.Entry {
		if opts.Offset < 0 || opts.Limit < 0 {
			return nil
		}

		start := min(opts.Offset, len(base))

		end := len(base)
		if opts.Limit > 0 && start+opts.Limit < end {
			end = start + opts.Limit
		}

		return base[start:end]
	}

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
		spec := FilterSpec{Kind: filterKeyPrefixEq, Prefix: prefix}

		filteredOpts := fwdOpts
		filteredOpts.Filter = buildFilter(spec)

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

		// Metamorphic check: paging after filtering must equal slicing the full filtered result.
		pagedFiltered := filteredOpts
		pagedFiltered.Offset = 1
		pagedFiltered.Limit = 2

		harness.Scratch.modelTmp2 = ensureEntriesCapacity(harness.Scratch.modelTmp2, expectedEntries)
		harness.Scratch.realTmp2 = ensureEntriesCapacity(harness.Scratch.realTmp2, expectedEntries)

		mPaged, mPagedErr := scanModelInto(tb, harness.Model.Cache, pagedFiltered, harness.Scratch.modelTmp2)
		rPaged, rPagedErr := scanRealInto(tb, harness.Real.Cache, pagedFiltered, harness.Scratch.realTmp2)

		harness.Scratch.modelTmp2 = mPaged
		harness.Scratch.realTmp2 = rPaged

		if !errorsMatch(mPagedErr, rPagedErr) {
			tb.Fatalf("Scan(filter=%s,%+v) error mismatch\nmodel=%v\nreal=%v", spec.String(), pagedFiltered, mPagedErr, rPagedErr)
		}

		if mFilteredErr == nil && mPagedErr == nil {
			expected := sliceByOffsetLimit(mFiltered, pagedFiltered)
			if diff := DiffEntries(expected, mPaged); diff != "" {
				tb.Fatalf("model: Scan(filter=%s,%+v) != slice(Scan(filter=%s,%+v)):\n%s", spec.String(), pagedFiltered, spec.String(), filteredOpts, diff)
			}
		}

		if rFilteredErr == nil && rPagedErr == nil {
			expected := sliceByOffsetLimit(rFiltered, pagedFiltered)
			if diff := DiffEntries(expected, rPaged); diff != "" {
				tb.Fatalf("real: Scan(filter=%s,%+v) != slice(Scan(filter=%s,%+v)):\n%s", spec.String(), pagedFiltered, spec.String(), filteredOpts, diff)
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

		// Metamorphic check: paged Scan() must equal slicing the unpaged Scan().
		if mPageErr == nil {
			base := mFwd
			if opts.Reverse {
				base = mRev
			}

			expected := sliceByOffsetLimit(base, opts)
			if diff := DiffEntries(expected, mPage); diff != "" {
				tb.Fatalf("model: Scan(%+v) != slice(Scan(%+v)):\n%s", opts, fwdOpts, diff)
			}
		}

		if rPageErr == nil {
			base := rFwd
			if opts.Reverse {
				base = rRev
			}

			expected := sliceByOffsetLimit(base, opts)
			if diff := DiffEntries(expected, rPage); diff != "" {
				tb.Fatalf("real: Scan(%+v) != slice(Scan(%+v)):\n%s", opts, fwdOpts, diff)
			}
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
		rMatchEntries, rMatchErr := scanRealMatchInto(tb, harness.Real.Cache, spec, fwdOpts, harness.Scratch.realTmp2)
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
	rRangeEntries, rRangeErr := scanRealRangeInto(tb, harness.Real.Cache, nil, nil, fwdOpts, harness.Scratch.realTmp1)
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
		rHeadEntries, rHeadErr := scanRealRangeInto(tb, harness.Real.Cache, nil, end, fwdOpts, harness.Scratch.realTmp2)
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

	// Compare UserHeader().
	mHeader, mHeaderErr := harness.Model.Cache.UserHeader()
	rHeader, rHeaderErr := harness.Real.Cache.UserHeader()

	if !errorsMatch(mHeaderErr, rHeaderErr) {
		tb.Fatalf("UserHeader() error mismatch\nmodel=%v\nreal=%v", mHeaderErr, rHeaderErr)
	}

	if mHeaderErr == nil {
		if mHeader.Flags != rHeader.Flags {
			tb.Fatalf("UserHeader().Flags mismatch\nmodel=%d\nreal=%d", mHeader.Flags, rHeader.Flags)
		}

		if mHeader.Data != rHeader.Data {
			tb.Fatalf("UserHeader().Data mismatch\nmodel=%x\nreal=%x", mHeader.Data, rHeader.Data)
		}
	}
}

// CompareStateLight performs a fast subset of state comparison.
//
// Checks Len(), forward Scan(), and reverse Scan() only. Use this for
// frequent intermediate checks where full CompareState would be too slow.
func CompareStateLight(tb testing.TB, harness *Harness) {
	tb.Helper()

	mLen, mLenErr := harness.Model.Cache.Len()
	rLen, rLenErr := harness.Real.Cache.Len()

	if !errorsMatch(mLenErr, rLenErr) {
		tb.Fatalf("Len() error mismatch\nmodel=%v\nreal=%v", mLenErr, rLenErr)
	}

	if mLenErr != nil {
		return
	}

	if mLen != rLen {
		tb.Fatalf("Len() value mismatch\nmodel=%d\nreal=%d", mLen, rLen)
	}

	expectedEntries := mLen
	fwdOpts := slotcache.ScanOptions{Reverse: false, Offset: 0, Limit: 0}
	revOpts := slotcache.ScanOptions{Reverse: true, Offset: 0, Limit: 0}

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

	if mLen != len(mFwd) {
		tb.Fatalf("model: Len()=%d but Scan(forward) returned %d entries", mLen, len(mFwd))
	}

	if rLen != len(rFwd) {
		tb.Fatalf("real: Len()=%d but Scan(forward) returned %d entries", rLen, len(rFwd))
	}

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

	if !entriesAreReverse(mFwd, mRev) {
		tb.Fatal("model: reverse scan is not the exact reverse of forward scan")
	}

	if !entriesAreReverse(rFwd, rRev) {
		tb.Fatal("real: reverse scan is not the exact reverse of forward scan")
	}
}

// DiffEntries returns a diff string if slices differ, or "" if equal.
func DiffEntries(leftEntries, rightEntries []slotcache.Entry) string {
	if entriesSliceEqual(leftEntries, rightEntries) {
		return ""
	}

	return cmp.Diff(leftEntries, rightEntries, cmpopts.EquateEmpty())
}

// DiffEntry returns a diff string if entries differ, or "" if equal.
func DiffEntry(leftEntry, rightEntry slotcache.Entry) string {
	if entriesEqual(leftEntry, rightEntry) {
		return ""
	}

	return cmp.Diff(leftEntry, rightEntry, cmpopts.EquateEmpty())
}

// ToEntries converts model.Entry slice to slotcache.Entry slice for normalization.
func ToEntries(entries []model.Entry) []slotcache.Entry {
	return toEntriesInto(nil, entries)
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

// -----------------------------------------------------------------------------
// Scan helpers
// -----------------------------------------------------------------------------

func scanRealInto(tb testing.TB, cache *slotcache.Cache, opts slotcache.ScanOptions, dst []slotcache.Entry) ([]slotcache.Entry, error) {
	tb.Helper()

	entries, err := cache.Scan(opts)
	if err != nil {
		return nil, fmt.Errorf("real scan: %w", err)
	}

	dst = dst[:0]
	dst = append(dst, entries...)

	return dst, nil
}

func scanRealPrefixInto(tb testing.TB, cache *slotcache.Cache, prefix []byte, opts slotcache.ScanOptions, dst []slotcache.Entry) ([]slotcache.Entry, error) {
	tb.Helper()

	entries, err := cache.ScanPrefix(prefix, opts)
	if err != nil {
		return nil, fmt.Errorf("real scan prefix: %w", err)
	}

	dst = dst[:0]
	dst = append(dst, entries...)

	return dst, nil
}

func scanRealMatchInto(tb testing.TB, cache *slotcache.Cache, spec slotcache.Prefix, opts slotcache.ScanOptions, dst []slotcache.Entry) ([]slotcache.Entry, error) {
	tb.Helper()

	entries, err := cache.ScanMatch(spec, opts)
	if err != nil {
		return nil, fmt.Errorf("real scan match: %w", err)
	}

	dst = dst[:0]
	dst = append(dst, entries...)

	return dst, nil
}

func scanRealRangeInto(tb testing.TB, cache *slotcache.Cache, start, end []byte, opts slotcache.ScanOptions, dst []slotcache.Entry) ([]slotcache.Entry, error) {
	tb.Helper()

	entries, err := cache.ScanRange(start, end, opts)
	if err != nil {
		return nil, fmt.Errorf("real scan range: %w", err)
	}

	dst = dst[:0]
	dst = append(dst, entries...)

	return dst, nil
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
// Entry conversion helpers
// -----------------------------------------------------------------------------

func toSlotcacheEntry(me model.Entry) slotcache.Entry {
	return slotcache.Entry{
		Key:      me.Key,
		Revision: me.Revision,
		Index:    me.Index,
	}
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
