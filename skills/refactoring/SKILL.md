---
name: refactoring
description: Restructure existing code without changing its behaviour - split large files or functions, remove duplication, rename, untangle dependencies, modernise APIs - in small, tested, reversible steps. Use when code is hard to change, before adding a feature to messy code, or when the user asks to "clean up" or "refactor".
tags: [code, refactoring, quality, architecture]
version: 1
---

# Refactoring

A refactor changes structure, never behaviour. If behaviour must change too, do it in a
separate step (and commit).

## 1. Before touching anything

- State the goal in one line ("make the skills index testable without a real home dir").
- Make sure tests cover the code you'll move. If not, write **characterisation tests** first: capture current outputs for representative inputs (even odd ones) so you'll notice any change.
- Run the suite and record the baseline (all green; timing; build size if relevant).
- Check who depends on it: `grep -rn` for the names, exported APIs, file formats, CLI flags, other repos/apps sharing data.

## 2. Work in small steps

Each step compiles and passes tests; commit after each meaningful one. Common moves:

| Smell | Move |
|---|---|
| Long function | Extract function with a name that says *what*; keep the call site reading like prose |
| Huge file / god object | Split by responsibility into new files/packages; keep a thin facade for callers |
| Duplicated code (3+ copies) | Extract a shared helper; parametrise the differences |
| Long parameter list | Group into a struct/options object |
| Deep nesting | Guard clauses and early returns |
| Boolean flag arguments | Two functions, or an enum/option |
| Magic numbers/strings | Named constants |
| Global state | Pass dependencies in (constructor/params); interface at the boundary for tests |
| Comments explaining confusing code | Rename/restructure so the comment is unnecessary; keep *why* comments |
| Dead code | Delete it (git remembers) |

Use tool support where it exists (gopls rename, IDE refactors, `gofmt -r`, codemods) over manual search-and-replace; review every replacement.

## 3. Keep it safe

- Preserve public behaviour exactly: same outputs, errors, file formats, logs others parse.
- Keep compatibility shims (old names as aliases, deprecated flags) when other code or users depend on them; note a removal date.
- Don't mix formatting-only changes with logic moves in one commit.
- Performance-sensitive code: benchmark before and after.
- Stop when the goal is met; don't refactor the whole codebase on the way.

## 4. Finish

- [ ] Tests green, linters clean, build passes on all targets.
- [ ] Diff reviewed: no behaviour change hidden in the moves.
- [ ] Explain to the user: what moved where, why, and how it was verified (test counts before/after).
