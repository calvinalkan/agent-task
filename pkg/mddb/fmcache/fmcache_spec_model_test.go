package fmcache_test

import (
	"errors"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/mddb/fmcache"
)

type Item struct {
	Status   uint8
	Priority uint8
	Name     string
}

func Test_Spec_Get_Returns_Entry_When_Key_Exists(t *testing.T) {
	t.Parallel()

	spec := fmcache.NewSpec[Item]()
	_ = spec.Put("a", 100, Item{Status: 1, Priority: 2, Name: "Alice"})

	entry, ok, err := spec.Get("a")
	if err != nil {
		t.Fatal(err)
	}

	if !ok || entry.Value.Name != "Alice" {
		t.Errorf("expected Alice, got %v", entry)
	}
}

func Test_Spec_Get_Returns_False_When_Key_Not_Exists(t *testing.T) {
	t.Parallel()

	spec := fmcache.NewSpec[Item]()

	_, ok, err := spec.Get("nonexistent")
	if err != nil {
		t.Fatal(err)
	}

	if ok {
		t.Error("expected false for nonexistent key")
	}
}

func Test_Spec_Get_Returns_False_When_Not_Committed(t *testing.T) {
	t.Parallel()

	spec := fmcache.NewSpec[Item]()
	_ = spec.Put("a", 100, Item{Name: "Alice"})
	// no commit
	spec, _ = spec.Reopen()

	_, ok, err := spec.Get("a")
	if err != nil {
		t.Fatal(err)
	}

	if ok {
		t.Error("uncommitted should not survive reopen")
	}
}

func Test_Spec_Get_Returns_Entry_When_Committed(t *testing.T) {
	t.Parallel()

	spec := fmcache.NewSpec[Item]()
	_ = spec.Put("a", 100, Item{Name: "Alice"})
	_ = spec.Commit()
	spec, _ = spec.Reopen()

	_, ok, err := spec.Get("a")
	if err != nil {
		t.Fatal(err)
	}

	if !ok {
		t.Error("committed should survive reopen")
	}
}

func Test_Spec_AllEntries_Returns_Alphabetical_Order_When_Iterated(t *testing.T) {
	t.Parallel()

	spec := fmcache.NewSpec[Item]()
	_ = spec.Put("foo", 1, Item{Name: "Foo"})
	_ = spec.Put("bar", 2, Item{Name: "Bar"})
	_ = spec.Put("zz", 3, Item{Name: "Zz"})
	_ = spec.Put("aaa", 4, Item{Name: "Aaa"})

	all, err := spec.AllEntries(fmcache.FilterOpts{})
	if err != nil {
		t.Fatal(err)
	}

	expected := []string{"aaa", "bar", "foo", "zz"}

	keys := make([]string, 0, len(expected))
	for e := range all {
		keys = append(keys, e.Key)
	}

	for i, k := range keys {
		if k != expected[i] {
			t.Errorf("expected %v, got %v", expected, keys)

			break
		}
	}
}

func Test_Spec_Returns_ErrClosed_When_Closed(t *testing.T) {
	t.Parallel()

	spec := fmcache.NewSpec[Item]()
	_ = spec.Close()

	_, err := spec.Len()
	if !errors.Is(err, fmcache.ErrClosed) {
		t.Errorf("expected ErrClosed, got %v", err)
	}
}

func Test_Spec_Put_Returns_ErrInvalidKey_When_Key_Empty(t *testing.T) {
	t.Parallel()

	spec := fmcache.NewSpec[Item]()

	err := spec.Put("", 100, Item{Name: "Alice"})
	if !errors.Is(err, fmcache.ErrInvalidKey) {
		t.Errorf("expected ErrInvalidKey, got %v", err)
	}
}

func Test_Spec_Delete_Returns_True_When_Key_Exists(t *testing.T) {
	t.Parallel()

	spec := fmcache.NewSpec[Item]()
	_ = spec.Put("a", 100, Item{Name: "Alice"})

	existed, err := spec.Delete("a")
	if err != nil {
		t.Fatal(err)
	}

	if !existed {
		t.Error("delete of existing key should return true")
	}
}

func Test_Spec_Delete_Returns_False_When_Key_Not_Exists(t *testing.T) {
	t.Parallel()

	spec := fmcache.NewSpec[Item]()

	existed, err := spec.Delete("nonexistent")
	if err != nil {
		t.Fatal(err)
	}

	if existed {
		t.Error("delete of nonexistent key should return false")
	}
}

func Test_Spec_Get_Returns_False_When_Key_Deleted(t *testing.T) {
	t.Parallel()

	spec := fmcache.NewSpec[Item]()
	_ = spec.Put("a", 100, Item{Name: "Alice"})
	_ = spec.Put("b", 200, Item{Name: "Bob"})
	_, _ = spec.Delete("a")
	_ = spec.Commit()
	spec, _ = spec.Reopen()

	_, ok, _ := spec.Get("a")
	if ok {
		t.Error("deleted entry should not exist")
	}

	_, ok, _ = spec.Get("b")
	if !ok {
		t.Error("non-deleted entry should exist")
	}
}
