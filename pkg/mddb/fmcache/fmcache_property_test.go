//go:build fmcache_impl

package fmcache_test

import (
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/mddb/fmcache"
)

func Test_Cache_Matches_Spec_When_Operations_Applied(t *testing.T) {
	t.Parallel()
	spec := fmcache.NewSpec[TestItem]()
	path := filepath.Join(t.TempDir(), "test.cache")
	real, err := fmcache.Open(path, testSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = real.Close() }()

	opset := []any{
		// Test all field types
		PutOp{"a", 100, TestItem{Status: 1, Priority: 1000, Active: true, Tag: "alpha", Assignee: "bob", Name: "Alice"}},
		PutOp{"b", 200, TestItem{Status: 0, Priority: 500, Active: false, Tag: "beta", Assignee: "alice", Name: "Bob"}},
		FilterOp{Name: "active", Opts: fmcache.FilterOpts{}},
		FilterOp{Name: "highPriority", Opts: fmcache.FilterOpts{}},
		GetOp{"a"},
		GetOp{"nonexistent"},
		DeleteOp{"a"},
		DeleteOp{"a"},
		FilterOp{Name: "tagAlpha", Opts: fmcache.FilterOpts{}},
		GetOp{"a"},

		// Edge cases
		PutOp{"c", 300, TestItem{Status: 255, Priority: 65535, Active: true, Tag: "gamma", Assignee: "", Name: "Charlie"}},
		PutOp{"d", 400, TestItem{Status: 0, Priority: 0, Active: false, Tag: "", Assignee: "", Name: ""}},
		FilterOp{Name: "assigneeBob", Opts: fmcache.FilterOpts{Reverse: true, Offset: 0, Limit: 0}},
		FilterOp{Name: "active", Opts: fmcache.FilterOpts{Offset: -1, Limit: 0}},
		FilterOp{Name: "active", Opts: fmcache.FilterOpts{Offset: 0, Limit: -1}},
		FilterOp{Name: "active", Opts: fmcache.FilterOpts{Offset: 999999, Limit: 0}},
		CommitReopenOp{},
		FilterOp{Name: "active", Opts: fmcache.FilterOpts{Offset: 0, Limit: 1}},
		GetOp{"b"},
		GetOp{"c"},
		GetOp{"d"},

		// Update existing (last-write-wins)
		PutOp{"b", 201, TestItem{Status: 2, Priority: 999, Active: true, Tag: "updated", Assignee: "bob", Name: "Bob Updated"}},
		PutOp{"b", 202, TestItem{Status: 3, Priority: 1500, Active: false, Tag: "alpha", Assignee: "bob", Name: "Bob Updated Again"}},
		FilterOp{Name: "assigneeBob", Opts: fmcache.FilterOpts{Offset: 0, Limit: 2}},
		FilterOp{Name: "highPriority", Opts: fmcache.FilterOpts{}},
		GetOp{"b"},
		CommitReopenOp{},
		FilterOp{Name: "assigneeBob", Opts: fmcache.FilterOpts{Reverse: true, Offset: 0, Limit: 0}},
		GetOp{"b"},

		// Error cases
		PutOp{"", 500, TestItem{Name: "empty key"}},

		// Closed cache behavior
		CloseOp{},
		GetOp{"b"},
		DeleteOp{"b"},
		FilterOp{Name: "active", Opts: fmcache.FilterOpts{}},
		CommitReopenOp{},
	}

	for i, op := range opset {
		applyAndAssert(t, &spec, &real, path, op, i)
	}
}
