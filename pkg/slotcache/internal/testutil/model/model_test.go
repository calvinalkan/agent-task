package model_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil/model"
)

func Test_ModelFile_Returns_Error_When_Options_Invalid(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		options slotcache.Options
	}{
		{
			name: "ZeroKeySize",
			options: slotcache.Options{
				KeySize:      0,
				IndexSize:    1,
				SlotCapacity: 1,
			},
		},
		{
			name: "NegativeKeySize",
			options: slotcache.Options{
				KeySize:      -1,
				IndexSize:    1,
				SlotCapacity: 1,
			},
		},
		{
			name: "NegativeIndexSize",
			options: slotcache.Options{
				KeySize:      1,
				IndexSize:    -1,
				SlotCapacity: 1,
			},
		},
		{
			name: "ZeroSlotCapacity",
			options: slotcache.Options{
				KeySize:      1,
				IndexSize:    0,
				SlotCapacity: 0,
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, err := model.NewFile(testCase.options)
			require.ErrorIs(t, err, slotcache.ErrInvalidInput, "NewFile should reject invalid options")
		})
	}
}

func Test_ModelFile_Returns_File_When_Options_Valid(t *testing.T) {
	t.Parallel()

	options := slotcache.Options{
		KeySize:      2,
		IndexSize:    2,
		SlotCapacity: 3,
	}

	fileState, err := model.NewFile(options)
	require.NoError(t, err, "NewFile should succeed with valid options")

	expected := &model.FileState{
		KeySize:      2,
		IndexSize:    2,
		SlotCapacity: 3,
		Slots:        nil,
	}

	diff := cmp.Diff(expected, fileState)
	assert.Empty(t, diff, "file state mismatch")
}

func Test_ModelFile_Clone_Returns_Nil_When_File_Is_Nil(t *testing.T) {
	t.Parallel()

	var fileState *model.FileState

	clone := fileState.Clone()
	assert.Nil(t, clone, "clone should be nil for a nil file")
}

// Test_ModelFile_Clone_Preserves_Nil_Slots_When_Slots_Is_Nil verifies that Clone()
// preserves the nil vs empty slice distinction. This matters because:
// 1. NewFile() returns Slots: nil
// 2. cmp.Diff treats nil and []T{} as different
// 3. Clone promises "exact same state" for metamorphic test comparisons.
func Test_ModelFile_Clone_Preserves_Nil_Slots_When_Slots_Is_Nil(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 3) // NewFile returns Slots: nil
	require.Nil(t, fileState.Slots, "precondition: fresh file should have nil Slots")

	clone := fileState.Clone()
	require.NotNil(t, clone, "clone should not be nil")

	diff := cmp.Diff(fileState, clone)
	assert.Empty(t, diff, "clone should be identical to original (including nil Slots)")
	assert.Nil(t, clone.Slots, "clone should preserve nil Slots")
}

func Test_ModelFile_Clone_Copies_Slots_When_File_Not_Nil(t *testing.T) {
	t.Parallel()

	fileState := &model.FileState{
		KeySize:      2,
		IndexSize:    2,
		SlotCapacity: 3,
		Slots: []model.SlotRecord{
			{
				KeyString:   "aa",
				IsLive:      true,
				Revision:    10,
				IndexString: "i1",
			},
		},
	}

	clone := fileState.Clone()
	require.NotNil(t, clone, "clone should not be nil")

	diff := cmp.Diff(fileState, clone)
	assert.Empty(t, diff, "clone mismatch")

	clone.Slots[0].KeyString = "zz"
	assert.NotEqual(t, "zz", fileState.Slots[0].KeyString, "clone mutation should not affect original")
}

func Test_ModelCache_Close_Returns_ErrBusy_When_Writer_Active(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 2)
	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")

	closeErr := cacheHandle.Close()

	require.ErrorIs(t, closeErr, slotcache.ErrBusy, "Close should fail while writer is active")
	writerSession.Close()
	require.NoError(t, cacheHandle.Close(), "Close should succeed after close")
}

func Test_ModelCache_BeginWrite_Returns_ErrBusy_When_Writer_Active(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 2)
	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")

	_, err = cacheHandle.BeginWrite()
	require.ErrorIs(t, err, slotcache.ErrBusy, "BeginWrite should reject concurrent writer")
	writerSession.Close()

	writerSession, err = cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed after close")
	writerSession.Close()
}

func Test_ModelCache_Returns_ErrClosed_When_Handle_Is_Closed(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		run  func(*model.CacheModel) error
		want error
	}{
		{
			name: "Len",
			run: func(cacheHandle *model.CacheModel) error {
				_, err := cacheHandle.Len()

				return err
			},
			want: slotcache.ErrClosed,
		},
		{
			name: "Get",
			run: func(cacheHandle *model.CacheModel) error {
				_, _, err := cacheHandle.Get([]byte("aa"))

				return err
			},
			want: slotcache.ErrClosed,
		},
		{
			name: "Scan",
			run: func(cacheHandle *model.CacheModel) error {
				_, err := cacheHandle.Scan(slotcache.ScanOptions{})

				return err
			},
			want: slotcache.ErrClosed,
		},
		{
			name: "ScanPrefix",
			run: func(cacheHandle *model.CacheModel) error {
				_, err := cacheHandle.ScanPrefix([]byte("a"), slotcache.ScanOptions{})

				return err
			},
			want: slotcache.ErrClosed,
		},
		{
			name: "BeginWrite",
			run: func(cacheHandle *model.CacheModel) error {
				_, err := cacheHandle.BeginWrite()

				return err
			},
			want: slotcache.ErrClosed,
		},
		{
			name: "CloseAgain",
			run: func(cacheHandle *model.CacheModel) error {
				return cacheHandle.Close()
			},
			want: nil, // Close is idempotent.
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fileState := newTestFile(t, 2)
			cacheHandle := model.Open(fileState)

			require.NoError(t, cacheHandle.Close(), "Close should succeed")

			err := testCase.run(cacheHandle)
			if testCase.want == nil {
				require.NoError(t, err, "operation should succeed")

				return
			}

			require.ErrorIs(t, err, testCase.want, "operation should fail once cache is closed")
		})
	}
}

func Test_ModelWriter_Defers_Visibility_When_Commit_Not_Called(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 2)
	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")

	require.NoError(t, writerSession.Put([]byte("aa"), 10, []byte("i1")), "Put should buffer")

	_, foundBeforeCommit, err := cacheHandle.Get([]byte("aa"))
	require.NoError(t, err, "Get should succeed before commit")
	require.False(t, foundBeforeCommit, "entry should be hidden before Commit")

	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	entryAfterCommit, foundAfterCommit, err := cacheHandle.Get([]byte("aa"))
	require.NoError(t, err, "Get should succeed after commit")
	require.True(t, foundAfterCommit, "entry should be visible after Commit")

	wantEntry := modelEntry("aa", 10, "i1")
	diff := cmp.Diff(wantEntry, entryAfterCommit)
	assert.Empty(t, diff, "unexpected entry")
}

func Test_ModelWriter_Returns_ErrInvalidInput_When_Index_Length_Wrong(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		index []byte
	}{
		{name: "IndexNil", index: nil},
		{name: "IndexTooShort", index: []byte("i")},
		{name: "IndexTooLong", index: []byte("iii")},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fileState := newTestFile(t, 2)
			cacheHandle := model.Open(fileState)

			writerSession, err := cacheHandle.BeginWrite()
			require.NoError(t, err, "BeginWrite should succeed")

			putErr := writerSession.Put([]byte("aa"), 1, testCase.index)
			require.ErrorIs(t, putErr, slotcache.ErrInvalidInput, "Put should reject invalid index length")

			writerSession.Close()
		})
	}
}

