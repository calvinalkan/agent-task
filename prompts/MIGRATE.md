0a. Study @migration_plan.md.
0b. The application source code is in `@internal/*`.

1. Pick the most important item to task from @migration_plan.md and implement **just that task**. 
Before making changes, search the codebase (don't assume not implemented, or implemented).
Write tests for new/changed functionality. Follow testing guidelines and look at existing tests. We want e2e/integration tests,
minimal unit tests.
2. After implementing functionality or resolving problems, run the tests for that unit of code that was improved. If functionality is missing then it's your job to add it.
3. When `make test` and `make lint` update @migration_plan.md, then `git add -A` then `git commit` with a message describing the changes (use the `commit` skill).

Important: Author documentation, capture the why, for both tests and source code..
