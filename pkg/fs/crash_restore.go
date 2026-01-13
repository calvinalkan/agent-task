package fs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveResult holds the result of resolving a path.
type resolveResult struct {
	abs  string
	rel  string
	live string
}

// resolvePairResult holds the result of resolving a pair of paths.
type resolvePairResult struct {
	oldAbs string
	oldRel string
	newAbs string
	newRel string
	live   string
}

type objID uint64

type objKind uint8

const (
	objDir objKind = iota
	objFile
)

const rootID objID = 1

type fileSnapshot struct {
	data []byte
	perm os.FileMode
}

func (c *Crash) allocIDLocked(kind objKind) objID {
	id := c.nextID
	c.nextID++

	c.kind[id] = kind
	if kind == objDir {
		if _, ok := c.liveChildren[id]; !ok {
			c.liveChildren[id] = make(map[string]objID)
		}

		if _, ok := c.durableChildren[id]; !ok {
			c.durableChildren[id] = make(map[string]objID)
		}
	}

	return id
}

// rotateLocked switches [Crash] to a new working directory.
//
// It closes all open files, creates a fresh work dir, and (if !isInit) restores
// the durable snapshot. Callers must hold [Crash.mu].
func (c *Crash) rotateLocked(isInit bool) error {
	oldLive := c.live

	for f := range c.open {
		_ = f.closeUnderlying()
	}

	c.open = make(map[*crashFile]struct{})

	workDir, err := os.MkdirTemp(c.baseDir, "crashfs-*")
	if err != nil {
		return CrashFSErr("create work dir", err)
	}

	c.live = workDir

	if !isInit {
		err := c.restoreLocked()
		if err != nil {
			c.live = oldLive
			_ = os.RemoveAll(workDir)

			return err
		}
	}

	if oldLive != "" {
		_ = os.RemoveAll(oldLive)
	}

	return nil
}

// restoreLocked replays the durable snapshot into the current work dir.
//
// It also rewrites the snapshot using fresh internal IDs so post-crash live state
// cannot retain any accidental object aliasing.
func (c *Crash) restoreLocked() error {
	// Clone the durable snapshot into a tree with fresh IDs.
	type cloneState struct {
		next     objID
		kind     map[objID]objKind
		children map[objID]map[string]objID
		files    map[objID]fileSnapshot
	}

	st := &cloneState{
		next:     rootID + 1,
		kind:     map[objID]objKind{rootID: objDir},
		children: map[objID]map[string]objID{rootID: {}},
		files:    make(map[objID]fileSnapshot),
	}

	var cloneNode func(old objID) (objID, error)

	cloneNode = func(old objID) (objID, error) {
		kind, ok := c.kind[old]
		if !ok {
			return 0, CrashFSErr("clone durable snapshot", fmt.Errorf("unknown object id %d", old))
		}

		newID := st.next
		st.next++
		st.kind[newID] = kind

		switch kind {
		case objFile:
			if snap, ok := c.durableFiles[old]; ok {
				st.files[newID] = snap
			}
		case objDir:
			st.children[newID] = map[string]objID{}

			oldChildren := c.durableChildren[old]
			for name, childOld := range oldChildren {
				childNew, err := cloneNode(childOld)
				if err != nil {
					return 0, err
				}

				st.children[newID][name] = childNew
			}
		default:
			return 0, CrashFSErr("clone durable snapshot", fmt.Errorf("unknown kind %d", kind))
		}

		return newID, nil
	}

	for name, oldChild := range c.durableChildren[rootID] {
		newChild, err := cloneNode(oldChild)
		if err != nil {
			return err
		}

		st.children[rootID][name] = newChild
	}

	c.nextID = st.next
	c.kind = st.kind
	c.durableChildren = st.children
	c.durableFiles = st.files

	c.rebuildLiveFromDurableLocked()

	return c.restoreToDiskLocked(rootID, c.live)
}

// restoreToDiskLocked materializes the durable snapshot into the current work dir.
//
// It walks durableChildren in deterministic name order and creates directories and
// files on disk. Files are written from durableFiles (or as empty if no content
// snapshot exists).
//
// Callers must hold [Crash.mu].
func (c *Crash) restoreToDiskLocked(dirID objID, abs string) error {
	children := c.durableChildren[dirID]
	for _, name := range sortedChildNames(children) {
		childID := children[name]
		childAbs := filepath.Join(abs, name)
		kind := c.kind[childID]

		switch kind {
		case objDir:
			err := os.MkdirAll(childAbs, 0o750)
			if err != nil {
				return CrashFSErr("restore dir", fmt.Errorf("dir %q: %w", filepath.Join(abs, name), err))
			}

			err = c.restoreToDiskLocked(childID, childAbs)
			if err != nil {
				return err
			}
		case objFile:
			snap, ok := c.durableFiles[childID]
			data := []byte(nil)
			perm := os.FileMode(0o644)

			if ok {
				data = snap.data
				if snap.perm != 0 {
					perm = snap.perm
				}
			}

			err := os.MkdirAll(filepath.Dir(childAbs), 0o750)
			if err != nil {
				return CrashFSErr("restore parent dir", fmt.Errorf("path %q: %w", childAbs, err))
			}

			err = os.WriteFile(childAbs, data, perm)
			if err != nil {
				return CrashFSErr("restore file", fmt.Errorf("path %q: %w", childAbs, err))
			}
		default:
			return CrashFSErr("restore snapshot", fmt.Errorf("unknown kind %d", kind))
		}
	}

	return nil
}

