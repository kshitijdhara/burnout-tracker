---
name: review-pr
description: Review a PR or branch to make sure it is performant, scalable, handles scale, is correct, is maintainable, is secure and follows clean code principles.
argument-hint: "[PR number | github.com PR URL | branch] [low | medium | high | xhigh | max]"
disable-model-invocation: true
---

Review the change named by `$ARGUMENTS` and make sure it is performant, scalable, handles scale, is correct, is maintainable and is secure, and that it follows clean code principles.

## 1. Read the arguments

Split the arguments on whitespace. A word that is `low`, `medium`, `high`, `xhigh` or `max` (any case, also written `effort=<level>` or `--effort=<level>`) is the **effort level**; take it out. What is left is the **target**. With no level given, the effort is `high`.

- Two levels, or a level that is not one of those five, is a mistake: say so and stop.
- `ultra` is not a level here. `/code-review ultra` is a billed cloud review the user starts themselves, so tell them to run it directly, and stop.

The effort level sets how deep every step goes:

| Effort | `/code-review` level | Subagent effort | Context read | Gap pass | Findings reported |
| --- | --- | --- | --- | --- | --- |
| `low` | `low` | `low` | Changed hunks and the code right around them | One subagent, all its criteria | Blockers and Should fix you are confident in; no Nits |
| `medium` | `medium` | `medium` | Changed files whole | One subagent, all its criteria | Blockers, Should fix, and Nits that break a project rule |
| `high` | `high` | `high` | Changed files whole, plus their direct callers and callees | One subagent, all its criteria | Everything confirmed |
| `xhigh` | `xhigh` | `xhigh` | As `high`, plus callers and callees two levels out and the tests that cover them | One subagent per criterion | Everything confirmed, plus a **Plausible** group for findings with a credible but unproven scenario |
| `max` | `max` | `max` | As `xhigh`, plus every other use of each changed type, function, table, event or endpoint | One subagent per criterion | As `xhigh`, and every Blocker and Should fix is checked by a second, independent subagent |

Pass the subagent effort as the Agent tool's `effort` for every subagent this skill starts. Say the target and the effort level in one line before starting.

## 2. Get the change

Work out the target before running anything:

- **Empty**: the current branch. Find the default branch (`git symbolic-ref refs/remotes/origin/HEAD`, falling back to `main`) and `git fetch origin <default>` so the base is current, then read `git diff $(git merge-base origin/<default> HEAD)` plus `git status` for uncommitted work. If the branch has an open PR (`gh pr view`), read its title and description too.
- **A PR number (`123` or `#123`) or a `https://github.com/<owner>/<repo>/pull/<n>` URL**: use the gh CLI, `gh pr view <n> --json title,body,baseRefName,headRefName,headRefOid,files` and `gh pr diff <n>`. If the PR's head is not checked out, fetch it and read changed files at the head commit with `git show <sha>:<path>`.
- **A branch name** (letters, digits, `.`, `_`, `/`, `-`, not starting with `-`): fetch the default branch as above, then read `git diff $(git merge-base origin/<default> <branch>) <branch>` and `git log` for that range, plus its PR description if one exists.
- **Anything else**: do not pass it to a shell. Say it is not a PR number, PR URL or branch, and stop.

## 3. Get the context

- Read as much of the code around the change as the effort level's **Context read** says.
- Read the repository's own rules (CLAUDE.md, Architecture.md, CONTRIBUTING, lint and tsconfig settings, and any design or convention docs they point to) and hold the change to them. Breaking a documented project rule counts as a finding.

## 4. Run the reviews