func Test_ModelWriter_Accepts_Empty_Index_When_IndexSize_Is_Zero(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		index []byte
	}{
		{name: "NilIndex", index: nil},
		{name: "EmptyIndex", index: []byte{}},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fileState, err := model.NewFile(slotcache.Options{
				KeySize:      2,
				IndexSize:    0,
				SlotCapacity: 1,
			})
			require.NoError(t, err, "NewFile should succeed")

			cacheHandle := model.Open(fileState)

			writerSession, err := cacheHandle.BeginWrite()
			require.NoError(t, err, "BeginWrite should succeed")
			require.NoError(t, writerSession.Put([]byte("aa"), 1, testCase.index), "Put should accept empty index")
			writerSession.Close()
		})
	}
}

func Test_ModelWriter_Returns_ErrInvalidInput_When_IndexSize_Is_Zero_And_Index_NonEmpty(t *testing.T) {
	t.Parallel()

	fileState, err := model.NewFile(slotcache.Options{
		KeySize:      2,
		IndexSize:    0,
		SlotCapacity: 1,
	})
	require.NoError(t, err, "NewFile should succeed")

	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")

	putErr := writerSession.Put([]byte("aa"), 1, []byte("i"))
	require.ErrorIs(t, putErr, slotcache.ErrInvalidInput, "Put should reject non-empty index")

	writerSession.Close()
}

func Test_ModelWriter_Uses_Last_Operation_When_Multiple_Ops_Buffered(t *testing.T) {
	t.Parallel()

	type bufferedOp struct {
		isPut    bool
		revision int64
		index    string
	}

	testCases := []struct {
		name      string
		ops       []bufferedOp
		wantFound bool
		wantEntry model.Entry
	}{
		{
			name: "FinalPut",
			ops: []bufferedOp{
				{isPut: true, revision: 1, index: "i1"},
				{isPut: false},
				{isPut: true, revision: 2, index: "i2"},
			},
			wantFound: true,
			wantEntry: modelEntry("aa", 2, "i2"),
		},
		{
			name: "FinalDelete",
			ops: []bufferedOp{
				{isPut: true, revision: 1, index: "i1"},
				{isPut: true, revision: 2, index: "i2"},
				{isPut: false},
			},
			wantFound: false,
		},
		{
			name: "PutAfterDelete",
			ops: []bufferedOp{
				{isPut: false},
				{isPut: true, revision: 3, index: "i3"},
			},
			wantFound: true,
			wantEntry: modelEntry("aa", 3, "i3"),
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fileState := newTestFile(t, 4)
			cacheHandle := model.Open(fileState)

			writerSession, err := cacheHandle.BeginWrite()
			require.NoError(t, err, "BeginWrite should succeed")

			for _, operation := range testCase.ops {
				if operation.isPut {
					require.NoError(t, writerSession.Put([]byte("aa"), operation.revision, []byte(operation.index)), "Put should succeed")

					continue
				}

				_, deleteErr := writerSession.Delete([]byte("aa"))
				require.NoError(t, deleteErr, "Delete should succeed")
			}

			require.NoError(t, writerSession.Commit(), "Commit should succeed")

			entryAfterCommit, foundAfterCommit, err := cacheHandle.Get([]byte("aa"))
			require.NoError(t, err, "Get should succeed")
			require.Equal(t, testCase.wantFound, foundAfterCommit, "found flag mismatch")

			if testCase.wantFound {
				diff := cmp.Diff(testCase.wantEntry, entryAfterCommit)
				assert.Empty(t, diff, "unexpected entry")
			}
		})
	}
}

