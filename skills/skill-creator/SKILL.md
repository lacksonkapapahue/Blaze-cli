---
name: skill-creator
description: How to write or improve a Blaze skill (SKILL.md) - when a workflow is worth saving, how to name and describe it so it's found, how to structure the steps, checklists and templates, and how to update it later. Use before calling write_skill, when the user says "save this as a skill" or teaches you a method, or when an existing skill turned out wrong.
tags: [skills, meta, writing, workflow]
version: 1
---

# Writing a good skill

A skill is a reusable method Blaze loads before doing a kind of task. It lives in
`~/.blaze/skills/<name>/SKILL.md` and is shared by Blaze Desktop, the CLI and the Android app.

## 1. When to create or change one

Create when:
- the user teaches a method ("here's how I deploy", "always do X then Y"), or asks to save one;
- a multi-step workflow repeats (second time you do the same kind of task with 5+ steps);
- you solved something non-obvious that will come up again (a tricky setup, a site's quirks).

Update (rewrite under the same name, after `load_skill`) when a step was wrong, a better way
was found, or the user corrected you. Don't create near-duplicates: improve the existing one.

Don't save one-off tasks, secrets (keys, passwords, tokens), or personal facts (those are memories: use `remember`).

After creating or changing a skill, tell the user in one line ("Saved this as the deploy-homeglow skill.").

## 2. Name and description (this is how it's found)

- **name**: kebab-case, specific, 2-5 words: `deploy-homeglow`, `upwork-proposal`, `arch-system-update`. Not `helper`, `my-skill`, `task1`.
- **description**: one or two sentences: *what it does* + *when to use it*, with the words a user would actually say. The skill search ranks name and description highest.
  - Good: "Deploy the HomeGlow Shopify theme to production with pnpm and the Shopify CLI. Use when the user says deploy, publish or push the store theme."
  - Bad: "Deployment stuff."
- **tags**: 3-6 lowercase topics (`deploy, shopify, pnpm`).
- **version**: an integer; write_skill bumps it on every rewrite.

## 3. Body structure

```markdown
# Title

One line: what this achieves and the finish line.

## Before you start
- Inputs needed (ask only for what can't be found), prerequisites, tools to check.

## Steps
1. Concrete action with the exact command / click path / file.
2. …each step says how to verify it worked.

## Pitfalls
- Known failure → what to do.

## Checklist
- [ ] Verifiable finish criteria.
```

Rules:
- Imperative, concise; exact commands in code blocks for the user's OS/shell; paths and URLs spelled out.
- Encode the *why* behind non-obvious steps in a few words so later edits keep the intent.
- Include decision points ("if the build fails with X, do Y") and safety gates ("ask before deleting").
- Keep it under ~150 lines; move long templates into `assets/` files in the skill folder and reference them by relative path (`assets/template.html`). Scripts in the folder can be run with run_shell.
- Generic enough to reuse, specific enough to act on: parameterise names that change ("the project's package manager") and keep fixed facts fixed ("the store is homeglow.myshopify.com").

## 4. Quality check before saving

- [ ] Would another agent with no context finish the task using only this skill?
- [ ] Description contains the trigger words; name is unique among existing skills.
- [ ] Steps verified on a real run (or clearly marked untested).
- [ ] No secrets, no personal data beyond what the workflow needs.

## 5. Bundled skills

Skills shipped with Blaze are seeded on start. Editing one is fine: Blaze never overwrites a
skill you or it changed, and the user can reset it, turn it off or delete it in Settings › Skills.
