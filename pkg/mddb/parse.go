package mddb

import (
	"errors"
	"fmt"
	"strings"

	"github.com/calvinalkan/agent-task/pkg/mddb/frontmatter"
)

func (mddb *MDDB[T]) parseFrontmatter(data []byte) (frontmatter.Frontmatter, []byte, error) {
	limit := mddb.cfg.FrontmatterLineLimit

	fm, tail, err := frontmatter.ParseFrontmatter(data, frontmatter.WithLineLimit(limit))
	if err != nil {
		return nil, nil, fmt.Errorf("parse frontmatter: %w", err)
	}

	return fm, tail, nil
}

func (mddb *MDDB[T]) parseDocumentContent(relPath string, content []byte, mtimeNS int64, expectedID string) (*T, error) {
	err := mddb.validateRelPath(relPath)
	if err != nil {
		return nil, err
	}

	fm, tail, err := mddb.parseFrontmatter(content)
	if err != nil {
		return nil, err
	}

	id, ok := fm.GetString("id")
	if !ok {
		return nil, errors.New("missing id")
	}

	if id == "" {
		return nil, errors.New("id is empty")
	}

	if expectedID != "" && id != expectedID {
		return nil, fmt.Errorf("id mismatch: expected %s, got %s", expectedID, id)
	}

	body := strings.TrimRight(string(tail), "\r\n")

	doc, err := mddb.cfg.Parse(id, fm, body, mtimeNS)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	if doc == nil {
		return nil, errors.New("parse: returned nil document")
	}

	d, ok := any(*doc).(Document)
	if !ok {
		return nil, fmt.Errorf("type assertion failed for %s", id)
	}

	if d.ID() != id {
		return nil, fmt.Errorf("id mismatch: doc says %q, frontmatter %q", d.ID(), id)
	}

	if d.RelPath() != relPath {
		return nil, fmt.Errorf("path mismatch: doc says %q, file at %q", d.RelPath(), relPath)
	}

	return doc, nil
}