func (c *Crash) rebuildLiveFromDurableLocked() {
	c.liveChildren = make(map[objID]map[string]objID)
	c.liveChildren[rootID] = copyChildren(c.durableChildren[rootID])

	var walk func(dirID objID)

	walk = func(dirID objID) {
		children := c.liveChildren[dirID]
		for _, childID := range children {
			if c.kind[childID] != objDir {
				continue
			}

			c.liveChildren[childID] = copyChildren(c.durableChildren[childID])
			walk(childID)
		}
	}
	walk(rootID)
}

func (c *Crash) liveLookupLocked(rel string) (objID, objKind, bool) {
	if rel == "" {
		return rootID, objDir, true
	}

	parts := strings.Split(rel, string(os.PathSeparator))

	dir := rootID
	for i, part := range parts {
		children, ok := c.liveChildren[dir]
		if !ok {
			return 0, 0, false
		}

		child, ok := children[part]
		if !ok {
			return 0, 0, false
		}

		kind, ok := c.kind[child]
		if !ok {
			return 0, 0, false
		}

		if i == len(parts)-1 {
			return child, kind, true
		}

		if kind != objDir {
			return 0, 0, false
		}

		dir = child
	}

	return 0, 0, false
}

func (c *Crash) liveDirIDLocked(relDir string) (objID, error) {
	id, kind, ok := c.liveLookupLocked(relDir)
	if !ok {
		return 0, os.ErrNotExist
	}

	if kind != objDir {
		return 0, fmt.Errorf("%q is not a directory", relDir)
	}

	return id, nil
}

func (c *Crash) liveEnsureDirPathLocked(relDir string) (objID, error) {
	if relDir == "" {
		return rootID, nil
	}

	parts := strings.Split(relDir, string(os.PathSeparator))

	dir := rootID
	for _, part := range parts {
		children := c.liveChildren[dir]

		child, ok := children[part]
		if ok {
			if c.kind[child] != objDir {
				return 0, fmt.Errorf("%q exists and is not a directory", filepath.Join(parts...))
			}

			dir = child

			continue
		}

		newID := c.allocIDLocked(objDir)
		children[part] = newID
		dir = newID
	}

	return dir, nil
}

func (c *Crash) liveRemoveEntryLocked(rel string) {
	if rel == "" {
		return
	}

	parent := parentRel(rel)
	base := filepath.Base(rel)

	parentID, err := c.liveDirIDLocked(parent)
	if err != nil {
		return
	}

	delete(c.liveChildren[parentID], base)
}

func (c *Crash) liveAddFileLocked(rel string) (objID, error) {
	if rel == "" {
		return 0, errors.New("cannot create root as file")
	}

	parent := parentRel(rel)
	base := filepath.Base(rel)

	parentID, err := c.liveDirIDLocked(parent)
	if err != nil {
		return 0, err
	}

	if existing, k, ok := c.liveLookupLocked(rel); ok {
		if k != objFile {
			return 0, fmt.Errorf("%q exists and is not a file", rel)
		}

		return existing, nil
	}

	id := c.allocIDLocked(objFile)
	c.liveChildren[parentID][base] = id

	return id, nil
}

// resolveLocked resolves a user path to the live work dir and a root-relative rel path.
//
// It applies root-scoped path rules and returns both the absolute on-disk path and
// the normalized relative path. Callers must hold [Crash.mu].
func (c *Crash) resolveLocked(path string) (string, string, error) {
	if c.live == "" {
		return "", "", CrashFSErr("resolve live path", errors.New("crashfs not initialized"))
	}

	rel, err := c.virtualRel(path)
	if err != nil {
		return "", "", err
	}

	if rel == "" {
		return c.live, "", nil
	}

	return filepath.Join(c.live, rel), rel, nil
}

func (c *Crash) resolveAbs(path string) (string, error) {
	c.mu.Lock()
	abs, _, err := c.resolveLocked(path)
	c.mu.Unlock()

	return abs, err
}

func (c *Crash) resolveWithLive(path string) (resolveResult, error) {
	c.mu.Lock()
	abs, rel, err := c.resolveLocked(path)
	live := c.live
	c.mu.Unlock()

	return resolveResult{abs: abs, rel: rel, live: live}, err
}

func (c *Crash) resolvePairWithLive(oldpath, newpath string) (resolvePairResult, error) {
	c.mu.Lock()

	oldAbs, oldRel, err := c.resolveLocked(oldpath)
	if err != nil {
		c.mu.Unlock()

		return resolvePairResult{}, err
	}

	newAbs, newRel, err := c.resolveLocked(newpath)
	live := c.live
	c.mu.Unlock()

	return resolvePairResult{
		oldAbs: oldAbs,
		oldRel: oldRel,
		newAbs: newAbs,
		newRel: newRel,
		live:   live,
	}, err
}

// virtualRel normalizes a user path into root-relative form.
//
// Absolute paths become root-relative ("/a" -> "a"). Relative paths are treated
// as root-relative as well. "." and "" refer to the root, and relative paths that
// clean to a leading ".." are rejected.
func (*Crash) virtualRel(path string) (string, error) {
	if path == "" {
		return "", nil
	}

	clean := filepath.Clean(path)
	if clean == "." {
		return "", nil
	}

	if filepath.IsAbs(clean) {
		clean = strings.TrimPrefix(clean, string(os.PathSeparator))
		if clean == "" {
			return "", nil
		}

		return clean, nil
	}

	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("relative path %q escapes crashfs root", path)
	}

	return clean, nil
}
