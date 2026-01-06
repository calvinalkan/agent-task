## 1. Data Model

### Storage Format

Source of truth: Markdown files with YAML frontmatter

```markdown
---
id: task-1
status: open
priority: 1
parent: epic-1
blocked_by: [task-2]
claimed_by: agent-123
claimed_at: 2024-01-15T10:30:00Z
---

Free-form content here. Notes, logs, screenshots, whatever.
```

### Directory Structure

```
.issues/
  issues/
    task-1.md
    task-2.md
    epic-1.md
  .pending          # single operation WAL
  .cache/
    index.bin       # mmap'd binary index
```

### Edge Direction Rules

Single-direction edges only (no dual writes):

| Relationship | Stored on | Field |
|--------------|-----------|-------|
| Parent/Child | Child | `parent` |
| Blocks | Blocked issue | `blocked_by` |

Reverse mappings (`Blocks`, `Children`) are derived in cache.

### Content Freedom

Everything after frontmatter is untracked. Agents and humans can edit freely without cache invalidation.

---

## 2. Index/Cache

### FrontmatterIndex Structure

```go
type FrontmatterIndex struct {
    entries    []Entry              // mmap'd, fixed-size
    byID       map[string]int       // id → entry offset
    AgentTasks map[string][]string  // agentID → []taskID (derived)
}

type Entry struct {
    ID                 [16]byte
    Status             uint8
    Priority           uint8
    OpenBlockerCount   uint16
    OpenChildCount     uint16
    TransitiveUnblocks uint32
    CriticalPathLen    uint16
    FrontmatterOffset  uint64
    FrontmatterLen     uint32
}
```

### Lazy Loading

