//go:build slotcache_impl

package slotcache

// Put buffers a put operation for the given key.
func (*Writer) Put(_ []byte, _ int64, _ []byte) error {
	panic("not implemented")
}

// Delete buffers a delete operation for the given key.
func (*Writer) Delete(_ []byte) (bool, error) {
	panic("not implemented")
}

// Commit applies all buffered operations atomically.
func (*Writer) Commit() error {
	panic("not implemented")
}

// Abort discards all buffered operations.
func (*Writer) Abort() error {
	panic("not implemented")
}

// Close is an alias for Abort.
func (*Writer) Close() error {
	panic("not implemented")
}