The reviews run in parallel, each in its own subagent (general-purpose, at the effort level's subagent effort) so their output stays out of this context. Give every subagent the target, the effort level, the diff command from step 2 and the project rules from step 3. Tell each to stay read-only and to end with its findings as a plain list: severity, `file:line`, what is wrong, the concrete failure scenario, and the fix.

1. **`/code-review`** (correctness, reuse, simplification, efficiency): the subagent invokes the `code-review` skill with the target and the effort level's `/code-review` level as args (no target for the current branch). Never pass `--comment`, `--fix` or `ultra`.
2. **`/security-review`** (security): the subagent invokes the `security-review` skill. It reviews the current branch's pending changes only, so run it when the target is the current branch, or the named branch is the one checked out. For any other target skip it, and the gap pass below covers Security instead. Never check out another branch to make it fit.
3. **`/engineering:code-review`** (security, performance, correctness, N+1 queries, edge cases, error handling): the subagent invokes the `engineering:code-review` skill with the PR URL or number, or for a branch, the diff.
4. **Gap pass**: reviews the change itself against the criteria below that the three skills do not cover (Scalability, Handles scale, Maintainability, Clean code, and Security when `/security-review` was skipped), and against the project's own rules. It runs as one subagent or one per criterion, as the effort level's **Gap pass** says.

If one of the skills is unavailable or fails, say so in the report and add its criteria to the gap pass.

The criteria every finding is filed under, and the gap pass's checklist:

1. **Performance**: N+1 queries and queries inside loops; work repeated per request that could be cached or batched; allocations, sorting or parsing on hot paths; sequential awaits that could run in parallel; unneeded re-renders, effects or subscriptions on the client; oversized payloads.
2. **Scalability**: unbounded queries or lists (missing pagination, limits or indexes); in-memory state that breaks with more than one instance; memory that grows with data or traffic; fan-out per user or per item; missing backpressure, timeouts, retries with backoff, or idempotency on jobs and webhooks.
3. **Handles scale**: what happens at 10x and 100x today's data and traffic. Look for races under concurrency (check-then-act, lost updates, double processing), transactions and locking, rate limits, queue depth, large files or inputs, and what happens when a dependency is slow or down.
4. **Correctness**: the change does what it claims; edge cases (empty, null, zero, duplicates, time zones, unicode, boundaries); error paths and partial failure; a contract change (type, event, hook, API shape, DB column) carried through every layer that consumes it; migrations that are safe on existing data; tests that actually cover the new behaviour.
5. **Maintainability**: fits the surrounding architecture and its patterns instead of inventing new ones; clear module boundaries and dependency direction; no hidden coupling or magic values; easy to test and to revert; docs and comments updated where behaviour changed.
6. **Security**: authentication and authorization on every new entry point, including object-level access (can user A reach user B's data?); injection (SQL, shell, template, path traversal); input validation at the trust boundary; XSS and unsafe HTML; SSRF and open redirects; secrets or PII in code, logs or responses; CSRF, CORS and cookie flags; new dependencies.
7. **Clean code**: intention-revealing names; small functions with a single responsibility; no duplication that should be shared; no dead code, commented-out code or leftover debugging; flat control flow with early returns; consistent error handling; types that make invalid states unrepresentable.

## 5. Verify before reporting

Merge the lists: when several reviews report the same problem, keep one finding and note every review that raised it. Then re-read the code behind every finding and confirm it. Keep a finding only if you can name the inputs or conditions that make it go wrong, or quote the project rule it breaks. Drop speculation, style nits the project's linter already enforces, and anything outside the diff unless the diff makes it worse. At `xhigh` and `max`, a finding with a credible scenario you could not prove goes in the Plausible group instead of being dropped; at `max`, a second subagent that has not seen the first one's reasoning checks each Blocker and Should fix, and a finding it refutes is dropped.

## 6. Report

Stay read-only: no edits, commits, pushes or PR comments unless the user asks.

Start with the target and the effort level. List findings most severe first, grouped as **Blocker**, **Should fix**, **Nit** and, at `xhigh` and `max`, **Plausible**, keeping only the groups the effort level's **Findings reported** allows. For each give the criterion, the review or reviews that found it, `file:line`, what is wrong, the concrete scenario where it fails, and the fix. Then give one line for each criterion with no findings, saying what you checked, and one line naming any review that was skipped or failed. End with a one-sentence verdict: approve, approve with changes, or request changes.
