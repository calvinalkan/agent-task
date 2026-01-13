package fmcache

// This file defines the low-level byte-oriented cache API.
//
// NOTE: All methods are currently no-op stubs for TDD. The implementation will
// be added later and must match the behavior described in spec.md and the spec
// model.

// SyncMode configures fsync behavior on Commit.
type SyncMode int

const (
	// SyncNone does not call fsync. Fastest, least durable.
	SyncNone SyncMode = iota // default

	// Sync fsyncs the temp file before rename, but does not fsync the directory.
	Sync

	// SyncFull fsyncs the temp file, renames into place, then fsyncs the directory.
	SyncFull
)

// Options configures the on-disk format and durability.
//
// NOTE: These fields are not validated yet in the stub.
type Options struct {
	KeySize       int
	IndexSize     int
	MaxDataLen    int
	SchemaVersion uint16
	SyncMode      SyncMode
}

// ByteCache is the low-level cache API that stores and returns raw bytes.
type ByteCache struct{}

// ByteEntry is a cached item returned by Get.
type ByteEntry struct {
	Key      string
	Revision int64
	Index    []byte
	Data     []byte
}

// IndexMatch is the result element type for FilterIndex/AllEntries.
type IndexMatch struct {
	Key      string
	Revision int64
	Index    []byte
}

// OpenByteCache opens or creates a byte cache at the given path.
func OpenByteCache(_ string, _ Options) (*ByteCache, error) {
	panic("not implemented")
}

// Close closes the cache.
func (*ByteCache) Close() error {
	panic("not implemented")
}

// Len returns the number of entries in the cache.
func (*ByteCache) Len() (int, error) {
	panic("not implemented")
}

// Get retrieves an entry by key.
func (*ByteCache) Get(_ string) (ByteEntry, bool, error) {
	panic("not implemented")
}

// Put stores an entry with the given key, revision, index and data.
func (*ByteCache) Put(_ string, _ int64, _, _ []byte) error {
	panic("not implemented")
}

// Delete removes an entry by key. Returns true if it existed.
func (*ByteCache) Delete(_ string) (bool, error) {
	panic("not implemented")
}

// FilterIndex returns entries matching the filter function.
func (*ByteCache) FilterIndex(_ FilterOpts, _ func(key string, revision int64, index []byte) bool) ([]IndexMatch, error) {
	panic("not implemented")
}

// AllEntries returns all entries in the cache.
func (*ByteCache) AllEntries(_ FilterOpts) ([]IndexMatch, error) {
	panic("not implemented")
}

// Commit writes all pending changes to disk.
func (*ByteCache) Commit() error {
	panic("not implemented")
}
