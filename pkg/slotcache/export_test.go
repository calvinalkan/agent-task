package slotcache

// Export internal functions and variables for testing.
// This file is only compiled during tests.

// GetRegistryEntryForTesting returns the registry entry for the given cache identity.
// Returns (refCount, exists). refCount is 0 if not found.
func GetRegistryEntryForTesting(c Cache) (int, bool) {
	cc, ok := c.(*cache)
	if !ok {
		return 0, false
	}

	val, ok := globalRegistry.Load(cc.identity)
	if !ok {
		return 0, false
	}

	entry, ok := val.(*fileRegistryEntry)
	if !ok {
		return 0, false
	}

	registryMu.Lock()

	count := entry.refCount

	registryMu.Unlock()

	return count, true
}

// RegistryEntryExistsForTesting checks if a registry entry exists for the given cache.
// This can be called even after the cache is closed (uses the identity from the cache struct).
func RegistryEntryExistsForTesting(c Cache) bool {
	cc, ok := c.(*cache)
	if !ok {
		return false
	}

	_, exists := globalRegistry.Load(cc.identity)

	return exists
}
