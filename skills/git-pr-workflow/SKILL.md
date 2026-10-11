---
name: git-pr-workflow
description: Day-to-day git and GitHub - branch, commit with good messages, rebase on main, resolve conflicts, push, open a pull request, watch CI, merge, tag a release, and undo mistakes safely. Use for any git or PR task, or when finishing a coding change.
tags: [git, github, pr, ci, release, version-control]
version: 1
---

# Git and pull-request workflow

Always look first: `git status`, `git branch --show-current`, `git log --oneline -5`, `git remote -v`.
Never rewrite shared history (force-push to main), never commit secrets, never run destructive
commands (`reset --hard`, `clean -fd`, `push --force`) without saying what will be lost and getting a yes.

## 1. Start

```bash
git fetch origin
git switch main && git pull --ff-only
git switch -c feat/short-topic        # feat/ fix/ docs/ chore/ refactor/
```

## 2. Commit

- Small, logical commits; stage deliberately (`git add -p` or named files), check `git diff --staged`.
- Message: imperative summary ≤ 72 chars, blank line, body saying *why* and anything notable.
  `fix(store): keep skill index in one transaction` / `feat: seed bundled skills on startup`.
- Run formatter, linter and tests before committing. Don't commit build output, `.env`, keys, large binaries (check `.gitignore`).

## 3. Stay current

```bash
git fetch origin
git rebase origin/main                # own branch only; or `git merge origin/main` if the branch is shared
# conflict: edit files, keep BOTH sides' intent, then
git add <file> && git rebase --continue
git rebase --abort                    # to back out
```
After a rebase, rerun the tests. Push with `git push --force-with-lease` (never plain `--force`).

## 4. Pull request

- Push: `git push -u origin HEAD`.
- Open with `gh pr create --fill` or the GitHub API/UI. Description template:
  ```
  ## What
  ## Why
  ## How tested   (commands + results, screenshots for UI)
  ## Risk / rollout / follow-ups
  ```
- Keep PRs focused (< ~400 lines of real change when possible); link the issue (`Fixes #12`).
- Watch CI: `gh pr checks --watch` or `gh run list --branch <b>` / `gh run view <id> --log-failed`. Fix failures on the branch; don't merge red.
- Respond to review with follow-up commits; resolve threads.

## 5. Merge and release

- Merge method: follow the repo's or the user's rule (merge commit / squash / rebase). If the user has a standing rule, it wins.
- `gh pr merge <n> --merge --delete-branch` (or `--squash`). Then `git switch main && git pull --ff-only`.
- Release: bump the version file, update CHANGELOG, `git tag -a v1.2.0 -m "v1.2.0"` and `git push origin v1.2.0`, or `gh release create v1.2.0 --generate-notes`. Semantic versioning: MAJOR breaking, MINOR features, PATCH fixes.

## 6. Undo safely

| Situation | Command |
|---|---|
| Unstage a file | `git restore --staged f` |
| Discard local edits to a file (destructive) | `git restore f` |
| Fix the last commit message / add a forgotten file (not pushed) | `git commit --amend` |
| Undo a pushed commit | `git revert <sha>` |
| Move last commit back to staging (not pushed) | `git reset --soft HEAD~1` |
| Find "lost" commits | `git reflog` then `git switch -c rescue <sha>` |
| Stash work | `git stash push -m msg`, `git stash pop` |
| Wrong branch | `git stash`, `git switch right`, `git stash pop` |

## 7. Checklist before saying "merged"

- [ ] Branch rebased on the latest main, tests green locally and in CI on every OS the project builds for.
- [ ] Version/CHANGELOG updated if the project releases.
- [ ] PR merged with the agreed method, branch deleted, local main updated.
- [ ] Give the user the PR URL, merge commit sha and release link.
