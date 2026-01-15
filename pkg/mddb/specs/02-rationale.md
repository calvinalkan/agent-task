# Design rationale

This document captures the “why” behind mddb’s design.

mddb is optimized for a specific workflow:

- local files
- agentic / CLI tooling
- git as the replication and history mechanism

## Why Markdown for agents?

Agents (LLMs operating autonomously) tend to work unusually well with markdown + files:

- **Native search:** tools like `grep`, `ripgrep`, `find` work directly on the source of truth.
- **Native read/write:** agents can read and edit markdown reliably without needing a query language.
- **Partial access:** the tool can read or update *one file* without loading an entire database.
- **Debuggable:** humans can inspect any document with `cat`.
- **Recoverable:** if a single doc is broken, `git checkout <file>` fixes it.

## The stale-index problem

A common pattern is “markdown as truth + SQLite as an index”. The failure mode is **index drift**:

- files are edited outside the indexer (editor, git checkout, agent)
- the index becomes stale
- queries return incorrect results

For autonomous agents, stale reads can be catastrophic: the agent makes decisions based on wrong data.

mddb’s approach is to make the cache explicitly **throwaway**:

- the source of truth is always `*.mddb.md`
- `.cache` may be deleted and rebuilt at any time
- external tools can invalidate the cache cheaply (see [cache](cache.md))

## Why not SQLite?

SQLite is excellent, but it optimizes for a different set of constraints.

| Aspect | SQLite | mddb |
|---|---|---|
| Human readability | binary / SQL tooling | plain markdown files |
| Agent access | SQL queries | grep/cat/direct file reads |
| Git diffs/merges | binary conflict | native diffs + 3-way merge |
| Direct edits | requires a DB client | any editor |
| External changes | index drift unless carefully managed | delete `.cache` to rebuild |
| Concurrent writers | single writer | single writer (v1) |
| Query power | full SQL | filter on indexed fields |

mddb is intended for **thousands of docs**, not millions of rows.
