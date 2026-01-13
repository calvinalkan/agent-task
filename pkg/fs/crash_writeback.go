package fs

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
)

// CrashWritebackConfig configures the optional crash writeback model.
//
// When enabled, crashfs can retain some unsynced file and directory changes at
// crash time based on deterministic, weighted choices. This intentionally models
// a simplified page-cache writeback: per file it chooses old/new/prefix contents,
// and per directory entry it chooses old/new presence.
//
// Weights are relative: values must be >= 0, 0 disables an outcome, and the
// remaining weights are normalized at runtime. They do not need to sum to 1.
//
// The zero value disables writeback and preserves the strict durability model.
//
// Example:
//
//	crash, _ := fs.NewCrash(t, fs.NewReal(), fs.CrashConfig{
//		Writeback: fs.CrashWritebackConfig{
//			Seed:           1,
//			FileWeights:     fs.CrashWritebackFileWeights{KeepOld: 1, KeepNew: 1},
//			DirEntryWeights: fs.CrashWritebackDirEntryWeights{KeepOld: 1, KeepNew: 1},
//		},
//	})
type CrashWritebackConfig struct {
	// Seed seeds the deterministic RNG used for writeback decisions. The same Seed
	// and operation order always produce the same crash outcome.
	Seed int64

	// FileWeights controls how unsynced file contents persist at crash time.
	//
	// If all file weights are zero, writeback falls back to the strict model
	// (KeepOld only).
	FileWeights CrashWritebackFileWeights

	// DirEntryWeights controls how unsynced directory entries persist at crash time.
	//
	// If all dir weights are zero, writeback falls back to the strict model
	// (KeepOld only).
	DirEntryWeights CrashWritebackDirEntryWeights
}

// CrashWritebackFileWeights defines the weighted outcomes for unsynced files.
//
// Weights are relative: 0 disables an outcome and larger values make it more
// likely after normalization.
type CrashWritebackFileWeights struct {
	// KeepOld retains the last durable contents for an unsynced file.
	KeepOld float64
	// KeepNew retains the full new contents for an unsynced file.
	KeepNew float64
	// KeepPrefix retains a prefix of the new contents and a suffix from the old data.
	KeepPrefix float64
}

// CrashWritebackDirEntryWeights defines the weighted outcomes for unsynced dir entries.
//
// Weights are relative: 0 disables an outcome and larger values make it more
// likely after normalization.
type CrashWritebackDirEntryWeights struct {
	// KeepOld retains the last durable directory entry state.
	KeepOld float64
	// KeepNew keeps the updated directory entry state.
	KeepNew float64
}

type crashWriteback struct {
	rng *rand.Rand

	fileWeights     writebackFileWeights
	dirEntryWeights writebackDirEntryWeights
}

type writebackFileWeights struct {
	keepOld    float64
	keepNew    float64
	keepPrefix float64
}

type writebackDirEntryWeights struct {
	keepOld float64
	keepNew float64
}

type writebackFileOutcome uint8

const (
	writebackFileOld writebackFileOutcome = iota
	writebackFileNew
	writebackFilePrefix
)

func newCrashWriteback(cfg CrashWritebackConfig) (*crashWriteback, error) {
	fileSum, err := validateWritebackWeights(cfg.FileWeights.KeepOld, cfg.FileWeights.KeepNew, cfg.FileWeights.KeepPrefix)
	if err != nil {
		return nil, fmt.Errorf("crashfs: invalid writeback file weights: %w", err)
	}

	dirSum, err := validateWritebackWeights(cfg.DirEntryWeights.KeepOld, cfg.DirEntryWeights.KeepNew)
	if err != nil {
		return nil, fmt.Errorf("crashfs: invalid writeback dir entry weights: %w", err)
	}

	if fileSum == 0 && dirSum == 0 {
		return &crashWriteback{
			fileWeights:     writebackFileWeights{keepOld: 1},
			dirEntryWeights: writebackDirEntryWeights{keepOld: 1},
		}, nil
	}

	wb := &crashWriteback{rng: rand.New(rand.NewPCG(uint64(cfg.Seed), uint64(cfg.Seed)))}

	if fileSum == 0 {
		wb.fileWeights.keepOld = 1
	} else {
		wb.fileWeights.keepOld = cfg.FileWeights.KeepOld / fileSum
		wb.fileWeights.keepNew = cfg.FileWeights.KeepNew / fileSum
		wb.fileWeights.keepPrefix = cfg.FileWeights.KeepPrefix / fileSum
	}

	if dirSum == 0 {
		wb.dirEntryWeights.keepOld = 1
	} else {
		wb.dirEntryWeights.keepOld = cfg.DirEntryWeights.KeepOld / dirSum
		wb.dirEntryWeights.keepNew = cfg.DirEntryWeights.KeepNew / dirSum
	}

	return wb, nil
}

