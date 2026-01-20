0a. Study `@pkg/slotcache/specs/README.md` to learn the application specifications.
0b. Study @IMPLEMENTATION_PLAN.md.
0c. The application source code is in `@pkg/slotcache/*`.

1. Your task is to implement functionality per the specifications. Follow the technical direction outlined in 
@pkg/slotcache/specs/TECHNICAL_DECISIONS.md.
 Pick the most important item to task from @IMPLEMENTATION_PLAN.md and implement **just that task**. 
Before making changes, search the codebase (don't assume not implemented, or implemented).
2. After implementing functionality or resolving problems, run the tests for that unit of code that was improved. If functionality is missing then it's your job to add it as per the application specifications.
3. When `make test` and `make lint` passes, run all fuzz tests with `FUZZ_TIME=5s make fuzz-slotcache`. If all good, then update @IMPLEMENTATION_PLAN.md, then `git add -A` then `git commit` with a message describing the changes (use the `commit` skill).

Important: When authoring documentation, capture the why — tests and implementation importance.
