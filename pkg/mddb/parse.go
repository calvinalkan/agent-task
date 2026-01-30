package mddb

import (
	"errors"
	"fmt"

	"github.com/calvinalkan/agent-task/pkg/mddb/frontmatter"
)

// parseIndexable parses a markdown file into an IndexableDocument for SQL indexing.
// Used during reindex and WAL replay. All returned data is borrowed from the content
// buffer except ShortID which is derived.
//
// This is a lower-level helper - returns errors with subsystem prefix only.
// Public APIs (Get, Reindex) add structured context via withContext().
//
// Validates:
//   - Frontmatter structure and required fields (id, title)
//   - Derived path matches actual file path (prevents orphaned files)
//   - ShortID derivation succeeds
func (mddb *MDDB[T]) parseIndexable(fsRelPath []byte, content []byte, mtimeNS int64, sizeBytes int64, expectedID string) (IndexableDocument, error) {
	fm, tail, err := frontmatter.ParseBytes(content, mddb.cfg.ParseOptions...)
	if err != nil {
		return IndexableDocument{}, fmt.Errorf("frontmatter: %w", err)
	}

	idBytes, ok := fm.GetBytes(frontmatterKeyID)
	if !ok {
		return IndexableDocument{}, fmt.Errorf("frontmatter: %w", ErrMissingID)
	}

	if len(idBytes) == 0 {
		return IndexableDocument{}, fmt.Errorf("frontmatter: %w", ErrEmptyID)
	}

	id := string(idBytes)

	if expectedID != "" && id != expectedID {
		return IndexableDocument{}, fmt.Errorf("frontmatter: id mismatch: expected %q, got %q", expectedID, id)
	}

	titleBytes, _ := fm.GetBytes(frontmatterKeyTitle)
	if len(titleBytes) == 0 {
		return IndexableDocument{}, fmt.Errorf("frontmatter: %w", ErrEmptyTitle)
	}

	_, shortID, err := mddb.deriveAndValidate(id, fsRelPath)
	if err != nil {
		return IndexableDocument{}, err // Already has context from deriveAndValidate
	}

	return IndexableDocument{
		ID:          idBytes,
		ShortID:     []byte(shortID),
		RelPath:     fsRelPath,
		MtimeNS:     mtimeNS,
		SizeBytes:   sizeBytes,
		Title:       titleBytes,
		Body:        tail,
		Frontmatter: fm,
	}, nil
}

// parseDocument parses a markdown file and returns the user document type.
// Used by Get() to load a document by ID. Calls parseIndexable internally,
// then converts via Config.DocumentFrom.
//
// This is a lower-level helper - returns errors with subsystem prefix only.
// Public APIs add structured context via withContext().
func (mddb *MDDB[T]) parseDocument(fsRelPath string, content []byte, mtimeNS int64, sizeBytes int64, expectedID string) (*T, error) {
	indexable, err := mddb.parseIndexable([]byte(fsRelPath), content, mtimeNS, sizeBytes, expectedID)
	if err != nil {
		return nil, err
	}

	id := string(indexable.ID)

	doc, err := mddb.cfg.DocumentFrom(indexable)
	if err != nil {
		return nil, fmt.Errorf("DocumentFrom: %w", err)
	}

	if doc == nil {
		return nil, errors.New("DocumentFrom: returned nil")
	}

	d, ok := any(*doc).(Document)
	if !ok {
		return nil, errors.New("DocumentFrom: type assertion to Document failed")
	}

	if d.ID() != id {
		return nil, fmt.Errorf("DocumentFrom: id mismatch: doc.ID()=%q, frontmatter=%q", d.ID(), id)
	}

	return doc, nil
}