func validateWritebackWeights(weights ...float64) (float64, error) {
	sum := 0.0

	for _, weight := range weights {
		if weight < 0 || math.IsNaN(weight) || math.IsInf(weight, 0) {
			return 0, fmt.Errorf("invalid weight %v", weight)
		}

		sum += weight
	}

	return sum, nil
}

func (wb *crashWriteback) chooseDirOutcome() bool {
	if wb.dirEntryWeights.keepNew == 0 {
		return false
	}

	if wb.dirEntryWeights.keepOld == 0 {
		return true
	}

	return wb.rng.Float64() < wb.dirEntryWeights.keepNew
}

func (wb *crashWriteback) chooseFileOutcome() writebackFileOutcome {
	if wb.fileWeights.keepNew == 0 && wb.fileWeights.keepPrefix == 0 {
		return writebackFileOld
	}

	if wb.fileWeights.keepOld == 0 && wb.fileWeights.keepPrefix == 0 {
		return writebackFileNew
	}

	if wb.fileWeights.keepOld == 0 && wb.fileWeights.keepNew == 0 {
		return writebackFilePrefix
	}

	return wb.fileOutcomeForRoll(wb.rng.Float64())
}

func (wb *crashWriteback) fileOutcomeForRoll(roll float64) writebackFileOutcome {
	if roll < wb.fileWeights.keepOld {
		return writebackFileOld
	}

	if roll < wb.fileWeights.keepOld+wb.fileWeights.keepNew {
		return writebackFileNew
	}

	return writebackFilePrefix
}

type snapshotFile struct {
	rel string
	id  objID
}

// listSnapshotFilesLocked returns the set of file IDs reachable from a directory snapshot.
//
// The returned rel path is the snapshot-relative name (not necessarily a current live name).
//
// Callers must hold [Crash.mu].
func (c *Crash) listSnapshotFilesLocked(children map[objID]map[string]objID) []snapshotFile {
	out := make([]snapshotFile, 0)

	var walk func(dirID objID, prefix string)

	walk = func(dirID objID, prefix string) {
		m := children[dirID]
		for _, name := range sortedChildNames(m) {
			childID := m[name]

			var rel string
			if prefix != "" {
				rel = filepath.Join(prefix, name)
			} else {
				rel = name
			}

			switch c.kind[childID] {
			case objDir:
				walk(childID, rel)
			case objFile:
				out = append(out, snapshotFile{rel: rel, id: childID})
			}
		}
	}

	walk(rootID, "")

	return out
}

type writebackRenamePair struct {
	oldName string
	newName string
	id      objID
}

func indexNamesByID(entries map[string]objID) (map[objID]string, map[objID]bool) {
	byID := make(map[objID]string)
	dup := make(map[objID]bool)

	for name, id := range entries {
		if existing, ok := byID[id]; ok {
			dup[id] = true
			if existing > name {
				byID[id] = name
			}

			continue
		}

		byID[id] = name
	}

	return byID, dup
}

// collectWritebackRenamePairs detects directory renames within a single directory.
//
// A rename is represented as the same objID moving from an old-only name to a new-only
// name. Duplicate IDs are ignored to avoid ambiguous pairing.
func collectWritebackRenamePairs(oldOnly, newOnly map[string]objID) []writebackRenamePair {
	oldByID, oldDup := indexNamesByID(oldOnly)
	newByID, newDup := indexNamesByID(newOnly)

	pairs := make([]writebackRenamePair, 0)

	for id, oldName := range oldByID {
		newName, ok := newByID[id]
		if !ok || oldDup[id] || newDup[id] {
			continue
		}

		pairs = append(pairs, writebackRenamePair{oldName: oldName, newName: newName, id: id})
	}

	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].oldName == pairs[j].oldName {
			return pairs[i].newName < pairs[j].newName
		}

		return pairs[i].oldName < pairs[j].oldName
	})

	return pairs
}