| Data | When Loaded |
|------|-------------|
| `entries` | Always (mmap'd) |
| `byID` | Always |
| `AgentTasks` | On startup (scan ClaimedBy) |
| Reverse maps (`Blocks`, `Children`) | On first write operation |
| Full frontmatter | On demand per issue |

### Filtering

All filtering uses mmap'd entries. 500k issues in ~10ms.

```go
ready := idx.Filter(func(e *Entry) bool {
    return e.Status == Open &&
           e.OpenBlockerCount == 0 &&
           e.OpenChildCount == 0 &&
           e.ClaimedBy == ""
})
```

---

## 3. Write Semantics / WAL

### Serialized Writes

All writes are serialized. No concurrent write handling needed.

### Pending File (Mini-WAL)

Single `.pending` file tracks in-flight operation:

```
.issues/.pending
```

Contains just the task ID (or operation details for multi-file writes).

### Write Flow

```go
func Complete(agentID, taskID string) error {
    // 1. Write intent
    writePending(taskID)
    
    // 2. Write .md
    issue.Status = "done"
    issue.ClaimedBy = ""
    writeMD(issue)
    
    // 3. Update cache + clear intent
    updateCache(issue)
    clearPending()
    
    return nil
}
```

### Startup Reconciliation

```go
func Load() *Index {
    if pending := loadPending(); pending != nil {
        // .md is source of truth - read and update cache
        issue := readMD(pending)
        updateCache(issue)
        clearPending()
    }
    return loadCache()
}
```

### Crash Recovery

| Crash Point | .md State | Cache State | Recovery |
|-------------|-----------|-------------|----------|
| After intent, before .md | Old | Old | Read old .md, discard pending |
| After .md, before cache | New | Old | Read new .md, update cache |
| After cache clear | New | New | Nothing to do |

### Multi-File WAL (If Needed)

For operations touching multiple files:

```json
{
  "writes": [
    {"id": "task-1", "changes": {"parent": "epic-2"}},
    {"id": "task-2", "changes": {"parent": "epic-2"}}
  ]
}
```

Recovery rolls forward (replays incomplete writes from intent).

---

## 4. DAG Operations

### Cycle Detection

Simple DFS, no library needed:

```go
func (idx *Index) WouldCycle(blocker, blocked string) bool {
    return idx.canReach(blocked, blocker, make(map[string]bool))
}

func (idx *Index) canReach(from, to string, seen map[string]bool) bool {
    if from == to { return true }
    if seen[from] { return false }
    seen[from] = true
    
    for _, b := range idx.Frontmatter[from].BlockedBy {
        if idx.canReach(b, to, seen) { return true }
    }
    return false
}
```

### Cached Metrics

| Field | Meaning | Updated When |
|-------|---------|--------------|
| `OpenBlockerCount` | Open issues blocking this | Issue completed, edge added/removed |
| `OpenChildCount` | Open children | Child completed, child created |
| `TransitiveUnblocks` | Total downstream unblocked by completing | Lazy recompute |
| `CriticalPathLen` | Longest blocker chain | Lazy recompute |

### Count Updates on Write

```go
func (idx *Index) OnComplete(taskID string) {
    // Decrement OpenBlockerCount on everything this blocks
    for _, blockedID := range idx.Blocks[taskID] {
        idx.Entries[blockedID].OpenBlockerCount--
    }
    
    // Decrement parent's OpenChildCount
    if parent := idx.Frontmatter[taskID].Parent; parent != "" {
        idx.Entries[parent].OpenChildCount--
    }
}
```

### Sparse Graph Assumption

Typical distribution:

| Issues | Blockers | % |
|--------|----------|---|
| 80% | 0 | No blockers |
| 16% | 1-2 | Light |
| 3.8% | 3-5 | Some |
| 0.2% | 5+ | Hub nodes |

Traversals are fast. Most DFS hits dead ends quickly.

### Health Check

```go
func (idx *Index) Density() float64 {
    n := float64(len(idx.Entries))
    e := float64(idx.EdgeCount)
    return e / (n * (n - 1))
}
// 0.0001 = healthy sparse
// 0.1 = highly coupled, problem
```

---

## 5. Agent Coordination

### Claim Fields

```yaml
---
claimed_by: agent-123
claimed_at: 2024-01-15T10:30:00Z
---
```

### Claim Operation

```go
func (idx *Index) Start(agentID, taskID string) error {
    return idx.ReadCheckWrite(taskID, func(issue *Issue) error {
        if issue.ClaimedBy != "" {
            return ErrAlreadyClaimed
        }
        issue.ClaimedBy = agentID
        issue.ClaimedAt = time.Now()
        issue.Status = InProgress
        return nil
    })
}
```

### Claim Timeout (Stuck Agents)

```go
func (idx *Index) ClaimOrSteal(agentID, taskID string, timeout time.Duration) error {
    return idx.ReadCheckWrite(taskID, func(issue *Issue) error {
        if issue.ClaimedBy != "" && time.Since(issue.ClaimedAt) < timeout {
            return ErrAlreadyClaimed
        }
        issue.ClaimedBy = agentID
        issue.ClaimedAt = time.Now()
        return nil
    })
}
```

### Multiple Tasks Per Agent

Index allows it. Higher layer decides limits:

```go
type FrontmatterIndex struct {
    // ...
    AgentTasks map[string][]string  // agentID → []taskID
}
```

Index only enforces: no double claims on same task.

### Creating Children

No claim on parent needed. Single file write:

```yaml
# new-child.md
---
parent: epic-1
---
```

Two agents can create siblings concurrently.

---

## 6. Prioritization

### Graph Metrics vs Business Priority

| Source | Measures |
|--------|----------|
| Graph (CriticalPathLen, TransitiveUnblocks) | Throughput, what unblocks work |
| Priority field | Business value, urgency |

### Combined Sort

```go
sort.Slice(tasks, func(a, b int) bool {
    // 1. Own work first (avoid context switch)
    aMine := tasks[a].ClaimedBy == agentID
    bMine := tasks[b].ClaimedBy == agentID
    if aMine != bMine {
        return aMine
    }
    
    // 2. Critical path (bottlenecks)
    if tasks[a].CriticalPathLen != tasks[b].CriticalPathLen {
        return tasks[a].CriticalPathLen > tasks[b].CriticalPathLen
    }
    
    // 3. Unblocks most work
    if tasks[a].TransitiveUnblocks != tasks[b].TransitiveUnblocks {
        return tasks[a].TransitiveUnblocks > tasks[b].TransitiveUnblocks
    }
    
    // 4. Business priority as tiebreaker
    return tasks[a].Priority < tasks[b].Priority
})
```

### Query Functions

```go
// What anyone can grab
func (idx *Index) Claimable() []*Issue {
    return idx.Filter(func(e *Entry) bool {
        return e.Status == Open &&
               e.ClaimedBy == "" &&
               e.OpenBlockerCount == 0 &&
               e.OpenChildCount == 0
    })
}

// What this agent should consider
func (idx *Index) ReadyFor(agentID string) []*Issue {
    return idx.Filter(func(e *Entry) bool {
        if e.OpenBlockerCount != 0 || e.OpenChildCount != 0 {
            return false
        }
        
        isMine := e.ClaimedBy == agentID
        isClaimable := e.Status == Open && e.ClaimedBy == ""
        
        return isMine || isClaimable
    })
}
```

### Higher Layer Orchestration

Index is dumb. Orchestrator decides:

```go
func (o *Orchestrator) AssignNext(agentID string) *Issue {
    current := o.index.AgentTasks[agentID]
    
    if len(current) >= o.maxPerAgent {
        return nil  // finish something first
    }
    
    ready := o.index.Claimable()
    task := o.pickBest(agentID, ready)
    o.index.Start(agentID, task.ID)
    return task
}
```

---

## 7. CLI / Agent Interface

### Safe Content Editing

Agents write full files. CLI validates frontmatter changes.

```bash
# 1. Start edit session
$ issues edit task-1 --start
/tmp/issues/task-1.md

# 2. Agent reads, edits, writes to that path

# 3. Merge back
$ issues edit task-1 --finish
✓ Validated and merged
```

### Implementation

```go
func tmpPath(taskID string) string {
    return filepath.Join(os.TempDir(), "issues", taskID+".md")
}

func EditStart(taskID string) string {
    tmp := tmpPath(taskID)
    copyFile(originalPath(taskID), tmp)
    fmt.Println(tmp)
    return tmp
}

func EditFinish(taskID string) error {
    tmp := tmpPath(taskID)
    if !exists(tmp) {
        return ErrNoEditSession
    }
    
    // Parse and validate
    newFM, newContent := parse(tmp)
    oldFM, _ := parse(originalPath(taskID))
    
    // Validate frontmatter changes
    if newFM.ID != oldFM.ID {
        return ErrCantChangeID
    }
    if blockedByChanged(newFM, oldFM) {
        for _, b := range newFM.BlockedBy {
            if wouldCycle(b, taskID) {
                return ErrWouldCycle
            }
        }
    }
    
    // Write and update cache
    writeMD(taskID, newFM, newContent)
    updateCache(taskID, newFM)
    os.Remove(tmp)
    
    return nil
}
```

### Command Cost

| Command | Loads |
|---------|-------|
| `show <id>` | 1 frontmatter |
| `list --status=open` | mmap only |
| `list --ready` | mmap only |
| `complete <id>` | mmap + reverse maps |
| `block <id> --by <id>` | mmap + frontmatter (DFS) |

---

## 8. Invariants

### Data Integrity

- .md files are always source of truth
- Cache is derived, fully rebuildable from .md files
- No cycles in blocking DAG
- No cycles in parent DAG
- Single-direction edges (no dual file writes)

### Claim Integrity

- No duplicate claims (one agent per task)
- Multiple tasks per agent allowed
- Claim timeout handles stuck agents

### Edge Constraints

- Can't block on epic AND its child (redundant)
- Adding edge requires cycle check

```go
func (idx *Index) AddBlocker(blockedID, blockerID string) error {
    // Cycle check
    if idx.WouldCycle(blockerID, blockedID) {
        return ErrWouldCycle
    }
    
    // Redundancy check
    for _, existing := range idx.Frontmatter[blockedID].BlockedBy {
        if idx.IsAncestor(existing, blockerID) {
            return ErrRedundantBlocker
        }
        if idx.IsDescendant(existing, blockerID) {
            return ErrRedundantBlocker
        }
    }
    
    // ...add edge
}
```

### Write Integrity

- Serialized writes (no concurrent modifications)
- Pending file tracks in-flight operation
- Startup reconciles incomplete writes
- Frontmatter changes must go through CLI
- Content changes are free (untracked)

---

## Summary

A document database with:

| Concern | Solution |
|---------|----------|
| Storage | Markdown files |
| Schema | YAML frontmatter |
| Free content | Markdown body |
| Index | Mmap'd binary cache |
| Queries | Filter on cached fields |
| DAG | Cached counts + DFS |
| Transactions | Serialized + pending file |
| Recovery | Reconcile from .md |
| Replication | Git |
| History | Git |
| Agent coordination | Claims + orchestrator |
