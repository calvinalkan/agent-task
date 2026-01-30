package mddb_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/fs"
	"github.com/calvinalkan/agent-task/pkg/mddb"
)

func Test_Get_Succeeds_When_Shared_Lock_Held(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir, withTestLockTimeout())

	defer func() { _ = s.Close() }()

	doc := newTestDoc(t, "Test Doc")
	createTestDoc(t.Context(), t, s, doc)

	// Hold a shared lock - Get should still succeed
	locker := fs.NewLocker(fs.NewReal())
	walPath := filepath.Join(dir, ".mddb", "wal")

	lock, err := locker.RLock(walPath)
	if err != nil {
		t.Fatalf("acquire shared lock: %v", err)
	}

	defer func() { _ = lock.Close() }()

	got, err := s.Get(t.Context(), doc.DocID)
	if err != nil {
		t.Fatalf("get while shared lock held: %v", err)
	}

	if got.ID() != doc.DocID {
		t.Fatalf("id = %s, want %s", got.ID(), doc.DocID)
	}
}

func Test_Get_Returns_Error_When_Exclusive_Lock_Held(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir, withTestLockTimeout())

	defer func() { _ = s.Close() }()

	doc := newTestDoc(t, "Test Doc")
	createTestDoc(t.Context(), t, s, doc)

	// Hold an exclusive lock - Get should block and timeout
	locker := fs.NewLocker(fs.NewReal())
	walPath := filepath.Join(dir, ".mddb", "wal")

	lock, err := locker.Lock(walPath)
	if err != nil {
		t.Fatalf("acquire exclusive lock: %v", err)
	}

	defer func() { _ = lock.Close() }()

	_, err = s.Get(t.Context(), doc.DocID)
	if err == nil {
		t.Fatal("expected error when exclusive lock held")
	}

	if !isDeadlineExceeded(err) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
}

func Test_Query_Succeeds_When_Shared_Lock_Held(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir, withTestLockTimeout())

	defer func() { _ = s.Close() }()

	doc := newTestDoc(t, "Test Doc")
	createTestDoc(t.Context(), t, s, doc)

	// Hold a shared lock - Query should still succeed
	locker := fs.NewLocker(fs.NewReal())
	walPath := filepath.Join(dir, ".mddb", "wal")

	lock, err := locker.RLock(walPath)
	if err != nil {
		t.Fatalf("acquire shared lock: %v", err)
	}

	defer func() { _ = lock.Close() }()

	count, err := mddb.Query(t.Context(), s, func(db *sql.DB) (int, error) {
		var n int

		scanErr := db.QueryRow("SELECT COUNT(*) FROM " + testTableName).Scan(&n)

		return n, scanErr
	})
	if err != nil {
		t.Fatalf("query while shared lock held: %v", err)
	}

	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
}

func Test_Query_Returns_Error_When_Exclusive_Lock_Held(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir, withTestLockTimeout())

	defer func() { _ = s.Close() }()

	// Hold an exclusive lock - Query should block and timeout
	locker := fs.NewLocker(fs.NewReal())
	walPath := filepath.Join(dir, ".mddb", "wal")

	lock, err := locker.Lock(walPath)
	if err != nil {
		t.Fatalf("acquire exclusive lock: %v", err)
	}

	defer func() { _ = lock.Close() }()

	_, err = mddb.Query(t.Context(), s, func(_ *sql.DB) (int, error) {
		return 0, nil
	})
	if err == nil {
		t.Fatal("expected error when exclusive lock held")
	}

	if !isDeadlineExceeded(err) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
}

func Test_GetByPrefix_Succeeds_When_Shared_Lock_Held(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir, withTestLockTimeout())

	defer func() { _ = s.Close() }()

	doc := newTestDoc(t, "Test Doc")
	createTestDoc(t.Context(), t, s, doc)

	// Hold a shared lock - GetByPrefix should still succeed
	locker := fs.NewLocker(fs.NewReal())
	walPath := filepath.Join(dir, ".mddb", "wal")

	lock, err := locker.RLock(walPath)
	if err != nil {
		t.Fatalf("acquire shared lock: %v", err)
	}

	defer func() { _ = lock.Close() }()

	results, err := s.GetByPrefix(t.Context(), doc.DocShort[:4])
	if err != nil {
		t.Fatalf("get by prefix while shared lock held: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
}

func Test_GetByPrefix_Returns_Error_When_Exclusive_Lock_Held(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir, withTestLockTimeout())

	defer func() { _ = s.Close() }()

	// Hold an exclusive lock - GetByPrefix should block and timeout
	locker := fs.NewLocker(fs.NewReal())
	walPath := filepath.Join(dir, ".mddb", "wal")

	lock, err := locker.Lock(walPath)
	if err != nil {
		t.Fatalf("acquire exclusive lock: %v", err)
	}

	defer func() { _ = lock.Close() }()

	_, err = s.GetByPrefix(t.Context(), "ABCD")
	if err == nil {
		t.Fatal("expected error when exclusive lock held")
	}

	if !isDeadlineExceeded(err) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
}

