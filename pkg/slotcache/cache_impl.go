//go:build slotcache_impl

package slotcache

// Open creates or opens a cache file with the given options.
func Open(_ Options) (*Cache, error) {
	panic("not implemented")
}

// Close closes the cache handle.
func (*Cache) Close() error {
	panic("not implemented")
}

// Len returns the number of live entries in the cache.
func (*Cache) Len() (int, error) {
	panic("not implemented")
}

// Get retrieves an entry by exact key.
func (*Cache) Get(_ []byte) (Entry, bool, error) {
	panic("not implemented")
}

// Scan iterates over all live entries.
func (*Cache) Scan(_ ScanOpts) (Seq, error) {
	panic("not implemented")
}

// ScanPrefix iterates over live entries matching the given prefix.
func (*Cache) ScanPrefix(_ []byte, _ ScanOpts) (Seq, error) {
	panic("not implemented")
}

// BeginWrite starts a new write session.
func (*Cache) BeginWrite() (*Writer, error) {
	panic("not implemented")
}
