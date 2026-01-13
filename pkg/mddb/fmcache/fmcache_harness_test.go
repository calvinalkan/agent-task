//go:build fmcache_impl

package fmcache_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/calvinalkan/agent-task/pkg/mddb/fmcache"
)

// Test item using all field types.
type TestItem struct {
	Status   uint8  // Uint8Field
	Priority uint16 // Uint16Field
	Active   bool   // BoolField
	Tag      string // StringField (in index)
	Assignee string // StringField (in index)
	Name     string // variable data
}

// Schema for real cache - uses all field types.
var testFields = fmcache.Fields{
	fmcache.Uint8Field("status"),
	fmcache.Uint16Field("priority"),
	fmcache.BoolField("active"),
	fmcache.StringField("tag", 8),
	fmcache.StringField("assignee", 8),
}

var testSchema = fmcache.Schema[TestItem]{
	Encode: func(item *TestItem) (index, data []byte, err error) {
		w := testFields.NewWriter()
		w.SetUint8("status", item.Status)
		w.SetUint16("priority", item.Priority)
		w.SetBool("active", item.Active)
		w.SetString("tag", item.Tag)
		w.SetString("assignee", item.Assignee)
		return w.Bytes(), []byte(item.Name), nil
	},
	Decode: func(index, data []byte) (TestItem, error) {
		r := testFields.NewReader(index)
		return TestItem{
			Status:   r.Uint8("status"),
			Priority: r.Uint16("priority"),
			Active:   r.Bool("active"),
			Tag:      r.String("tag"),
			Assignee: r.String("assignee"),
			Name:     string(data),
		}, nil
	},
}

// Operations.
type PutOp struct {
	Key   string
	Rev   int64
	Value TestItem
}

type DeleteOp struct{ Key string }

type GetOp struct{ Key string }

type CommitReopenOp struct{}

type CloseOp struct{}

type FilterOp struct {
	Name string
	Opts fmcache.FilterOpts
}

// Result types.
//
// Each op returns a typed result. This keeps comparisons explicit and avoids
// `any` casting in tests.
type opResult interface{ isOpResult() }

type errResult struct{ Err error }

func (errResult) isOpResult() {}

type getOpResult struct {
	Entry  fmcache.Entry[TestItem]
	Exists bool
	Err    error
}

func (getOpResult) isOpResult() {}

type deleteOpResult struct {
	Existed bool
	Err     error
}

func (deleteOpResult) isOpResult() {}

type filterOpResult struct {
	Entries []fmcache.Entry[TestItem]
	Err     error
}

func (filterOpResult) isOpResult() {}

// Apply operation to spec.
func applySpec(s *fmcache.Spec[TestItem], op any) (opResult, *fmcache.Spec[TestItem]) {
	switch o := op.(type) {
	case PutOp:
		err := s.Put(o.Key, o.Rev, o.Value)
		return errResult{Err: err}, s
	case DeleteOp:
		existed, err := s.Delete(o.Key)
		return deleteOpResult{Existed: existed, Err: err}, s
	case GetOp:
		e, ok, err := s.Get(o.Key)
		return getOpResult{Entry: e, Exists: ok, Err: err}, s
	case CommitReopenOp:
		err := s.Commit()
		if err != nil {
			return errResult{Err: err}, s
		}
		newS, err := s.Reopen()
		if err != nil {
			return errResult{Err: err}, s
		}
		return errResult{Err: nil}, newS
	case CloseOp:
		err := s.Close()
		return errResult{Err: err}, s
	case FilterOp:
		seq, err := s.FilterEntries(o.Opts, filterPred(o.Name))
		if err != nil {
			return filterOpResult{Err: err}, s
		}
		return filterOpResult{Entries: slices.Collect(seq), Err: nil}, s
	default:
		panic("unknown op")
	}
}

// Apply operation to real cache.
func applyReal(c *fmcache.Cache[TestItem], path string, op any) (opResult, *fmcache.Cache[TestItem]) {
	switch o := op.(type) {
	case PutOp:
		err := c.Put(o.Key, o.Rev, o.Value)
		return errResult{Err: err}, c
	case DeleteOp:
		existed, err := c.Delete(o.Key)
		return deleteOpResult{Existed: existed, Err: err}, c
	case GetOp:
		e, ok, err := c.Get(o.Key)
		return getOpResult{Entry: e, Exists: ok, Err: err}, c
	case CommitReopenOp:
		err := c.Commit()
		if err != nil {
			return errResult{Err: err}, c
		}
		_ = c.Close()
		newC, err := fmcache.Open(path, testSchema)
		if err != nil {
			return errResult{Err: err}, c
		}
		return errResult{Err: nil}, newC
	case CloseOp:
		err := c.Close()
		return errResult{Err: err}, c
	case FilterOp:
		seq, err := c.FilterEntries(o.Opts, filterPred(o.Name))
		if err != nil {
			return filterOpResult{Err: err}, c
		}
		return filterOpResult{Entries: slices.Collect(seq), Err: nil}, c
	default:
		panic("unknown op")
	}
}