// mergeWritebackDirChildrenLocked merges directory entries for writeback.
//
// oldChildren is the durable directory snapshot (what was known durable).
// liveChildren is the live directory state (what exists at crash time).
//
// The merge chooses old/new outcomes using writeback weights:
//   - creates/deletes: choose presence
//   - replacements (same name, different ID): choose which ID keeps the name
//   - renames (same ID, different name): treat as a linked pair (keep only old OR new name)
//
// Callers must hold [Crash.mu].
func (c *Crash) mergeWritebackDirChildrenLocked(oldChildren, liveChildren map[string]objID) map[string]objID {
	final := make(map[string]objID)

	if oldChildren == nil {
		oldChildren = map[string]objID{}
	}

	if liveChildren == nil {
		liveChildren = map[string]objID{}
	}

	oldOnly := make(map[string]objID)
	newOnly := make(map[string]objID)
	replaceNames := make([]string, 0)

	for name, oldID := range oldChildren {
		if newID, ok := liveChildren[name]; ok {
			if oldID == newID {
				final[name] = oldID

				continue
			}

			replaceNames = append(replaceNames, name)

			continue
		}

		oldOnly[name] = oldID
	}

	for name, newID := range liveChildren {
		if _, ok := oldChildren[name]; ok {
			continue
		}

		newOnly[name] = newID
	}

	renamePairs := collectWritebackRenamePairs(oldOnly, newOnly)
	for _, pair := range renamePairs {
		if c.writeback.chooseDirOutcome() {
			final[pair.newName] = pair.id
		} else {
			final[pair.oldName] = pair.id
		}

		delete(oldOnly, pair.oldName)
		delete(newOnly, pair.newName)
	}

	sort.Strings(replaceNames)

	for _, name := range replaceNames {
		if c.writeback.chooseDirOutcome() {
			final[name] = liveChildren[name]
		} else {
			final[name] = oldChildren[name]
		}
	}

	for _, name := range sortedChildNames(oldOnly) {
		if !c.writeback.chooseDirOutcome() {
			final[name] = oldOnly[name]
		}
	}

	for _, name := range sortedChildNames(newOnly) {
		if c.writeback.chooseDirOutcome() {
			final[name] = newOnly[name]
		}
	}

	return final
}

func filterChildrenToLiveMatches(current, live map[string]objID) map[string]objID {
	if live == nil {
		return map[string]objID{}
	}

	filtered := make(map[string]objID)

	for name, id := range current {
		newID, ok := live[name]
		if !ok {
			continue
		}

		if newID == id {
			filtered[name] = id
		}
	}

	return filtered
}

func writebackPrefixMix(rng *rand.Rand, oldData, newData []byte) []byte {
	if len(oldData) == 0 {
		return []byte{}
	}

	maxLen := min(len(oldData), len(newData))

	prefixLen := 0
	if maxLen > 0 {
		prefixLen = rng.IntN(maxLen + 1)
	}

	out := make([]byte, len(oldData))
	copy(out, oldData)
	copy(out[:prefixLen], newData[:prefixLen])

	return out
}

