# testutil — slotcache test infrastructure

Test-only infrastructure for `pkg/slotcache/*_test.go`.

## What this package provides

- **Behavior testing**: Compare the real slotcache against an in-memory model to verify public API semantics.
- **Spec testing**: Drive the real implementation, then validate the on-disk file format with a spec oracle.
- **Deterministic fuzzing**: Convert arbitrary bytes into reproducible operation sequences.

## Files

```
testutil/
├── byte_stream.go      # ByteStream: deterministic byte reader for fuzz inputs
├── ops.go              # Operation types (OpPut, OpScan, etc.) and results
├── opgenerator.go      # OpGenerator: bytes → operations using configurable weights
│
├── harness.go          # Harness + ApplyModel/ApplyReal + RunBehavior
├── compare_state.go    # CompareState/CompareStateLight: model vs real comparison
│
├── curated_seeds.go    # Pre-built byte sequences for important code paths
├── seed_builder.go     # Fluent builders for creating seeds
│
├── spec_oracle.go      # ValidateFile: file format invariant checker
├── options.go          # Fuzz-derived options generation
└── model/              # In-memory behavioral model
```

## Behavior testing flow

```
RunBehavior(tb, opts, opSource, cfg)
    │
    ├── NewHarness(opts)
    │     ├── Model: in-memory (testutil/model)
    │     └── Real: slotcache.Open(opts)
    │
    └── for each op:
          ├── op = opSource.NextOp(writerActive, seenKeys)
          ├── modelResult = ApplyModel(harness, op)
          ├── realResult = ApplyReal(harness, op)
          ├── AssertOpMatch(op, modelResult, realResult)
          └── CompareState (on commit/close/reopen)
```

## Spec testing flow

```
FuzzSpec_*(fuzzBytes)
    │
    ├── cache = slotcache.Open(opts)
    ├── opGen = NewOpGenerator(fuzzBytes, opts, SpecOpSet)
    │
    └── for each op:
          ├── Apply op to real cache
          └── ValidateFile(path, opts) at checkpoints
```