func assertSortedKeys(t *testing.T, entries []fmcache.Entry[TestItem], reverse bool) {
	t.Helper()
	for i := 1; i < len(entries); i++ {
		prev := entries[i-1].Key
		curr := entries[i].Key
		if reverse {
			if prev <= curr {
				t.Fatalf("expected keys in descending order at %d: %q then %q", i, prev, curr)
			}
		} else {
			if prev >= curr {
				t.Fatalf("expected keys in ascending order at %d: %q then %q", i, prev, curr)
			}
		}
	}
}

func filterPred(name string) func(fmcache.Entry[TestItem]) bool {
	switch name {
	case "active":
		return func(e fmcache.Entry[TestItem]) bool { return e.Value.Active }
	case "highPriority":
		return func(e fmcache.Entry[TestItem]) bool { return e.Value.Priority >= 1000 }
	case "tagAlpha":
		return func(e fmcache.Entry[TestItem]) bool { return e.Value.Tag == "alpha" }
	case "assigneeBob":
		return func(e fmcache.Entry[TestItem]) bool { return e.Value.Assignee == "bob" }
	default:
		panic("unknown predicate: " + name)
	}
}

// Compare errors (both nil, or both same error type).
func sameError(specErr, realErr error) bool {
	if specErr == nil && realErr == nil {
		return true
	}
	if specErr == nil || realErr == nil {
		return false
	}
	return errors.Is(realErr, specErr) || errors.Is(specErr, realErr)
}

// Compare state between spec and real.
func compareState(t *testing.T, spec *fmcache.Spec[TestItem], real *fmcache.Cache[TestItem]) {
	t.Helper()

	specLen, specErr := spec.Len()
	realLen, realErr := real.Len()
	if !sameError(specErr, realErr) {
		t.Fatalf("Len() error mismatch: spec=%v real=%v", specErr, realErr)
	}
	if specLen != realLen {
		t.Fatalf("Len() mismatch: spec=%d real=%d", specLen, realLen)
	}

	specAll, specErr := spec.AllEntries(fmcache.FilterOpts{})
	realAll, realErr := real.AllEntries(fmcache.FilterOpts{})
	if !sameError(specErr, realErr) {
		t.Fatalf("AllEntries() error mismatch: spec=%v real=%v", specErr, realErr)
	}
	if specErr != nil {
		return
	}

	specEntries := slices.Collect(specAll)
	realEntries := slices.Collect(realAll)
	assertSortedKeys(t, specEntries, false)
	assertSortedKeys(t, realEntries, false)
	if diff := cmp.Diff(specEntries, realEntries); diff != "" {
		t.Fatalf("AllEntries() mismatch (-spec +real):\n%s", diff)
	}

	filterOpts := []fmcache.FilterOpts{
		{Reverse: false, Offset: 0, Limit: 0},
		{Reverse: true, Offset: 0, Limit: 0},
		{Reverse: false, Offset: 0, Limit: 1},
		{Reverse: false, Offset: 1, Limit: 2},
		{Reverse: true, Offset: 1, Limit: 2},
	}
	for _, opts := range filterOpts {
		specSeq, specErr := spec.AllEntries(opts)
		realSeq, realErr := real.AllEntries(opts)
		if !sameError(specErr, realErr) {
			t.Fatalf("AllEntries(%+v) error mismatch: spec=%v real=%v", opts, specErr, realErr)
		}
		if specErr != nil {
			continue
		}
		specFiltered := slices.Collect(specSeq)
		realFiltered := slices.Collect(realSeq)
		assertSortedKeys(t, specFiltered, opts.Reverse)
		assertSortedKeys(t, realFiltered, opts.Reverse)
		if diff := cmp.Diff(specFiltered, realFiltered); diff != "" {
			t.Fatalf("AllEntries(%+v) mismatch (-spec +real):\n%s", opts, diff)
		}
	}

	predicates := []string{"active", "highPriority", "tagAlpha", "assigneeBob"}
	filterOpts2 := []fmcache.FilterOpts{
		{Reverse: false, Offset: 0, Limit: 0},
		{Reverse: true, Offset: 0, Limit: 0},
		{Reverse: false, Offset: 1, Limit: 2},
	}
	for _, predName := range predicates {
		pred := filterPred(predName)
		for _, opts := range filterOpts2 {
			specSeq, specErr := spec.FilterEntries(opts, pred)
			realSeq, realErr := real.FilterEntries(opts, pred)
			if !sameError(specErr, realErr) {
				t.Fatalf("FilterEntries(%s,%+v) error mismatch: spec=%v real=%v", predName, opts, specErr, realErr)
			}
			if specErr == nil {
				specFiltered := slices.Collect(specSeq)
				realFiltered := slices.Collect(realSeq)
				assertSortedKeys(t, specFiltered, opts.Reverse)
				assertSortedKeys(t, realFiltered, opts.Reverse)
				if diff := cmp.Diff(specFiltered, realFiltered); diff != "" {
					t.Fatalf("FilterEntries(%s,%+v) mismatch (-spec +real):\n%s", predName, opts, diff)
				}
			}

			if predName == "active" {
				specAllSeq, specAllErr := spec.AllEntries(opts)
				realAllSeq, realAllErr := real.AllEntries(opts)
				if !sameError(specAllErr, realAllErr) {
					t.Fatalf("AllEntries(%+v) error mismatch: spec=%v real=%v", opts, specAllErr, realAllErr)
				}
				if specAllErr == nil {
					specAll := slices.Collect(specAllSeq)
					realAll := slices.Collect(realAllSeq)
					assertSortedKeys(t, specAll, opts.Reverse)
					assertSortedKeys(t, realAll, opts.Reverse)

					specMatchAllSeq, specMatchAllErr := spec.FilterEntries(opts, func(fmcache.Entry[TestItem]) bool { return true })
					realMatchAllSeq, realMatchAllErr := real.FilterEntries(opts, func(fmcache.Entry[TestItem]) bool { return true })
					if !sameError(specMatchAllErr, realMatchAllErr) {
						t.Fatalf("FilterEntries(matchAll,%+v) error mismatch: spec=%v real=%v", opts, specMatchAllErr, realMatchAllErr)
					}
					if specMatchAllErr == nil {
						specMatchAll := slices.Collect(specMatchAllSeq)
						realMatchAll := slices.Collect(realMatchAllSeq)
						if diff := cmp.Diff(specAll, specMatchAll); diff != "" {
							t.Fatalf("spec: AllEntries(%+v) != FilterEntries(matchAll) (-all +filter):\n%s", opts, diff)
						}
						if diff := cmp.Diff(realAll, realMatchAll); diff != "" {
							t.Fatalf("real: AllEntries(%+v) != FilterEntries(matchAll) (-all +filter):\n%s", opts, diff)
						}
					}
				}
			}
		}
	}

	keys := make(map[string]bool)
	for _, e := range specEntries {
		keys[e.Key] = true
	}
	for _, e := range realEntries {
		keys[e.Key] = true
	}

	for key := range keys {
		specEntry, specOk, specErr := spec.Get(key)
		realEntry, realOk, realErr := real.Get(key)

		if !sameError(specErr, realErr) {
			t.Fatalf("Get(%q) error mismatch: spec=%v real=%v", key, specErr, realErr)
		}
		if specOk != realOk {
			t.Fatalf("Get(%q) exists mismatch: spec=%v real=%v", key, specOk, realOk)
		}
		if diff := cmp.Diff(specEntry, realEntry); diff != "" {
			t.Fatalf("Get(%q) mismatch (-spec +real):\n%s", key, diff)
		}
	}
}