func (c *Crash) simulateWritebackLocked() error {
	// Writeback crash path:
	//   1) Validate the live on-disk tree (reject symlinks).
	//   2) Build a directory-entry snapshot by merging:
	//        - durableChildren (what is known durable)
	//        - liveChildren (what exists in the current work dir)
	//      using deterministic, weighted choices.
	//      This merge must be top-down so directory replacements cannot resurrect
	//      stale durable descendants under a newly chosen directory.
	//   3) Build a file-content snapshot for files reachable from the chosen
	//      directory snapshot (old/new/prefix by weights).
	//   4) Swap durable state, then rotate+restore.
	if c.writeback == nil {
		return c.rotateLocked(false)
	}

	err := c.rejectSymlinksInLiveTreeLocked()
	if err != nil {
		return err
	}

	// Start with a clone of the current durable namespace and apply writeback decisions.
	nextChildren := make(map[objID]map[string]objID, len(c.durableChildren))
	for dirID, children := range c.durableChildren {
		nextChildren[dirID] = copyChildren(children)
	}

	// Invariants: the object graph is intended to be a tree (no cycles). visitedDirs is
	// a defensive guard against bugs that accidentally create cycles.
	visitedDirs := make(map[objID]struct{})

	var mergeDir func(oldDirID, liveDirID, outDirID objID, filterToLive bool) error

	mergeDir = func(oldDirID, liveDirID, outDirID objID, filterToLive bool) error {
		if _, ok := visitedDirs[outDirID]; ok {
			return nil
		}

		visitedDirs[outDirID] = struct{}{}

		var oldChildren map[string]objID
		if oldDirID != 0 {
			oldChildren = c.durableChildren[oldDirID]
		}

		var liveChildren map[string]objID
		if liveDirID != 0 {
			liveChildren = c.liveChildren[liveDirID]
		}

		merged := c.mergeWritebackDirChildrenLocked(oldChildren, liveChildren)
		if filterToLive {
			merged = filterChildrenToLiveMatches(merged, liveChildren)
		}

		nextChildren[outDirID] = merged

		for _, name := range sortedChildNames(merged) {
			childID := merged[name]
			if c.kind[childID] != objDir {
				continue
			}

			oldChildID := objID(0)
			if oldChildren != nil {
				oldChildID = oldChildren[name]
			}

			liveChildID := objID(0)
			if liveChildren != nil {
				liveChildID = liveChildren[name]
			}

			if oldChildID != 0 && liveChildID != 0 && oldChildID != liveChildID && c.kind[oldChildID] == objDir && c.kind[liveChildID] == objDir && childID == liveChildID {
				mergeErr := mergeDir(oldChildID, liveChildID, liveChildID, true)
				if mergeErr != nil {
					return mergeErr
				}

				continue
			}

			if _, ok := c.findLivePathLocked(childID); ok {
				mergeErr := mergeDir(childID, childID, childID, false)
				if mergeErr != nil {
					return mergeErr
				}
			}
		}

		return nil
	}

	err = mergeDir(rootID, rootID, rootID, false)
	if err != nil {
		return err
	}

	nextFiles := make(map[objID]fileSnapshot, len(c.durableFiles))
	for id, snap := range c.durableFiles {
		dataCopy := append([]byte(nil), snap.data...)
		nextFiles[id] = fileSnapshot{data: dataCopy, perm: snap.perm}
	}

	for _, sf := range c.listSnapshotFilesLocked(nextChildren) {
		liveRel, ok := c.findLivePathLocked(sf.id)
		if !ok {
			continue
		}

		abs := filepath.Join(c.live, liveRel)

		info, err := os.Stat(abs)
		if err != nil {
			return CrashFSErr("writeback file", fmt.Errorf("stat live file %q: %w", liveRel, err))
		}

		if info.IsDir() {
			return CrashFSErr("writeback file", fmt.Errorf("expected file at %q, found directory", liveRel))
		}

		newData, err := os.ReadFile(abs)
		if err != nil {
			return CrashFSErr("writeback file", fmt.Errorf("read live file %q: %w", liveRel, err))
		}

		newPerm := info.Mode().Perm()

		oldSnap, hasOld := c.durableFiles[sf.id]
		oldData := oldSnap.data
		oldPerm := oldSnap.perm

		if hasOld && bytes.Equal(oldData, newData) && oldPerm == newPerm {
			continue
		}

		switch c.writeback.chooseFileOutcome() {
		case writebackFileOld:
			if !hasOld {
				delete(nextFiles, sf.id)
			}
		case writebackFileNew:
			nextFiles[sf.id] = fileSnapshot{data: newData, perm: newPerm}
		case writebackFilePrefix:
			mixed := writebackPrefixMix(c.writeback.rng, oldData, newData)

			perm := oldPerm
			if !hasOld {
				perm = newPerm
			}

			nextFiles[sf.id] = fileSnapshot{data: mixed, perm: perm}
		default:
			return CrashFSErr("writeback file", errors.New("unknown outcome"))
		}
	}

	c.durableChildren = nextChildren
	c.durableFiles = nextFiles

	return c.rotateLocked(false)
}
