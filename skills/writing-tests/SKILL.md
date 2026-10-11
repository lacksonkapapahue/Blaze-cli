---
name: writing-tests
description: Write or improve automated tests - unit, integration, table-driven, regression, snapshot or end-to-end - for Go, Python, JavaScript/TypeScript and others, so they catch real bugs and stay fast and reliable. Use when adding features, fixing bugs, raising coverage or when tests are flaky.
tags: [code, testing, go, python, javascript, quality]
version: 1
---

# Writing tests

## 1. Decide what to test

- Behaviour, not implementation: inputs → outputs and visible side effects (files written, rows saved, events emitted, HTTP calls made).
- Prioritise: logic with branches, money/time/dates, parsing, migrations, security checks, anything that broke before.
- For a bug fix, the first test reproduces the bug and fails before the fix.
- Cover per function: the happy path, edge cases (empty, zero, one, many, max, unicode, very long), invalid input, and failure of dependencies.

## 2. Use the project's tools

Find them first (`ls`, `package.json` scripts, `go.mod`, `pyproject.toml`, existing `*_test.*` files) and copy the existing style.

- **Go**: `foo_test.go` in the same package; table-driven tests with `t.Run(name, …)`; `t.TempDir()`, `t.Setenv()`; `httptest.NewServer` for HTTP; `testing/fstest.MapFS` for files; run `go test ./... -race -count=1`.
- **Python**: pytest; fixtures for setup; `tmp_path`, `monkeypatch`; `pytest.mark.parametrize` for tables; `responses`/`respx` for HTTP; run `pytest -q -x`.
- **JS/TS**: the repo's runner (vitest/jest/node:test); `describe/it`; fake timers for time; `msw` for HTTP; Playwright for end-to-end and screenshots.
- No framework at all? A plain script with asserts that exits non-zero on failure is still a test (e.g. `node ci/x.test.js`).

## 3. Write good tests

- Arrange - Act - Assert; one behaviour per test; a name that states it (`TestSeedNeverOverwritesEdits`).
- Table-driven when cases differ only by data:
  ```go
  for _, c := range []struct{ in string; want bool }{{"ok", true}, {"", false}} {
      if got := Valid(c.in); got != c.want { t.Errorf("Valid(%q) = %v, want %v", c.in, got, c.want) }
  }
  ```
- Failure messages show input, got and want.
- Deterministic: no real network, no wall-clock sleeps, no dependence on test order or the machine's time zone/locale; seed randomness; use temp dirs.
- Fakes over mocks: a small in-memory implementation of an interface beats asserting call sequences.
- Keep them fast (unit tests in milliseconds); tag slow integration tests.
- Test the public API of a package; test private helpers only when they hold tricky logic.

## 4. Flaky tests

Reproduce with `-count=50` / `--repeat`; usual causes: shared state, time, ordering of maps/sets, races, real network, leftover files. Fix the cause; never just add retries.

## 5. Checklist

- [ ] New/changed behaviour covered, including a failure path.
- [ ] Tests fail when the code is broken (temporarily break it once to check).
- [ ] Whole suite passes locally (with `-race` for Go concurrency code) and in CI.
- [ ] No skipped tests without a reason and an issue.