func Test_ModelWriter_Updates_Slot_When_Key_Already_Live(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 3)
	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	require.NoError(t, writerSession.Put([]byte("aa"), 1, []byte("i1")), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	require.Len(t, fileState.Slots, 1, "expected one slot after first Put")

	writerSession, err = cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	require.NoError(t, writerSession.Put([]byte("aa"), 2, []byte("i2")), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	require.Len(t, fileState.Slots, 1, "expected slot to be updated in place")

	entryAfterCommit, foundAfterCommit, err := cacheHandle.Get([]byte("aa"))
	require.NoError(t, err, "Get should succeed")
	require.True(t, foundAfterCommit, "expected entry after update")

	wantEntry := modelEntry("aa", 2, "i2")
	diff := cmp.Diff(wantEntry, entryAfterCommit)
	assert.Empty(t, diff, "unexpected entry")
}

func Test_ModelWriter_Returns_ErrClosed_When_Called_After_Commit(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 2)
	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	require.NoError(t, writerSession.Put([]byte("aa"), 1, []byte("i1")), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	// Close is idempotent even after Commit.
	writerSession.Close()
	require.True(t, writerSession.IsClosed, "writer should be closed after Close()")

	// Second Close should also be a no-op (doesn't panic).
	require.NotPanics(t, func() { writerSession.Close() }, "second Close should not panic")

	// Put/Delete/Commit should return ErrClosed after Commit.
	testCases := []struct {
		name string
		run  func(*model.WriterModel) error
	}{
		{
			name: "Put",
			run: func(writerSession *model.WriterModel) error {
				return writerSession.Put([]byte("aa"), 2, []byte("i2"))
			},
		},
		{
			name: "Delete",
			run: func(writerSession *model.WriterModel) error {
				_, err := writerSession.Delete([]byte("aa"))

				return err
			},
		},
		{
			name: "Commit",
			run: func(writerSession *model.WriterModel) error {
				return writerSession.Commit()
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			err := testCase.run(writerSession)
			require.ErrorIs(t, err, slotcache.ErrClosed, "writer should be closed after commit")
		})
	}
}

func Test_ModelWriter_Delete_Returns_Presence_When_Buffered_State_Changes(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 4)
	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")

	require.NoError(t, writerSession.Put([]byte("aa"), 1, []byte("i1")), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	writerSession, err = cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")

	present, err := writerSession.Delete([]byte("aa"))
	require.NoError(t, err, "Delete should succeed")
	require.True(t, present, "expected Delete to report key present")

	present, err = writerSession.Delete([]byte("aa"))
	require.NoError(t, err, "Delete should succeed")
	require.False(t, present, "expected Delete to report key absent")

	require.NoError(t, writerSession.Put([]byte("aa"), 2, []byte("i2")), "Put should succeed")

	present, err = writerSession.Delete([]byte("aa"))
	require.NoError(t, err, "Delete should succeed")
	require.True(t, present, "expected Delete to report key present after buffered Put")

	writerSession.Close()
}

func Test_ModelWriter_Appends_Slot_When_Key_Reinserted(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 3)
	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	require.NoError(t, writerSession.Put([]byte("aa"), 1, []byte("i1")), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	require.Len(t, fileState.Slots, 1, "expected one slot after first Put")
	require.True(t, fileState.Slots[0].IsLive, "expected first slot to be live")

	writerSession, err = cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	_, err = writerSession.Delete([]byte("aa"))
	require.NoError(t, err, "Delete should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	require.Len(t, fileState.Slots, 1, "expected one slot after Delete")
	require.False(t, fileState.Slots[0].IsLive, "expected slot to be tombstoned")

	writerSession, err = cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	require.NoError(t, writerSession.Put([]byte("aa"), 2, []byte("i2")), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	require.Len(t, fileState.Slots, 2, "expected two slot records after reinsertion")
	require.False(t, fileState.Slots[0].IsLive, "expected original slot to remain tombstoned")
	require.True(t, fileState.Slots[1].IsLive, "expected new slot to be live")
	assert.Equal(t, "aa", fileState.Slots[1].KeyString, "expected new slot key to be preserved")
}

func Test_ModelWriter_Updates_In_Place_When_Delete_Then_Put_In_Same_Batch(t *testing.T) {
	t.Parallel()

	// This tests a subtle semantic: Delete → Put in the SAME batch for an already-live key
	// should result in an in-place update (slot count stays 1), NOT tombstone + new slot.
	// This contrasts with Test_ModelWriter_Appends_Slot_When_Key_Reinserted which uses
	// separate commits and results in 2 slots.
	fileState := newTestFile(t, 3)
	cacheHandle := model.Open(fileState)

	// First, create a live entry
	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	require.NoError(t, writerSession.Put([]byte("aa"), 1, []byte("i1")), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	require.Len(t, fileState.Slots, 1, "expected one slot after first Put")
	require.True(t, fileState.Slots[0].IsLive, "expected slot to be live")
	assert.Equal(t, int64(1), fileState.Slots[0].Revision, "expected initial revision")

	// Now Delete → Put in the SAME write session
	writerSession, err = cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")

	present, err := writerSession.Delete([]byte("aa"))
	require.NoError(t, err, "Delete should succeed")
	require.True(t, present, "Delete should report key was present")

	require.NoError(t, writerSession.Put([]byte("aa"), 2, []byte("i2")), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	// Key semantic: since the final op is Put and the key was live in committed state,
	// the slot should be updated in place (no tombstone, no new slot).
	require.Len(t, fileState.Slots, 1, "expected slot count to remain 1 (in-place update)")
	require.True(t, fileState.Slots[0].IsLive, "expected slot to remain live")
	assert.Equal(t, int64(2), fileState.Slots[0].Revision, "expected updated revision")
	assert.Equal(t, "i2", fileState.Slots[0].IndexString, "expected updated index")

	// Verify the entry is readable
	entry, found, err := cacheHandle.Get([]byte("aa"))
	require.NoError(t, err, "Get should succeed")
	require.True(t, found, "entry should be found")

	wantEntry := modelEntry("aa", 2, "i2")
	diff := cmp.Diff(wantEntry, entry)
	assert.Empty(t, diff, "entry should have updated values")
}

func Test_ModelWriter_Returns_ErrFull_When_SlotCapacity_Exceeded(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 1)
	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	require.NoError(t, writerSession.Put([]byte("aa"), 1, []byte("i1")), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	writerSession, err = cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")

	require.NoError(t, writerSession.Put([]byte("bb"), 2, []byte("i2")), "Put should succeed")

	commitErr := writerSession.Commit()
	require.ErrorIs(t, commitErr, slotcache.ErrFull, "Commit should return ErrFull when capacity exceeded")

	entries, scanErr := cacheHandle.Scan(slotcache.ScanOptions{})
	require.NoError(t, scanErr, "Scan should succeed")

	wantEntries := []model.Entry{modelEntry("aa", 1, "i1")}
	diff := cmp.Diff(wantEntries, entries)
	assert.Empty(t, diff, "unexpected entries")
}

func Test_ModelCache_Scan_Orders_And_Paginates_When_Options_Set(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 5)
	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")

	require.NoError(t, writerSession.Put([]byte("aa"), 1, []byte("i1")), "Put should succeed")
	require.NoError(t, writerSession.Put([]byte("ab"), 2, []byte("i2")), "Put should succeed")
	require.NoError(t, writerSession.Put([]byte("ba"), 3, []byte("i3")), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	entryAA := modelEntry("aa", 1, "i1")
	entryAB := modelEntry("ab", 2, "i2")
	entryBA := modelEntry("ba", 3, "i3")

	testCases := []struct {
		name   string
		prefix []byte
		opts   slotcache.ScanOptions
		want   []model.Entry
	}{
		{
			name: "ForwardAll",
			opts: slotcache.ScanOptions{},
			want: []model.Entry{entryAA, entryAB, entryBA},
		},
		{
			name: "ReverseAll",
			opts: slotcache.ScanOptions{Reverse: true},
			want: []model.Entry{entryBA, entryAB, entryAA},
		},
		{
			name:   "PrefixA",
			prefix: []byte("a"),
			opts:   slotcache.ScanOptions{},
			want:   []model.Entry{entryAA, entryAB},
		},
		{
			name:   "PrefixAReverse",
			prefix: []byte("a"),
			opts:   slotcache.ScanOptions{Reverse: true},
			want:   []model.Entry{entryAB, entryAA},
		},
		{
			name: "OffsetLimit",
			opts: slotcache.ScanOptions{Offset: 1, Limit: 1},
			want: []model.Entry{entryAB},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var (
				entries []model.Entry
				scanErr error
			)

			if testCase.prefix == nil {
				entries, scanErr = cacheHandle.Scan(testCase.opts)
			} else {
				entries, scanErr = cacheHandle.ScanPrefix(testCase.prefix, testCase.opts)
			}

			require.NoError(t, scanErr, "Scan should succeed")

			diff := cmp.Diff(testCase.want, entries)
			assert.Empty(t, diff, "unexpected entries")
		})
	}
}

func Test_ModelCache_Scan_Skips_Tombstones_When_Key_Deleted(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 4)
	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	require.NoError(t, writerSession.Put([]byte("aa"), 1, []byte("i1")), "Put should succeed")
	require.NoError(t, writerSession.Put([]byte("ab"), 2, []byte("i2")), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	writerSession, err = cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	_, err = writerSession.Delete([]byte("ab"))
	require.NoError(t, err, "Delete should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	entries, scanErr := cacheHandle.Scan(slotcache.ScanOptions{})
	require.NoError(t, scanErr, "Scan should succeed")

	wantEntries := []model.Entry{modelEntry("aa", 1, "i1")}
	diff := cmp.Diff(wantEntries, entries)
	assert.Empty(t, diff, "unexpected entries")
}

func Test_ModelCache_Returns_ErrInvalidInput_When_Key_Length_Wrong(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		run  func(*model.CacheModel) error
	}{
		{
			name: "GetNil",
			run: func(cacheHandle *model.CacheModel) error {
				_, _, err := cacheHandle.Get(nil)

				return err
			},
		},
		{
			name: "GetWrongLength",
			run: func(cacheHandle *model.CacheModel) error {
				_, _, err := cacheHandle.Get([]byte("a"))

				return err
			},
		},
		{
			name: "WriterPutWrongLength",
			run: func(cacheHandle *model.CacheModel) error {
				writerSession, err := cacheHandle.BeginWrite()
				if err != nil {
					return err
				}

				defer func() {
					writerSession.Close()
				}()

				return writerSession.Put([]byte("a"), 1, []byte("i1"))
			},
		},
		{
			name: "WriterDeleteWrongLength",
			run: func(cacheHandle *model.CacheModel) error {
				writerSession, err := cacheHandle.BeginWrite()
				if err != nil {
					return err
				}

				defer func() {
					writerSession.Close()
				}()

				_, err = writerSession.Delete([]byte("a"))

				return err
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fileState := newTestFile(t, 2)
			cacheHandle := model.Open(fileState)

			err := testCase.run(cacheHandle)
			require.ErrorIs(t, err, slotcache.ErrInvalidInput, "operation should reject invalid key")
		})
	}
}

func Test_ModelCache_Returns_ErrInvalidInput_When_Prefix_Invalid(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 2)
	cacheHandle := model.Open(fileState)

	testCases := []struct {
		name   string
		prefix []byte
	}{
		{name: "NilPrefix", prefix: nil},
		{name: "EmptyPrefix", prefix: []byte("")},
		{name: "TooLongPrefix", prefix: []byte("abc")},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, err := cacheHandle.ScanPrefix(testCase.prefix, slotcache.ScanOptions{})
			require.ErrorIs(t, err, slotcache.ErrInvalidInput, "ScanPrefix should reject invalid prefix")
		})
	}
}

func Test_ModelCache_Returns_ErrInvalidInput_When_Options_Invalid(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		run  func(*model.CacheModel) error
	}{
		{
			name: "ScanNegativeOffset",
			run: func(cacheHandle *model.CacheModel) error {
				_, err := cacheHandle.Scan(slotcache.ScanOptions{Offset: -1})

				return err
			},
		},
		{
			name: "ScanNegativeLimit",
			run: func(cacheHandle *model.CacheModel) error {
				_, err := cacheHandle.Scan(slotcache.ScanOptions{Limit: -1})

				return err
			},
		},
		{
			name: "ScanPrefixNegativeOffset",
			run: func(cacheHandle *model.CacheModel) error {
				_, err := cacheHandle.ScanPrefix([]byte("a"), slotcache.ScanOptions{Offset: -1})

				return err
			},
		},
		{
			name: "ScanPrefixNegativeLimit",
			run: func(cacheHandle *model.CacheModel) error {
				_, err := cacheHandle.ScanPrefix([]byte("a"), slotcache.ScanOptions{Limit: -1})

				return err
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fileState := newTestFile(t, 2)
			cacheHandle := model.Open(fileState)

			err := testCase.run(cacheHandle)
			require.ErrorIs(t, err, slotcache.ErrInvalidInput, "operation should reject invalid scan options")
		})
	}
}

