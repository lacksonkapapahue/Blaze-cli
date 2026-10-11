---
name: debugging
description: Find and fix the root cause of a bug, crash, failing test, wrong output, build error or "it worked yesterday" - systematically, with a reproduction, one hypothesis at a time, and a regression test. Use whenever something is broken or behaves unexpectedly.
tags: [code, debugging, errors, testing, troubleshooting]
version: 1
---

# Debugging

Rule zero: don't guess-and-patch. Reproduce, locate, understand, fix, prove.

## 1. Reproduce

- Get the exact symptom: full error text and stack trace, command run, input, expected vs. actual, OS, versions, when it started.
- Reproduce it yourself with the smallest command (`go test ./pkg -run TestX`, `pytest tests/test_x.py::test_y -x`, `npm test -- -t "name"`, a curl call). Save it as a script if it takes more than one step.
- If you can't reproduce: collect more (logs, env vars, config, data), compare environments, check whether it's intermittent (run it 20×).

## 2. Read the evidence

- Read the error bottom-up: the first frame in *your* code is usually the place; the message usually says what's wrong. Search the exact message in the codebase (`grep -rn`) and on the web (in quotes) if it comes from a library.
- Check what changed: `git log --oneline -15`, `git diff HEAD~3`, lockfile changes, dependency upgrades, config, environment, data.
- Regression with a known good commit? `git bisect start; git bisect bad; git bisect good <sha>; git bisect run <test-cmd>`.

## 3. Narrow down

- Form one hypothesis at a time and a test that would disprove it. Write them down.
- Halve the search space: comment out / short-circuit half the pipeline, feed known-good input midway, check intermediate values.
- Add temporary logging at boundaries (input, output, branch taken) with clear prefixes (`DEBUG:`), or use a debugger (`dlv test`, `python -m pdb`, `node --inspect`, browser devtools).
- Usual suspects: nil/undefined, off-by-one, wrong types/units, stale cache or build artefacts (clean and rebuild), async ordering/race (`go test -race`), time zones and DST, encoding (UTF-8/CRLF), path separators on Windows, permissions, env differences between shells/CI, version mismatch.

## 4. Fix

- Fix the cause, not the symptom (no `try/except: pass`, no retry loops hiding a bug, no "sleep 2").
- Smallest change that makes the reasoning true; keep unrelated cleanups for later.
- Write a regression test that fails before the fix and passes after. Run the whole relevant suite.
- Remove the temporary logging.

## 5. Report

Tell the user in a few lines: **cause** (one sentence, plain words), **fix** (file and what changed), **proof** (test/command output), and anything left at risk. If you stopped without a fix, say what you ruled out and the next best hypothesis.

## Environment-specific tips

- **CI only**: read the full job log; compare tool versions; reproduce with the same OS image or container; check secrets/env vars and case-sensitive paths (Linux) vs. Windows paths.
- **Frontend**: browser console + network tab; check the built file actually changed (cache); test with extensions off.
- **Performance bug**: measure first (`time`, profiler, `pprof`, Lighthouse), change one thing, measure again.
- **Works on my machine**: diff `env`, versions (`node -v`, `go version`), config files, and data.

## Checklist

- [ ] Reproduced with one command before changing code.
- [ ] Root cause stated in one sentence and confirmed by evidence, not a guess.
- [ ] Regression test fails before the fix and passes after; full suite green.
- [ ] Temporary logging removed; the user told cause, fix and proof.
