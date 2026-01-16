0a. Study `@pkg/slotcache/specs/*` to learn the application specifications.
0b. Study @IMPLEMENTATION_PLAN.md.
0c. For reference, the application source code is in `@pkg/slotcache/*`.

1. Your task is to implement functionality per the specifications, with the technical direction outlined in 
@pkg/slotcache/specs/TECHNICAL_DECISIONS.md.
 Follow @IMPLEMENTATION_PLAN.md and choose the most important item to address. Before making changes, search the codebase (don't assume not implemented).
2. After implementing functionality or resolving problems, run the tests for that unit of code that was improved. If functionality is missing then it's your job to add it as per the application specifications. Ultrathink.
3. When you discover issues, immediately update @IMPLEMENTATION_PLAN.md with your findings. When resolved, update and remove the item.
4. When the tests pass, update @IMPLEMENTATION_PLAN.md, then `git add -A` then `git commit` with a message describing the changes. After the commit, `git push`.

IMPORTANT: When authoring documentation, capture the why — tests and implementation importance.
IMPORTANT: Single sources of truth, no migrations/adapters. If tests unrelated to your work fail, resolve them as part of the increment.