func Test_ModelCache_Scan_Returns_Empty_When_Offset_Exceeds_Length(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 2)
	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	require.NoError(t, writerSession.Put([]byte("aa"), 1, []byte("i1")), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	entries, scanErr := cacheHandle.Scan(slotcache.ScanOptions{Offset: 2})
	require.NoError(t, scanErr, "Scan should succeed with offset beyond length")
	assert.Empty(t, entries, "Scan should return empty when offset exceeds length")
}

func Test_ModelCache_Scan_Returns_Empty_When_Offset_Equals_Length(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 3)
	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	require.NoError(t, writerSession.Put([]byte("aa"), 1, []byte("i1")), "Put should succeed")
	require.NoError(t, writerSession.Put([]byte("ab"), 2, []byte("i2")), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	entries, scanErr := cacheHandle.Scan(slotcache.ScanOptions{Offset: 2})
	require.NoError(t, scanErr, "Scan should succeed")

	var wantEntries []model.Entry

	diff := cmp.Diff(wantEntries, entries, cmpopts.EquateEmpty())
	assert.Empty(t, diff, "unexpected entries")
}

func Test_ModelCache_Len_Returns_LiveCount_When_Entries_Added_And_Deleted(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 5)
	cacheHandle := model.Open(fileState)

	length, err := cacheHandle.Len()
	require.NoError(t, err, "Len should succeed")
	assert.Equal(t, 0, length, "expected zero length for empty cache")

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	require.NoError(t, writerSession.Put([]byte("aa"), 1, []byte("i1")), "Put should succeed")
	require.NoError(t, writerSession.Put([]byte("ab"), 2, []byte("i2")), "Put should succeed")
	require.NoError(t, writerSession.Put([]byte("ba"), 3, []byte("i3")), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	length, err = cacheHandle.Len()
	require.NoError(t, err, "Len should succeed")
	assert.Equal(t, 3, length, "expected three live entries")

	writerSession, err = cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	_, err = writerSession.Delete([]byte("ab"))
	require.NoError(t, err, "Delete should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	length, err = cacheHandle.Len()
	require.NoError(t, err, "Len should succeed")
	assert.Equal(t, 2, length, "expected two live entries after delete")
}

func Test_ModelCache_Get_Returns_False_When_Key_Never_Inserted(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 2)
	cacheHandle := model.Open(fileState)

	entry, found, err := cacheHandle.Get([]byte("aa"))
	require.NoError(t, err, "Get should succeed")
	assert.False(t, found, "expected key not found")
	assert.Equal(t, model.Entry{}, entry, "expected zero-value entry")
}

func Test_ModelWriter_Delete_Returns_False_When_Key_Never_Existed(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 2)
	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")

	present, err := writerSession.Delete([]byte("aa"))
	require.NoError(t, err, "Delete should succeed")
	assert.False(t, present, "expected Delete to report key absent for never-inserted key")

	writerSession.Close()
}

func Test_ModelWriter_Returns_ErrClosed_When_Cache_Closed_Mid_Session(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 2)
	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")

	// Force-close the cache by setting IsClosed directly (simulating external close)
	cacheHandle.IsClosed = true

	putErr := writerSession.Put([]byte("aa"), 1, []byte("i1"))
	require.ErrorIs(t, putErr, slotcache.ErrClosed, "Put should fail when cache closed mid-session")

	_, deleteErr := writerSession.Delete([]byte("aa"))
	require.ErrorIs(t, deleteErr, slotcache.ErrClosed, "Delete should fail when cache closed mid-session")

	commitErr := writerSession.Commit()
	require.ErrorIs(t, commitErr, slotcache.ErrClosed, "Commit should fail when cache closed mid-session")
}

func Test_ModelCache_ScanPrefix_Returns_Empty_When_No_Match(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 3)
	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	require.NoError(t, writerSession.Put([]byte("aa"), 1, []byte("i1")), "Put should succeed")
	require.NoError(t, writerSession.Put([]byte("ab"), 2, []byte("i2")), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	entries, err := cacheHandle.ScanPrefix([]byte("z"), slotcache.ScanOptions{})
	require.NoError(t, err, "ScanPrefix should succeed")

	var wantEntries []model.Entry

	diff := cmp.Diff(wantEntries, entries, cmpopts.EquateEmpty())
	assert.Empty(t, diff, "expected empty result for non-matching prefix")
}

func Test_ModelCache_Scan_Returns_Empty_When_Cache_Empty(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 2)
	cacheHandle := model.Open(fileState)

	entries, err := cacheHandle.Scan(slotcache.ScanOptions{})
	require.NoError(t, err, "Scan should succeed on empty cache")

	var wantEntries []model.Entry

	diff := cmp.Diff(wantEntries, entries, cmpopts.EquateEmpty())
	assert.Empty(t, diff, "expected empty result for empty cache")
}

func Test_ModelWriter_Close_Discards_Buffered_Ops_When_Called_Before_Commit(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 2)
	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	require.NoError(t, writerSession.Put([]byte("aa"), 1, []byte("i1")), "Put should succeed")

	// Close() discards buffered ops.
	writerSession.Close()

	// Verify the buffered Put was discarded
	_, found, err := cacheHandle.Get([]byte("aa"))
	require.NoError(t, err, "Get should succeed")
	assert.False(t, found, "entry should not exist after Close discards buffered ops")

	// Verify we can start a new write session
	writerSession, err = cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed after Close")
	writerSession.Close()
}

func Test_ModelWriter_Preserves_Revision_When_Value_Is_Negative(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 2)
	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	require.NoError(t, writerSession.Put([]byte("aa"), -42, []byte("i1")), "Put should accept negative revision")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	entry, found, err := cacheHandle.Get([]byte("aa"))
	require.NoError(t, err, "Get should succeed")
	require.True(t, found, "entry should exist")
	assert.Equal(t, int64(-42), entry.Revision, "expected negative revision to be preserved")
}

