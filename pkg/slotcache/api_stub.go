//go:build !slotcache_impl

package slotcache

// This file provides a compile-time stub of the slotcache API.
//
// It is used when the real implementation has not yet been built (the
// slotcache_impl build tag is not enabled). The Open function panics.
// Since Cache and Writer are interfaces, no method stubs are needed.

// Open opens or creates a cache file at the path specified in options.
func Open(_ Options) (Cache, error) {
	panic("slotcache: not compiled with slotcache_impl build tag")
}
