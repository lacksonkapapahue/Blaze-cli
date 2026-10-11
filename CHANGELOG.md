# Changelog

## v0.6.1 — Slash menu like Blaze Desktop

- **Type `/` and the commands pop up above the input box**, each with a one-line description, the way Blaze Desktop's
  composer menu works. The list filters as you type (names that start with what you typed first, then names that
  contain it, then descriptions from three letters on). ↑ ↓ move, Enter picks (and runs commands that need nothing
  more), Tab fills the line in, Esc closes it. The menu keeps its height while you filter so the box doesn't jump.
- **`/skills ` opens your skill library in the same menu** and narrows to matching skills with every key. Picking a
  skill starts a message that uses it (`Use the <name> skill: `), like Desktop. `list`, `on`, `off`, `delete` and
  `restore` are in the menu too, and after `/skills off ` (etc.) it shows only the skills that action applies to.
- A line recalled from history with ↑ doesn't pop the menu, so ↑ ↓ keep walking history.

## v0.6.0 — Shared SQLite memory with Blaze Desktop

- **Memories now live in `~/.blaze/blaze.db`**, the SQLite database Blaze Desktop moved to in v0.5.1, instead of
  `memory.md`. `remember`, `recall`, `forget`, `#note`, end-of-turn learning and the prompt all read and write it, so the
  CLI and Desktop see each other's facts at once and share memory ids. The prompt gets pinned memories plus the ones
  that match the conversation (full-text, BM25) instead of every fact; `recall` searches the rest.
- **One-time import:** an existing `memory.md` (+ `memory.meta.json`) is imported on first run without duplicates and
  kept as `memory.md.migrated`; the CLI stops writing `memory.md`.
- **Skills index:** the skills folder stays the source of truth and bundled-skill seeding is unchanged (idempotent,
  never overwrites your edits); the CLI keeps the shared `skills` index in `blaze.db` up to date and uses its
  full-text search to pick the skills for a conversation.
- **Safe with Desktop open:** WAL, `busy_timeout` 5 s, `BEGIN IMMEDIATE` transactions, single-statement dedupe.
  Migrations re-check the schema version under the write lock. A database from a newer Blaze (or one missing Blaze's
  tables) is refused and left untouched; `blaze doctor` shows the database path, memory count and schema version.
- `internal/store` is a vendor copy of Desktop's store (schema v1, `modernc.org/sqlite`, pure Go, no cgo); a test
  pins the schema to Desktop's. The CLI never rewrites Desktop's tools index.
- Tests: import idempotency and backups, CLI writes read through Desktop's queries (and back), four processes writing
  at once on a fresh file, newer/broken schema refused without changing a byte.

## v0.5.0 — Skill library and proactive learning

Brings Blaze Desktop v0.7.0's skill library and memory/skill learning to the terminal.

- **Bundled skill library.** The desktop's 19 skills (SKILL.md + `skills/index.json`, with their assets) are embedded in the binary and seeded into `~/.blaze/skills`, the folder Blaze Desktop shares. Seeding is idempotent: a newer pack upgrades skills you haven't touched, an older binary never downgrades, and skills you edited, deleted or wrote yourself are never overwritten (`.bundled.json` manifest, sha256 over LF-normalised SKILL.md). Only skills that match the conversation go into the prompt; `load_skill` opens one.
- **`/skills` and `blaze skills`:** list (state, version, update available), show one, `on|off|delete|restore NAME`.
- **Proactive memory & skills.** Prompt rules tell Blaze to save durable facts as they appear, replace outdated ones (`remember(fact, replaces="#id")`), forget on request, `recall` saved facts, and turn repeated or taught workflows into skills. After an interactive turn a local check (no model call) decides whether a short reflection call is worth making (at most once a minute) and it saves, updates or creates from the answer. Changes print as `◆ Remembered · …`, `◆ Memory updated · …`, `◆ Forgot · …`, `◆ New skill · …`.
- **Learning setting:** `/learn [on|off]` and `blaze learn [on|off]` write the same `desktop.json` `learn.off` the desktop's Settings › Behaviour › Learning uses (other settings in that file are kept). Off means Blaze saves only when asked. `BLAZE_LEARN=off|on` overrides it for one shell.
- Plan mode allows `load_skill` and `recall` (read-only).
- `scripts/sync-skills.sh [desktop-checkout | branch]` resyncs the pack from Blaze Desktop (main, falling back to `skills-library`).

## v0.4.0 — Desktop parity

Brings Blaze Desktop v0.5.0's agent features to the terminal.

- **Background agents.** The main agent can `dispatch_agent`, `message_agent`, `cancel_agent` and `list_agents`. Up to 3 run at once (queue beyond that), each with its own shell and history. A finished agent wakes the main agent (one result per turn) and sends a desktop notification when the terminal isn't focused. Sub-agent approvals and `ask_user` questions wait at the prompt instead of drawing over it. `/agents` lists, shows, steers, cancels, resumes, discards or starts agents. Agents persist in `~/.blaze/cli/agents/`; after a restart they show as interrupted, and after an in-chat update they resume automatically.
- **Plan / Build and Auto / Autopilot**, same semantics as the desktop. Plan filters the tool list to read + scout tools and refuses the rest (git checked per subcommand). Autopilot approves everything except a catastrophic blocklist, auto-answers questions ("the user is away, decide pragmatically"), and runs up to 150 steps / 45 minutes. Shift+Tab cycles Build·Auto → Build·Autopilot → Plan; `/plan /build /auto /autopilot`; `--plan`, `--autopilot` (`--yolo` now means Autopilot). Modes persist in `~/.blaze/cli.json`.
- **Blaze accounts:** `blaze login` (email, Google, GitHub, `--signup`), `blaze logout`, `blaze whoami`, through the blaze-auth Worker with state + PKCE S256 + 127.0.0.1 loopback. Same keychain entry and `~/.blaze/desktop/account.json` as Blaze Desktop.
- **Connectors:** `blaze connect [app]` / `blaze disconnect app` and `/connect` — one-click Google, GitHub, Notion, Spotify via the Worker (`/oauth/:provider/start`, `/oauth/redeem`, `/oauth/:provider/refresh`), or `--manual` with your own app; connector tools with approvals ("Always allow"). Shares connector state with the desktop.
- **`/update` + `update_blaze` tool:** atomic binary swap (rollback on Windows), then Blaze restarts itself into the same chat (`-c`).
- **Native notifications:** `notify` tool and `/notify`, reusing the desktop's probed method when present; rate limited.
- `@file` / `@folder` attachments as before (now covered by tests).
- CI now runs on pull requests too, checks formatting and runs `go test`; releases still publish only from `main`.

Not yet: screen vision, voice input (SQLite memory arrived in v0.6.0).

## v0.3.0
Site look (boxed welcome, particles, ember spinner, boxed input) and the Claude Code-style harness: plan/auto modes, todo checklist, diffs, `@files`, `!` shell, `#` memory, BLAZE.md, `/init`, `/compact`, `/resume`, `-c`.

## v0.2.1
`blaze update` / `/update` self-update with a progress bar; daily new-version hint.

## v0.2.0
Real line editor, arrow-key pickers, animated intro, step marks with timing.

## v0.1.0
Blaze CLI: PC detection, setup wizard, free model router, tools, scheduler, doctor.