func Test_ModelCache_ScanPrefix_Orders_And_Paginates_When_Options_Set(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 6)
	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")

	require.NoError(t, writerSession.Put([]byte("aa"), 1, []byte("i1")), "Put should succeed")
	require.NoError(t, writerSession.Put([]byte("ab"), 2, []byte("i2")), "Put should succeed")
	require.NoError(t, writerSession.Put([]byte("ac"), 3, []byte("i3")), "Put should succeed")
	require.NoError(t, writerSession.Put([]byte("ba"), 4, []byte("i4")), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	entryAA := modelEntry("aa", 1, "i1")
	entryAB := modelEntry("ab", 2, "i2")
	entryAC := modelEntry("ac", 3, "i3")

	testCases := []struct {
		name string
		opts slotcache.ScanOptions
		want []model.Entry
	}{
		{
			name: "OffsetOnly",
			opts: slotcache.ScanOptions{Offset: 1},
			want: []model.Entry{entryAB, entryAC},
		},
		{
			name: "LimitOnly",
			opts: slotcache.ScanOptions{Limit: 2},
			want: []model.Entry{entryAA, entryAB},
		},
		{
			name: "OffsetAndLimit",
			opts: slotcache.ScanOptions{Offset: 1, Limit: 1},
			want: []model.Entry{entryAB},
		},
		{
			name: "ReverseWithOffset",
			opts: slotcache.ScanOptions{Reverse: true, Offset: 1},
			want: []model.Entry{entryAB, entryAA},
		},
		{
			name: "ReverseWithLimit",
			opts: slotcache.ScanOptions{Reverse: true, Limit: 2},
			want: []model.Entry{entryAC, entryAB},
		},
		{
			name: "ReverseWithOffsetAndLimit",
			opts: slotcache.ScanOptions{Reverse: true, Offset: 1, Limit: 1},
			want: []model.Entry{entryAB},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			entries, scanErr := cacheHandle.ScanPrefix([]byte("a"), testCase.opts)
			require.NoError(t, scanErr, "ScanPrefix should succeed")

			diff := cmp.Diff(testCase.want, entries)
			assert.Empty(t, diff, "unexpected entries")
		})
	}
}

func Test_ModelCache_ScanPrefix_Returns_Empty_When_Offset_Exceeds_Filtered_Length(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 4)
	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	require.NoError(t, writerSession.Put([]byte("aa"), 1, []byte("i1")), "Put should succeed")
	require.NoError(t, writerSession.Put([]byte("ab"), 2, []byte("i2")), "Put should succeed")
	require.NoError(t, writerSession.Put([]byte("ba"), 3, []byte("i3")), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	entries, scanErr := cacheHandle.ScanPrefix([]byte("a"), slotcache.ScanOptions{Offset: 3})
	require.NoError(t, scanErr, "ScanPrefix should succeed with offset beyond filtered length")
	assert.Empty(t, entries, "ScanPrefix should return empty when offset exceeds filtered length")
}

func Test_ModelWriter_Commit_Succeeds_When_No_Buffered_Ops(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 2)
	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")

	require.NoError(t, writerSession.Commit(), "Commit should succeed with no buffered ops")

	length, err := cacheHandle.Len()
	require.NoError(t, err, "Len should succeed")
	assert.Equal(t, 0, length, "expected zero length after empty commit")

	// Verify we can start a new write session
	writerSession, err = cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed after empty commit")
	writerSession.Close()
}

func Test_ModelWriter_Close_Succeeds_When_Called_Multiple_Times(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 2)
	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")

	writerSession.Close()
	require.True(t, writerSession.IsClosed, "writer should be closed after first Close()")

	require.NotPanics(t, func() { writerSession.Close() }, "second Close should be a no-op")
}

func Test_ModelCache_Scan_Returns_All_Entries_When_Limit_Is_Zero(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 4)
	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	require.NoError(t, writerSession.Put([]byte("aa"), 1, []byte("i1")), "Put should succeed")
	require.NoError(t, writerSession.Put([]byte("ab"), 2, []byte("i2")), "Put should succeed")
	require.NoError(t, writerSession.Put([]byte("ac"), 3, []byte("i3")), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	entriesWithZeroLimit, err := cacheHandle.Scan(slotcache.ScanOptions{Limit: 0})
	require.NoError(t, err, "Scan with Limit=0 should succeed")

	entriesUnlimited, err := cacheHandle.Scan(slotcache.ScanOptions{})
	require.NoError(t, err, "Scan without Limit should succeed")

	diff := cmp.Diff(entriesUnlimited, entriesWithZeroLimit)
	assert.Empty(t, diff, "Limit=0 should return all entries like no limit")
	assert.Len(t, entriesWithZeroLimit, 3, "expected all three entries")
}

func Test_ModelCache_Scan_Returns_Only_Committed_Entries_When_Write_Session_Active(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 4)
	cacheHandle := model.Open(fileState)

	// First commit some entries
	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	require.NoError(t, writerSession.Put([]byte("aa"), 1, []byte("i1")), "Put should succeed")
	require.NoError(t, writerSession.Put([]byte("ab"), 2, []byte("i2")), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	// Start a new write session but don't commit
	writerSession, err = cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	require.NoError(t, writerSession.Put([]byte("ac"), 3, []byte("i3")), "Put should succeed")

	// Scan should only see committed entries
	entries, scanErr := cacheHandle.Scan(slotcache.ScanOptions{})
	require.NoError(t, scanErr, "Scan should succeed during active write session")

	wantEntries := []model.Entry{
		modelEntry("aa", 1, "i1"),
		modelEntry("ab", 2, "i2"),
	}
	diff := cmp.Diff(wantEntries, entries)
	assert.Empty(t, diff, "Scan should only return committed entries")

	// ScanPrefix should also only see committed entries
	prefixEntries, prefixErr := cacheHandle.ScanPrefix([]byte("a"), slotcache.ScanOptions{})
	require.NoError(t, prefixErr, "ScanPrefix should succeed during active write session")

	diff = cmp.Diff(wantEntries, prefixEntries)
	assert.Empty(t, diff, "ScanPrefix should only return committed entries")

	writerSession.Close()
}

