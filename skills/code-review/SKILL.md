---
name: code-review
description: Review code changes - a diff, a branch, a pull request or a file - for bugs, security, performance, readability and tests, and report findings ranked by severity with concrete fixes. Use when the user asks to "review", "check my code", "look over this PR" or before merging Blaze's own work.
tags: [code, review, quality, security, git]
version: 1
---

# Code review

## 1. Get the change and its context

- Diff: `git diff main...HEAD` (or `git diff --staged`, or the PR's files). Stats first: `git diff --stat main...HEAD`.
- Purpose: PR description, linked issue, commit messages. If unclear, ask what it's meant to do before judging it.
- Read the surrounding code of every changed function, its callers (`grep -rn "FuncName("`), and existing tests.
- Run what exists: build, linter, tests (`go vet ./... && go test ./...`, `npm test`, `pytest -q`, …). A review that didn't run the tests says so.

## 2. Read in this order

1. **Correctness**: does it do what it claims for normal, empty, huge, concurrent and failing inputs? Off-by-one, nil/null, error paths ignored, wrong defaults, time zones, encodings, float money.
2. **Security**: untrusted input reaching shell/SQL/HTML/paths (injection, traversal), secrets in code or logs, authz checks missing, unsafe deserialisation, permissive CORS, dependencies with known CVEs.
3. **Data & state**: migrations reversible and idempotent, no data loss on upgrade, races (shared maps, goroutines, async without await), resource leaks (files, connections, timers, listeners).
4. **Behaviour change**: public APIs, config, file formats, CLI flags: backwards compatible or flagged.
5. **Performance**: work inside loops, N+1 queries, unbounded memory, blocking the UI thread, missing pagination or timeouts.
6. **Tests**: new behaviour covered, including the failure case; tests assert outcomes, not implementation details; no flaky sleeps.
7. **Readability**: names say what things are, functions do one thing, no dead code or commented-out blocks, comments explain *why*.
8. **Consistency** with the codebase's patterns, formatting (gofmt/prettier) and error style.

## 3. Report

Group by severity, most important first; each finding = location, problem, why it matters, fix:

```
BLOCKER  internal/store/index.go:57  ReplaceSkills deletes before insert outside a transaction → a crash mid-way empties the index. Wrap both in one tx (as SyncTools does).
MAJOR    app.go:612  error from saveConv is ignored → lost chats on a full disk. Return it and show the error card.
MINOR    learn.go:40  "workTools" comment doesn't match the variable name lookTools.
NIT      naming: tmp → manifestTmp.
```

- **Blocker**: wrong results, data loss, security hole, crash, breaks the build.
- **Major**: likely bug in a real case, missing test for risky logic, notable perf issue.
- **Minor / Nit**: clarity, naming, style. Keep nits few; never block on taste.
- Say what is good too (one line) and give an overall verdict: approve / approve with changes / request changes.
- Offer to apply the fixes. When fixing, make each change small, rerun tests, show the diff.

## 4. Reviewing your own work (before saying "done")

- [ ] Re-read the full diff as if someone else wrote it.
- [ ] Formatter and linter clean; tests added and passing; build passes for every target OS you ship.
- [ ] No debug prints, TODOs without an owner, secrets, or unrelated changes.
- [ ] The PR description says what changed, why, how it was tested, and any risk.