func Test_Get_Returns_Closed_When_Close_Concurrent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir, withTestLockTimeout())

	doc := newTestDoc(t, "Test Doc")
	createTestDoc(t.Context(), t, s, doc)

	start := make(chan struct{})
	done := make(chan struct{})

	go func() {
		<-start

		_ = s.Close()

		close(done)
	}()

	close(start)

	for range 50 {
		_, err := s.Get(t.Context(), doc.DocID)
		if err == nil {
			continue
		}

		if !errors.Is(err, mddb.ErrClosed) {
			t.Fatalf("get error = %v, want ErrClosed", err)
		}
	}

	<-done

	_, err := s.Get(t.Context(), doc.DocID)
	if err == nil || !errors.Is(err, mddb.ErrClosed) {
		t.Fatalf("get after close = %v, want ErrClosed", err)
	}
}

func Test_Query_Returns_Closed_When_Close_Concurrent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir, withTestLockTimeout())

	doc := newTestDoc(t, "Test Doc")
	createTestDoc(t.Context(), t, s, doc)

	start := make(chan struct{})
	done := make(chan struct{})

	go func() {
		<-start

		_ = s.Close()

		close(done)
	}()

	close(start)

	for range 50 {
		_, err := mddb.Query(t.Context(), s, func(db *sql.DB) (int, error) {
			var n int

			scanErr := db.QueryRow("SELECT COUNT(*) FROM " + testTableName).Scan(&n)

			return n, scanErr
		})
		if err == nil {
			continue
		}

		if !errors.Is(err, mddb.ErrClosed) {
			t.Fatalf("query error = %v, want ErrClosed", err)
		}
	}

	<-done

	_, err := mddb.Query(t.Context(), s, func(_ *sql.DB) (int, error) {
		return 0, nil
	})
	if err == nil || !errors.Is(err, mddb.ErrClosed) {
		t.Fatalf("query after close = %v, want ErrClosed", err)
	}
}

func Test_Begin_Returns_Closed_When_Close_Concurrent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir, withTestLockTimeout())

	start := make(chan struct{})
	done := make(chan struct{})

	go func() {
		<-start

		_ = s.Close()

		close(done)
	}()

	close(start)

	for range 50 {
		tx, err := s.Begin(t.Context())
		if err == nil {
			_ = tx.Rollback()

			continue
		}

		if !errors.Is(err, mddb.ErrClosed) {
			t.Fatalf("begin error = %v, want ErrClosed", err)
		}
	}

	<-done

	_, err := s.Begin(t.Context())
	if err == nil || !errors.Is(err, mddb.ErrClosed) {
		t.Fatalf("begin after close = %v, want ErrClosed", err)
	}
}

func Test_Multiple_Readers_Succeed_When_Called_Concurrently(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir, withTestLockTimeout())

	defer func() { _ = s.Close() }()

	doc := newTestDoc(t, "Test Doc")
	createTestDoc(t.Context(), t, s, doc)

	const numReaders = 5

	errs := make(chan error, numReaders*2)

	// Spawn concurrent Get calls
	for range numReaders {
		go func() {
			_, err := s.Get(t.Context(), doc.DocID)
			errs <- err
		}()
	}

	// Spawn concurrent Query calls
	for range numReaders {
		go func() {
			_, err := mddb.Query(t.Context(), s, func(db *sql.DB) (int, error) {
				var n int

				err := db.QueryRow("SELECT COUNT(*) FROM " + testTableName).Scan(&n)

				return n, err
			})
			errs <- err
		}()
	}

	for range numReaders * 2 {
		err := <-errs
		if err != nil {
			t.Errorf("concurrent read failed: %v", err)
		}
	}
}

func isDeadlineExceeded(err error) bool {
	return err != nil && (errors.Is(err, context.DeadlineExceeded) ||
		(err.Error() != "" && contains(err.Error(), "deadline exceeded")))
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || s != "" && containsAt(s, substr, 0))
}

func containsAt(s, substr string, start int) bool {
	for i := start; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}

	return false
}