func Test_ModelWriter_Commits_All_Keys_When_Batch_Contains_Multiple_Keys(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 5)
	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")

	require.NoError(t, writerSession.Put([]byte("aa"), 1, []byte("i1")), "Put should succeed")
	require.NoError(t, writerSession.Put([]byte("bb"), 2, []byte("i2")), "Put should succeed")
	require.NoError(t, writerSession.Put([]byte("cc"), 3, []byte("i3")), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	entries, scanErr := cacheHandle.Scan(slotcache.ScanOptions{})
	require.NoError(t, scanErr, "Scan should succeed")

	wantEntries := []model.Entry{
		modelEntry("aa", 1, "i1"),
		modelEntry("bb", 2, "i2"),
		modelEntry("cc", 3, "i3"),
	}
	diff := cmp.Diff(wantEntries, entries)
	assert.Empty(t, diff, "all keys should be committed")

	// Test batch with mixed puts and deletes on different keys
	writerSession, err = cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	require.NoError(t, writerSession.Put([]byte("dd"), 4, []byte("i4")), "Put should succeed")
	_, err = writerSession.Delete([]byte("bb"))
	require.NoError(t, err, "Delete should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	entries, scanErr = cacheHandle.Scan(slotcache.ScanOptions{})
	require.NoError(t, scanErr, "Scan should succeed")

	wantEntries = []model.Entry{
		modelEntry("aa", 1, "i1"),
		modelEntry("cc", 3, "i3"),
		modelEntry("dd", 4, "i4"),
	}
	diff = cmp.Diff(wantEntries, entries)
	assert.Empty(t, diff, "batch with puts and deletes on different keys should be atomic")
}

func Test_Open_Returns_Usable_CacheModel_When_FileState_Has_Existing_Slots(t *testing.T) {
	t.Parallel()

	fileState := &model.FileState{
		KeySize:      4,
		IndexSize:    8,
		SlotCapacity: 100,
		Slots: []model.SlotRecord{
			{KeyString: "aaaa", IsLive: true, Revision: 1, IndexString: "index123"},
		},
	}

	cacheHandle := model.Open(fileState)

	require.NotNil(t, cacheHandle, "Open should return non-nil cache")
	assert.Same(t, fileState, cacheHandle.File, "cache should reference the provided file state")
	assert.False(t, cacheHandle.IsClosed, "cache should not be closed")
	assert.Nil(t, cacheHandle.ActiveWrite, "cache should have no active writer")

	// Verify we can read the pre-existing slot
	entry, found, err := cacheHandle.Get([]byte("aaaa"))
	require.NoError(t, err, "Get should succeed")
	require.True(t, found, "pre-existing entry should be found")
	assert.Equal(t, int64(1), entry.Revision, "revision should match")
	assert.Equal(t, []byte("index123"), entry.Index, "index should match")
}

func Test_ModelFile_Clone_Copies_Tombstoned_Slots_When_Present(t *testing.T) {
	t.Parallel()

	fileState := &model.FileState{
		KeySize:      2,
		IndexSize:    2,
		SlotCapacity: 3,
		Slots: []model.SlotRecord{
			{KeyString: "aa", IsLive: true, Revision: 1, IndexString: "i1"},
			{KeyString: "bb", IsLive: false, Revision: 2, IndexString: "i2"}, // tombstoned
			{KeyString: "cc", IsLive: true, Revision: 3, IndexString: "i3"},
		},
	}

	clone := fileState.Clone()
	require.NotNil(t, clone, "clone should not be nil")

	diff := cmp.Diff(fileState, clone)
	assert.Empty(t, diff, "clone should match original including tombstoned slots")

	// Verify tombstone state is preserved
	assert.False(t, clone.Slots[1].IsLive, "tombstoned slot should remain tombstoned in clone")

	// Verify mutation isolation
	clone.Slots[1].IsLive = true
	assert.False(t, fileState.Slots[1].IsLive, "clone mutation should not affect original tombstone state")
}

func Test_ModelCache_ScanPrefix_Returns_Entry_When_Prefix_Equals_KeySize(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 3)
	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	require.NoError(t, writerSession.Put([]byte("aa"), 1, []byte("i1")), "Put should succeed")
	require.NoError(t, writerSession.Put([]byte("ab"), 2, []byte("i2")), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	// Prefix of exactly KeySize should match only the exact key
	entries, err := cacheHandle.ScanPrefix([]byte("aa"), slotcache.ScanOptions{})
	require.NoError(t, err, "ScanPrefix with exact-length prefix should succeed")

	wantEntries := []model.Entry{modelEntry("aa", 1, "i1")}
	diff := cmp.Diff(wantEntries, entries)
	assert.Empty(t, diff, "exact-length prefix should match only the exact key")

	// Verify non-matching exact-length prefix returns empty
	entries, err = cacheHandle.ScanPrefix([]byte("zz"), slotcache.ScanOptions{})
	require.NoError(t, err, "ScanPrefix should succeed")
	assert.Empty(t, entries, "non-matching exact-length prefix should return empty")
}

func Test_Open_Skips_Tombstoned_Slots_When_FileState_Has_Mixed_Slots(t *testing.T) {
	t.Parallel()

	fileState := &model.FileState{
		KeySize:      4,
		IndexSize:    8,
		SlotCapacity: 100,
		Slots: []model.SlotRecord{
			{KeyString: "aaaa", IsLive: true, Revision: 1, IndexString: "index111"},
			{KeyString: "bbbb", IsLive: false, Revision: 2, IndexString: "index222"}, // tombstoned
			{KeyString: "cccc", IsLive: true, Revision: 3, IndexString: "index333"},
		},
	}

	cacheHandle := model.Open(fileState)

	// Get should not find tombstoned entry
	_, found, err := cacheHandle.Get([]byte("bbbb"))
	require.NoError(t, err, "Get should succeed")
	assert.False(t, found, "tombstoned entry should not be found")

	// Get should find live entries
	entry, found, err := cacheHandle.Get([]byte("aaaa"))
	require.NoError(t, err, "Get should succeed")
	require.True(t, found, "live entry should be found")
	assert.Equal(t, int64(1), entry.Revision, "revision should match")

	// Scan should skip tombstoned entries
	entries, err := cacheHandle.Scan(slotcache.ScanOptions{})
	require.NoError(t, err, "Scan should succeed")

	wantEntries := []model.Entry{
		{Key: []byte("aaaa"), Revision: 1, Index: []byte("index111")},
		{Key: []byte("cccc"), Revision: 3, Index: []byte("index333")},
	}
	diff := cmp.Diff(wantEntries, entries)
	assert.Empty(t, diff, "Scan should skip tombstoned entries")

	// Len should only count live entries
	length, err := cacheHandle.Len()
	require.NoError(t, err, "Len should succeed")
	assert.Equal(t, 2, length, "Len should only count live entries")
}

func Test_ModelCache_Scan_Returns_Empty_When_All_Entries_Tombstoned(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 3)
	cacheHandle := model.Open(fileState)

	// Add entries
	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	require.NoError(t, writerSession.Put([]byte("aa"), 1, []byte("i1")), "Put should succeed")
	require.NoError(t, writerSession.Put([]byte("bb"), 2, []byte("i2")), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	// Delete all entries
	writerSession, err = cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	_, err = writerSession.Delete([]byte("aa"))
	require.NoError(t, err, "Delete should succeed")
	_, err = writerSession.Delete([]byte("bb"))
	require.NoError(t, err, "Delete should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	// Verify slots exist but are tombstoned
	assert.Len(t, fileState.Slots, 2, "slots should still exist")
	assert.False(t, fileState.Slots[0].IsLive, "first slot should be tombstoned")
	assert.False(t, fileState.Slots[1].IsLive, "second slot should be tombstoned")

	// Scan should return empty
	entries, err := cacheHandle.Scan(slotcache.ScanOptions{})
	require.NoError(t, err, "Scan should succeed")
	assert.Empty(t, entries, "Scan should return empty when all entries tombstoned")

	// ScanPrefix should also return empty
	entries, err = cacheHandle.ScanPrefix([]byte("a"), slotcache.ScanOptions{})
	require.NoError(t, err, "ScanPrefix should succeed")
	assert.Empty(t, entries, "ScanPrefix should return empty when all matching entries tombstoned")

	// Len should return zero
	length, err := cacheHandle.Len()
	require.NoError(t, err, "Len should succeed")
	assert.Equal(t, 0, length, "Len should return zero when all entries tombstoned")
}

func Test_ModelCache_ScanPrefix_Returns_Empty_When_Offset_Equals_Filtered_Count(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 4)
	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	require.NoError(t, writerSession.Put([]byte("aa"), 1, []byte("i1")), "Put should succeed")
	require.NoError(t, writerSession.Put([]byte("ab"), 2, []byte("i2")), "Put should succeed")
	require.NoError(t, writerSession.Put([]byte("ba"), 3, []byte("i3")), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	// Prefix "a" matches 2 entries (aa, ab). Offset=2 should return empty, not error.
	entries, err := cacheHandle.ScanPrefix([]byte("a"), slotcache.ScanOptions{Offset: 2})
	require.NoError(t, err, "ScanPrefix should succeed when offset equals filtered count")

	var wantEntries []model.Entry

	diff := cmp.Diff(wantEntries, entries, cmpopts.EquateEmpty())
	assert.Empty(t, diff, "ScanPrefix should return empty when offset equals filtered count")
}

func Test_ModelFile_Clone_Returns_Empty_Slice_When_Slots_Empty_But_Not_Nil(t *testing.T) {
	t.Parallel()

	fileState := &model.FileState{
		KeySize:      2,
		IndexSize:    2,
		SlotCapacity: 3,
		Slots:        []model.SlotRecord{}, // empty but not nil
	}

	clone := fileState.Clone()
	require.NotNil(t, clone, "clone should not be nil")
	require.NotNil(t, clone.Slots, "cloned Slots should not be nil")
	assert.Empty(t, clone.Slots, "cloned Slots should be empty")

	diff := cmp.Diff(fileState, clone)
	assert.Empty(t, diff, "clone should match original")
}

func Test_ModelWriter_Commits_Batch_When_Only_Deletes_Buffered(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 5)
	cacheHandle := model.Open(fileState)

	// First, insert multiple entries
	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	require.NoError(t, writerSession.Put([]byte("aa"), 1, []byte("i1")), "Put should succeed")
	require.NoError(t, writerSession.Put([]byte("bb"), 2, []byte("i2")), "Put should succeed")
	require.NoError(t, writerSession.Put([]byte("cc"), 3, []byte("i3")), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	length, err := cacheHandle.Len()
	require.NoError(t, err, "Len should succeed")
	assert.Equal(t, 3, length, "expected three entries before batch delete")

	// Now delete multiple entries in a single batch (no puts)
	writerSession, err = cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")

	present, err := writerSession.Delete([]byte("aa"))
	require.NoError(t, err, "Delete should succeed")
	assert.True(t, present, "aa should have been present")

	present, err = writerSession.Delete([]byte("cc"))
	require.NoError(t, err, "Delete should succeed")
	assert.True(t, present, "cc should have been present")

	require.NoError(t, writerSession.Commit(), "Commit should succeed with only deletes")

	// Verify only bb remains
	entries, err := cacheHandle.Scan(slotcache.ScanOptions{})
	require.NoError(t, err, "Scan should succeed")

	wantEntries := []model.Entry{modelEntry("bb", 2, "i2")}
	diff := cmp.Diff(wantEntries, entries)
	assert.Empty(t, diff, "only bb should remain after batch delete")

	length, err = cacheHandle.Len()
	require.NoError(t, err, "Len should succeed")
	assert.Equal(t, 1, length, "expected one entry after batch delete")
}

func Test_ModelCache_Scan_Preserves_Order_When_Keys_Reinserted_Multiple_Times(t *testing.T) {
	t.Parallel()

	fileState := newTestFile(t, 10)
	cacheHandle := model.Open(fileState)

	// Insert aa, bb, cc in order
	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	require.NoError(t, writerSession.Put([]byte("aa"), 1, []byte("i1")), "Put should succeed")
	require.NoError(t, writerSession.Put([]byte("bb"), 2, []byte("i2")), "Put should succeed")
	require.NoError(t, writerSession.Put([]byte("cc"), 3, []byte("i3")), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	// Delete aa
	writerSession, err = cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	_, err = writerSession.Delete([]byte("aa"))
	require.NoError(t, err, "Delete should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	// Reinsert aa - should now be at the end (appended as new slot)
	writerSession, err = cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")
	require.NoError(t, writerSession.Put([]byte("aa"), 4, []byte("i4")), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	// Scan forward: order should be bb, cc, aa (aa was reinserted last)
	entries, err := cacheHandle.Scan(slotcache.ScanOptions{})
	require.NoError(t, err, "Scan should succeed")

	wantEntries := []model.Entry{
		modelEntry("bb", 2, "i2"),
		modelEntry("cc", 3, "i3"),
		modelEntry("aa", 4, "i4"), // reinserted, now at end
	}
	diff := cmp.Diff(wantEntries, entries)
	assert.Empty(t, diff, "reinserted key should appear at end of scan")

	// Scan reverse: order should be aa, cc, bb
	entriesReverse, err := cacheHandle.Scan(slotcache.ScanOptions{Reverse: true})
	require.NoError(t, err, "Scan reverse should succeed")

	wantEntriesReverse := []model.Entry{
		modelEntry("aa", 4, "i4"),
		modelEntry("cc", 3, "i3"),
		modelEntry("bb", 2, "i2"),
	}
	diff = cmp.Diff(wantEntriesReverse, entriesReverse)
	assert.Empty(t, diff, "reverse scan should show reinserted key first")

	// Verify slot structure: should have 4 slots (original aa tombstoned, bb, cc, new aa)
	assert.Len(t, fileState.Slots, 4, "should have 4 slot records")
	assert.False(t, fileState.Slots[0].IsLive, "original aa slot should be tombstoned")
	assert.True(t, fileState.Slots[1].IsLive, "bb slot should be live")
	assert.True(t, fileState.Slots[2].IsLive, "cc slot should be live")
	assert.True(t, fileState.Slots[3].IsLive, "reinserted aa slot should be live")
}

func Test_ModelCache_ScanMatch_Returns_Entries_When_ByteAligned_Prefix_Matches_With_KeyOffset(t *testing.T) {
	t.Parallel()

	opts := slotcache.Options{
		KeySize:      4,
		IndexSize:    0,
		SlotCapacity: 10,
	}

	fileState, err := model.NewFile(opts)
	require.NoError(t, err, "NewFile should succeed")

	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")

	keyA := []byte{0x01, 0xAA, 0xBB, 0xCC}
	keyB := []byte{0x02, 0xAA, 0xBB, 0xDD}
	keyC := []byte{0x03, 0xAA, 0xCC, 0x00}

	require.NoError(t, writerSession.Put(keyA, 1, nil), "Put should succeed")
	require.NoError(t, writerSession.Put(keyB, 2, nil), "Put should succeed")
	require.NoError(t, writerSession.Put(keyC, 3, nil), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	spec := slotcache.Prefix{Offset: 1, Bits: 0, Bytes: []byte{0xAA, 0xBB}}

	entries, err := cacheHandle.ScanMatch(spec, slotcache.ScanOptions{})
	require.NoError(t, err, "ScanMatch should succeed")

	wantEntries := []model.Entry{
		{Key: keyA, Revision: 1, Index: []byte{}},
		{Key: keyB, Revision: 2, Index: []byte{}},
	}

	diff := cmp.Diff(wantEntries, entries, cmpopts.EquateEmpty())
	assert.Empty(t, diff, "unexpected entries")
}

func Test_ModelCache_ScanMatch_Returns_Entries_When_BitPrefix_Matches(t *testing.T) {
	t.Parallel()

	opts := slotcache.Options{
		KeySize:      2,
		IndexSize:    0,
		SlotCapacity: 10,
	}

	fileState, err := model.NewFile(opts)
	require.NoError(t, err, "NewFile should succeed")

	cacheHandle := model.Open(fileState)

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")

	key1 := []byte{0xAB, 0xC0}
	key2 := []byte{0xAB, 0xFF}
	key3 := []byte{0xAB, 0x80}
	key4 := []byte{0xAA, 0xC0}

	require.NoError(t, writerSession.Put(key1, 1, nil), "Put should succeed")
	require.NoError(t, writerSession.Put(key2, 2, nil), "Put should succeed")
	require.NoError(t, writerSession.Put(key3, 3, nil), "Put should succeed")
	require.NoError(t, writerSession.Put(key4, 4, nil), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	spec := slotcache.Prefix{Offset: 0, Bits: 10, Bytes: []byte{0xAB, 0xC0}}

	entries, err := cacheHandle.ScanMatch(spec, slotcache.ScanOptions{})
	require.NoError(t, err, "ScanMatch should succeed")

	wantEntries := []model.Entry{
		{Key: key1, Revision: 1, Index: []byte{}},
		{Key: key2, Revision: 2, Index: []byte{}},
	}

	diff := cmp.Diff(wantEntries, entries, cmpopts.EquateEmpty())
	assert.Empty(t, diff, "unexpected entries")
}

func Test_ModelCache_ScanMatch_Returns_ErrInvalidInput_When_Spec_Invalid(t *testing.T) {
	t.Parallel()

	opts := slotcache.Options{
		KeySize:      4,
		IndexSize:    0,
		SlotCapacity: 1,
	}

	fileState, err := model.NewFile(opts)
	require.NoError(t, err, "NewFile should succeed")

	cacheHandle := model.Open(fileState)

	testCases := []struct {
		name string
		spec slotcache.Prefix
	}{
		{name: "NegativeKeyOffset", spec: slotcache.Prefix{Offset: -1, Bits: 0, Bytes: []byte{0x00}}},
		{name: "KeyOffsetOutOfRange", spec: slotcache.Prefix{Offset: 4, Bits: 0, Bytes: []byte{0x00}}},
		{name: "EmptyBytesByteAligned", spec: slotcache.Prefix{Offset: 0, Bits: 0, Bytes: []byte{}}},
		{name: "NilBytesByteAligned", spec: slotcache.Prefix{Offset: 0, Bits: 0, Bytes: nil}},
		{name: "NegativePrefixBits", spec: slotcache.Prefix{Offset: 0, Bits: -1, Bytes: []byte{0x00}}},
		{name: "BytesLenMismatchBitMode", spec: slotcache.Prefix{Offset: 0, Bits: 9, Bytes: []byte{0xAA}}},
		{name: "TooLongBitMode", spec: slotcache.Prefix{Offset: 3, Bits: 16, Bytes: []byte{0xAA, 0xBB}}},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, err := cacheHandle.ScanMatch(testCase.spec, slotcache.ScanOptions{})
			require.ErrorIs(t, err, slotcache.ErrInvalidInput, "ScanMatch should reject invalid spec")
		})
	}
}

func Test_ModelCache_ScanRange_Returns_ErrUnordered_When_OrderedKeys_Disabled(t *testing.T) {
	t.Parallel()

	opts := slotcache.Options{
		KeySize:      2,
		IndexSize:    0,
		SlotCapacity: 10,
		OrderedKeys:  false,
	}

	fileState, err := model.NewFile(opts)
	require.NoError(t, err, "NewFile should succeed")

	cacheHandle := model.Open(fileState)

	_, err = cacheHandle.ScanRange(nil, nil, slotcache.ScanOptions{})
	require.ErrorIs(t, err, slotcache.ErrUnordered, "ScanRange should require ordered mode")
}

func Test_ModelCache_ScanRange_Filters_And_Paginates_When_OrderedKeys_Enabled(t *testing.T) {
	t.Parallel()

	opts := slotcache.Options{
		KeySize:      2,
		IndexSize:    0,
		SlotCapacity: 10,
		OrderedKeys:  true,
	}

	fileState, err := model.NewFile(opts)
	require.NoError(t, err, "NewFile should succeed")

	cacheHandle := model.Open(fileState)

	k1 := []byte{0x00, 0x10}
	k2 := []byte{0x00, 0x20}
	k3 := []byte{0x00, 0x30}
	k4 := []byte{0x00, 0x40}

	writerSession, err := cacheHandle.BeginWrite()
	require.NoError(t, err, "BeginWrite should succeed")

	// Insert in non-sorted order; ordered mode sorts new inserts at commit time.
	require.NoError(t, writerSession.Put(k3, 3, nil), "Put should succeed")
	require.NoError(t, writerSession.Put(k1, 1, nil), "Put should succeed")
	require.NoError(t, writerSession.Put(k4, 4, nil), "Put should succeed")
	require.NoError(t, writerSession.Put(k2, 2, nil), "Put should succeed")
	require.NoError(t, writerSession.Commit(), "Commit should succeed")

	// Range [k2, k4) should include k2 and k3.
	entries, err := cacheHandle.ScanRange(k2, k4, slotcache.ScanOptions{})
	require.NoError(t, err, "ScanRange should succeed")

	wantEntries := []model.Entry{
		{Key: k2, Revision: 2, Index: []byte{}},
		{Key: k3, Revision: 3, Index: []byte{}},
	}

	diff := cmp.Diff(wantEntries, entries, cmpopts.EquateEmpty())
	assert.Empty(t, diff, "unexpected entries")

	// A shorter start bound is padded with zeros.
	entries, err = cacheHandle.ScanRange([]byte{0x00}, []byte{0x00, 0x30}, slotcache.ScanOptions{})
	require.NoError(t, err, "ScanRange should succeed")

	wantEntries = []model.Entry{
		{Key: k1, Revision: 1, Index: []byte{}},
		{Key: k2, Revision: 2, Index: []byte{}},
	}

	diff = cmp.Diff(wantEntries, entries, cmpopts.EquateEmpty())
	assert.Empty(t, diff, "unexpected entries")

	// Reverse + pagination applies after filtering.
	entries, err = cacheHandle.ScanRange(nil, nil, slotcache.ScanOptions{Reverse: true, Offset: 1, Limit: 2})
	require.NoError(t, err, "ScanRange should succeed")

	wantEntries = []model.Entry{
		{Key: k3, Revision: 3, Index: []byte{}},
		{Key: k2, Revision: 2, Index: []byte{}},
	}

	diff = cmp.Diff(wantEntries, entries, cmpopts.EquateEmpty())
	assert.Empty(t, diff, "unexpected entries")
}

func Test_ModelCache_ScanRange_Returns_ErrInvalidInput_When_Bounds_Invalid(t *testing.T) {
	t.Parallel()

	opts := slotcache.Options{
		KeySize:      2,
		IndexSize:    0,
		SlotCapacity: 10,
		OrderedKeys:  true,
	}

	fileState, err := model.NewFile(opts)
	require.NoError(t, err, "NewFile should succeed")

	cacheHandle := model.Open(fileState)

	testCases := []struct {
		name  string
		start []byte
		end   []byte
	}{
		{name: "EmptyStart", start: []byte{}, end: nil},
		{name: "EmptyEnd", start: nil, end: []byte{}},
		{name: "StartTooLong", start: []byte{0x00, 0x00, 0x00}, end: nil},
		{name: "EndTooLong", start: nil, end: []byte{0x00, 0x00, 0x00}},
		{name: "StartGreaterThanEnd", start: []byte{0x02}, end: []byte{0x01}},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, err := cacheHandle.ScanRange(testCase.start, testCase.end, slotcache.ScanOptions{})
			require.ErrorIs(t, err, slotcache.ErrInvalidInput, "ScanRange should reject invalid bounds")
		})
	}
}

// newTestFile creates a model.FileState with KeySize=2, IndexSize=2, and the given slot capacity.
func newTestFile(t *testing.T, slotCapacity uint64) *model.FileState {
	t.Helper()

	fileState, err := model.NewFile(slotcache.Options{
		KeySize:      2,
		IndexSize:    2,
		SlotCapacity: slotCapacity,
	})
	if err != nil {
		t.Fatalf("newTestFile: %v", err)
	}

	return fileState
}

// modelEntry is a helper to construct a model.Entry for test assertions.
func modelEntry(key string, revision int64, index string) model.Entry {
	return model.Entry{
		Key:      []byte(key),
		Revision: revision,
		Index:    []byte(index),
	}
}
