// Package mddb is a markdown-first storage layer.
//
// It is intentionally designed to keep Markdown files as the source of truth
// (git-friendly, human-readable diffs) and treat indexes/caches as derived,
// throwaway materialized views.
//
// The low-level mmap-friendly cache component lives in subpackage fmcache.
package mddb
