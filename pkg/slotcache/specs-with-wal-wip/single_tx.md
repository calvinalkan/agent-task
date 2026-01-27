 Here’s the minimal “single‑txn WAL + 1 msync” sequence that gives atomicity via replay:

 ────────────────────────────────────────────────────────────────────────────────

 ✅ Commit (durable, 1 msync)

 1. Build WAL buffer in memory
     - Include all ops (PUT/DEL/UserHeader), plus a COMMIT marker and CRCs.
     - WAL is redo‑only (after‑images or logical ops that can be replayed safely).
 2. Write WAL to WAL region (mmapped or pwrite).
 3. msync WAL region
     - This is the only durable step.
     - After this point, the transaction is recoverable.
 4. Set generation odd (seqlock).
 5. Apply in‑place updates (slots/buckets/header counters).
 6. Set generation even (no msync required).
 7. Optionally clear WAL in memory (not synced).

 ────────────────────────────────────────────────────────────────────────────────

 ✅ Recovery on Open

 1. Read WAL header/state.
 2. If WAL has COMMIT marker + valid CRC:
     - Replay WAL into slots/buckets/header counters.
     - Set generation even.
     - Clear WAL (in memory).
 3. If WAL incomplete/corrupt:
     - Ignore WAL, treat as no committed txn.
 4. Validate invariants; if fail ⇒ rebuild.

 ────────────────────────────────────────────────────────────────────────────────

 ### Why this is atomic

 Because the only durable thing before the crash is the WAL:

 - If crash happens after WAL msync, replay brings you to new state.
 - If crash happens before WAL msync, WAL incomplete → ignore → old state.

 Even if data pages partially wrote, redo overwrites them.

 ────────────────────────────────────────────────────────────────────────────────

 ### Caveat

 Replay must not depend on current hash index state.
 Either log physical writes (slot id/bucket index + bytes), or log logical ops and rebuild buckets from slots during replay.

 That’s the minimal, 1‑msync durable path.
 ────────────────────────────────────────────────────────────────────────────────

 ⚠️  CRITICAL FLAW: Multiple Transactions

 This design ONLY works for a single transaction. For multiple transactions,
 you MUST msync base before starting the next transaction, resulting in
 2 msyncs per commit (defeating the purpose).

 ────────────────────────────────────────────────────────────────────────────────

 ### Why it fails: Power cut between transactions

 ```
 Tx1                              Tx2                              Power Cut
  │                                │                                   │
  ▼                                ▼                                   ▼
 ┌─────────────────┐             ┌─────────────────┐                   
 │ Write WAL1      │             │ Write WAL2      │  ← overwrites WAL1
 │ msync WAL1  ✓   │             │ msync WAL2  ✓   │                   
 │ Apply to base   │             │ Apply to base   │                   ⚡ CRASH
 │ (not synced)    │             │ (not synced)    │                   
 └─────────────────┘             └─────────────────┘                   
                                                                       
 On disk after crash:                                                  
 ┌──────────────────────────────────────────────────────────────────┐  
 │ WAL:  WAL2 (synced)                                         ✓   │  
 │ Base: ??? (original? partial Tx1? partial Tx2? unknown!)    ✗   │  
 └──────────────────────────────────────────────────────────────────┘  
                                                                       
 Recovery:                                                             
 ┌──────────────────────────────────────────────────────────────────┐  
 │ 1. WAL2 is valid → replay Tx2                                   │  
 │ 2. But Tx1 was NEVER synced to base, and WAL1 is GONE           │  
 │ 3. Result: Tx1 is LOST forever                                  │  
 │                                                                  │  
 │ ❌ UNDEFINED STATE - committed transaction lost                  │  
 └──────────────────────────────────────────────────────────────────┘  
 ```

 ### The fundamental problem

 - WAL can only protect ONE transaction at a time
 - To start Tx2, WAL1 must be overwritten
 - But Tx1's base changes weren't synced
 - Power loss → Tx1 lost, even though it was "committed"

 ### To fix, you need one of:

 1. **msync base before next tx** → 2 msyncs per commit (WAL + base)
 2. **Ring WAL** → keep all committed txns in WAL until checkpoint
 3. **Only ever do one transaction** → write-once cache

 ### Conclusion

 For ongoing writes with durability, use a ring WAL (see format_new.md).
 Single-tx WAL only works for write-once use cases.
