package ticket

import (
	"fmt"
	"path/filepath"
)

// UpdateCacheAfterTicketWrite updates the cache after writing a ticket.
func UpdateCacheAfterTicketWrite(ticketDir, filename string, summary *Summary) error {
	cacheErr := UpdateCacheEntry(ticketDir, filename, summary)
	if cacheErr != nil {
		cachePath := filepath.Join(ticketDir, CacheFileName)

		// Cache update failed - try to delete cache (safe: rebuild on next read).
		rmErr := DeleteCache(ticketDir)
		if rmErr != nil {
			// Both failed - return detailed error with fix instructions.
			return fmt.Errorf("updating cache: %w; "+
				"ticket was saved but cache is stale; "+
				"cache write: %v; "+
				"cache delete: %v; "+
				"fix: rm %s",
				cacheErr, cacheErr, rmErr, cachePath)
		}

		// Cache deleted successfully (or didn't exist) - rebuild on next read.
	}

	return nil
}