func applyAndAssert(t *testing.T, spec **fmcache.Spec[TestItem], real **fmcache.Cache[TestItem], path string, op any, i int) {
	t.Helper()

	specResult, newSpec := applySpec(*spec, op)
	realResult, newReal := applyReal(*real, path, op)
	*spec = newSpec
	*real = newReal

	switch sr := specResult.(type) {
	case errResult:
		rr := realResult.(errResult)
		if !sameError(sr.Err, rr.Err) {
			t.Fatalf("op %d %T error mismatch: spec=%v real=%v", i, op, sr.Err, rr.Err)
		}
		if sr.Err == nil {
			compareState(t, *spec, *real)
		}
	case getOpResult:
		rr := realResult.(getOpResult)
		if !sameError(sr.Err, rr.Err) {
			t.Fatalf("op %d %T error mismatch: spec=%v real=%v", i, op, sr.Err, rr.Err)
		}
		if sr.Err == nil {
			if diff := cmp.Diff(sr, rr); diff != "" {
				t.Fatalf("op %d %T result mismatch (-spec +real):\n%s", i, op, diff)
			}
			compareState(t, *spec, *real)
		}
	case deleteOpResult:
		rr := realResult.(deleteOpResult)
		if !sameError(sr.Err, rr.Err) {
			t.Fatalf("op %d %T error mismatch: spec=%v real=%v", i, op, sr.Err, rr.Err)
		}
		if sr.Err == nil {
			if diff := cmp.Diff(sr, rr); diff != "" {
				t.Fatalf("op %d %T result mismatch (-spec +real):\n%s", i, op, diff)
			}
			compareState(t, *spec, *real)
		}
	case filterOpResult:
		rr := realResult.(filterOpResult)
		if !sameError(sr.Err, rr.Err) {
			t.Fatalf("op %d %T error mismatch: spec=%v real=%v", i, op, sr.Err, rr.Err)
		}
		if sr.Err == nil {
			if diff := cmp.Diff(sr.Entries, rr.Entries); diff != "" {
				t.Fatalf("op %d %T result mismatch (-spec +real):\n%s", i, op, diff)
			}
			compareState(t, *spec, *real)
		}
	default:
		panic("unknown result type")
	}
}
