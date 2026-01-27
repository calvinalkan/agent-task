0a. Study @IMPLEMENTATION_PLAN.md.
0b. The application source code is in `@internal/*`.

1. Pick the most important phase from @IMPLEMENTATION_PLAN.md and implement **just that phase**. 
Before making changes, search the codebase (don't assume not implemented, or implemented).
Write tests for new/changed functionality. Follow testing guidelines and look at existing tests. We want only e2e/integration/api/contract tests, never unit tests for trivial functionality
2. After implementing functionality or resolving problems, run the tests for that unit of code that was improved. If functionality is missing then it's your job to add it.
3. When `make test` and `make lint` update @IMPLEMENTATION_PLAN.md, then `git add -A` then `git commit` with a message describing the changes (atomic/convential commits). `make test` and `make lint` must pass for the entire repo before you can commit.
If there are issues unrelated to your changes, you must fix them.

Important: Author documentation, capture the why, for both tests and source code..
