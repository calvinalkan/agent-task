package main

import (
	"encoding/gob"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const cacheFileName = ".cache"

// TicketCache stores parsed ticket summaries with file mtimes.
type TicketCache struct {
	Entries map[string]CacheEntry // filename (without path) -> entry
}

// CacheEntry holds cached data for a single ticket file.
type CacheEntry struct {
	Mtime   time.Time     // file mtime when parsed
	Summary TicketSummary // parsed frontmatter
}

// Cache errors.
var (
	errCacheNotFound = errors.New("cache file not found")
	errCacheCorrupt  = errors.New("cache file corrupted")
)

// LoadCache loads the cache from the ticket directory.
// Returns errCacheNotFound if file doesn't exist.
// Returns errCacheCorrupt if file can't be decoded.
func LoadCache(ticketDir string) (*TicketCache, error) {
	cachePath := filepath.Join(ticketDir, cacheFileName)

	file, err := os.Open(cachePath) //nolint:gosec // path is constructed from ticketDir
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errCacheNotFound
		}

		return nil, fmt.Errorf("opening cache: %w", err)
	}

	defer func() { _ = file.Close() }()

	var cache TicketCache

	decoder := gob.NewDecoder(file)

	decodeErr := decoder.Decode(&cache)
	if decodeErr != nil {
		return nil, errCacheCorrupt
	}

	// Ensure map is initialized
	if cache.Entries == nil {
		cache.Entries = make(map[string]CacheEntry)
	}

	return &cache, nil
}

// SaveCache saves the cache to the ticket directory.
func SaveCache(ticketDir string, cache *TicketCache) error {
	cachePath := filepath.Join(ticketDir, cacheFileName)

	file, err := os.Create(cachePath) //nolint:gosec // path is constructed from ticketDir
	if err != nil {
		return fmt.Errorf("creating cache file: %w", err)
	}

	defer func() { _ = file.Close() }()

	encoder := gob.NewEncoder(file)

	encodeErr := encoder.Encode(cache)
	if encodeErr != nil {
		return fmt.Errorf("encoding cache: %w", encodeErr)
	}

	return nil
}

// DeleteCache removes the cache file from the ticket directory.
func DeleteCache(ticketDir string) error {
	cachePath := filepath.Join(ticketDir, cacheFileName)

	err := os.Remove(cachePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing cache: %w", err)
	}

	return nil
}
