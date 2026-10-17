# Changelog

## v0.11.0 — You on the right, Blaze on the left

- **Chat layout.** What you send now stays in the scrollback on the right, like a chat app, and Blaze's replies stay on
  the left. Your message wraps to about 70% of the terminal, sits flush right with its text reading normally, and opens
  with an accent `‹`. The same layout is used for a message sent while Blaze works and for one queued until the step
  ends (with a dim "sent while Blaze worked" / "runs when this step is done" under it). Plain (`NO_COLOR`) or very
  narrow terminals keep the old left-hand `› text` line. New `ui.UserLines` (`internal/ui/bubble.go`) with tests.

## v0.10.1 — Safety and stability fixes

- **Shell safety in every mode.** The hard blocklist (wiping disks, the system or your home folder) now checks every
  shell command in every mode, not only when a narrow pattern matched, and catches dodges like `rm -r -f /`,
  `rm -rf -- ~`, `"$HOME"`, `${HOME}`, `%USERPROFILE%`, `find ~ -delete`, `Remove-Item C:\ -Recurse`. Auto asks first for
  recursive deletes of absolute/home/glob/variable targets, `wipefs`, `fdisk`, `parted`, `mkfs`, `dd of=`,
  `Format-Volume`, `bcdedit`, `vssadmin delete`, `shred`. One check (`internal/tools/guard.go`) serves the main agent,
  background agents and specialists. It is pattern matching, not a sandbox.
- **`run_code` is checked** like `run_shell` (shell, PowerShell, Python and Node snippets).
- **The `git` tool no longer goes through a shell.** Arguments are split and git runs directly, so
  `status && rm -rf src` is refused. Plan mode allows only read-only git (no `-c`, `-O`, `--output`, `--exec`…), and
  background agents and specialists share the same strict check. Risky git (`push --force`, `reset --hard`, `clean`,
  `branch -D`…) asks first in Auto.
- **Project MCP servers need your trust.** A folder's `.mcp.json` / `.blaze/mcp.json` servers no longer start until you
  trust the folder (a y/N prompt in chat, or `blaze mcp trust`; `blaze mcp untrust` undoes it). Trust is tied to the
  files' SHA-256, so any edit asks again. Untrusted files get no `${VAR}` expansion, a project server never replaces a
  global one with the same name, and "Always allow" is keyed by source, name and command/URL. `mcp.json` and
  `mcp-allow.json` are written atomically.
- **Scheduling can't erase your crontab.** Only "no crontab for user" counts as empty; any other `crontab -l` error
  stops without writing. Other lines are kept exactly, and a backup goes to `~/.blaze/crontab.bak`.
- **Connections stay connected through hiccups.** Only a real refusal (401, or `invalid_grant` / `invalid_client` /
  `unauthorized_client`) marks a connector Expired. 5xx, 429, timeouts, network errors and Esc no longer do. Key sync
  signs you out only on a real 401, not when offline.
- **Esc no longer crashes** the `/login` picker or a background agent's multiple-choice question.

## v0.10.0 — Blaze knows itself

- **ABOUT YOU.** Every chat starts with a short, live block: you are Blaze CLI (version), made by Luke (Lackson Kapapa,
  Blazeinchue); the three apps (Desktop, CLI, Android) and how they connect (one Blaze account syncs your model keys;
  Desktop and CLI share `~/.blaze`); Blaze Link status; the Blaze account; this PC (OS, desktop/Wayland, CPU, RAM).
- **A new rule.** Questions about Blaze, its apps or connecting them are answered from ABOUT YOU and `about_blaze`,
  never by asking which apps you are building.
- **`about_blaze` tool** (optional `section`): identity, apps, version and build, account and how to sign in, Blaze
  Link (steps, paired phones), BlazeRouter (Desktop's gateway, and whether it answers on 127.0.0.1:20128 now), slash
  commands from the real menu, the real `~/.blaze` layout, connected accounts, MCP servers from `mcp.json`, skills,
  memory count and database, and the PC. Same package as Blaze Desktop (`internal/selfknow`), same tool name as Blaze
  Android's `SelfKnowledge.kt`.

## v0.9.1 — Blaze is always available

- **No more queue.** A message sent while Blaze works gets an answer right away, like Blaze Desktop and Hark: a side
  lane reads the conversation, what the turn is doing and the background agents, and replies in a few sentences while
  the work carries on. A correction or addition goes to the work in progress, which reads it at its next step and changes
  course; "stop" stops it; separate work goes to a background agent or specialists. The exchange joins the conversation,
  and anything the turn didn't get to before it ended is picked up right after. Commands, `!` shell lines and `#` notes
  still wait for the step to finish.
- **The caret moves in the composer box:** ← → Home End, Delete, Ctrl+A/E/B/F, and Ctrl+U/W edit around it. Long lines
  scroll so the caret always shows.
- The main chat's instructions now say it can be reached mid-turn, so it keeps handing long work to agents.

## v0.9.0 — Checkpoints: rewind code, chat or both

- **Every message is a checkpoint (like Claude Code).** Before Blaze writes, edits or deletes a file it keeps the old
  content (or notes the file didn't exist). Applying a specialist's changes counts as a change in that message; the
  specialists' own worktrees aren't tracked.
- **`/rewind`, or Esc twice on an empty prompt,** lists your messages newest first (time, what you said, how many files it
  changed). Pick one with ↑ ↓ and Enter, then choose **Code and chat**, **Chat only** or **Code only**. Code goes back to how
  it was before that message: edits are undone, files Blaze created are removed, files it deleted come back ("Restoring 3
  files"). Chat goes back to just before the message and puts it back in the prompt so you can change it and send it again.
- **`/undo`** rewinds the last message's code and chat in one step.
- **Your own edits are never overwritten without asking.** If a file changed outside Blaze since the checkpoint, Blaze lists
  it and asks: keep your changes, overwrite them, or cancel.
- **In a git repo, shell commands are covered too.** Blaze takes a lightweight snapshot of the working tree at the start and
  end of each message (a git tree under `refs/blaze/checkpoints/…`, written with a private index), so a rewind also undoes
  what commands changed. Your index, branch, stash and files are never touched. Outside git only Blaze's own file edits are
  tracked, and `/rewind` says so.
- Checkpoints are kept in `~/.blaze/cli/checkpoints/` (`BLAZE_HOME` respected), one folder per chat, the same content stored
  once; files over 5 MB are noted but not copied; chats untouched for 30 days are pruned. `blaze -c` and `/resume` pick up the
  folder's last checkpoints, `/clear` starts fresh ones, and after `/compact` older messages can rewind code only.
  `BLAZE_CHECKPOINTS=off` turns them off. In /help, the `/` menu and the README.
- New `internal/checkpoint` package with tests: edit, create and delete restored; several messages; conflict detection (with
  and without git); a git snapshot leaves the index, branch, status and stash alone; shell changes rewound; big files; a
  deleted folder; pruning; chat cut back to the right message, even after old messages were trimmed.
- Built on v0.8.2: the composer box and the option menus include `/rewind` and `/undo`.

## v0.8.2 — The real chat bar stays while Blaze works, and menus for @ and every command's options

- **The composer box stays on screen during a turn.** Instead of the short "› type to queue a message" beside the working
  line, the same boxed chat bar as the prompt sits under it, with the mode glyph, a caret and a footer. Type there, and
  Enter queues the message for Blaze's next turn ("1 queued · Blaze reads it next"). Long lines scroll so the end stays in
  view. It gives way while the reply streams and comes back with the next step. Terminals without colour or narrower
  than 30 columns keep the one-line composer.
- **"@" opens a files menu**, the same pop-up as "/": the folder's folders (first) and files with their sizes, filtered as
  you type. Picking a folder lists what's inside it, and picking a file fills in `@path ` so you keep typing. Dotfiles
  show once you type a dot, and `.git` never does. It works at the prompt and in the composer box during a turn.
- **Every command with options shows them.** After a command and a space, its choices pop up the way "/" does:
  `/connect` (the apps, then `--manual`), `/disconnect`, `/login`, `/keys`, `/learn`, `/update`, `/mcp` (the actions,
  then your servers' names), `/agents` (the actions, then this chat's agents or specialists, 1-6 for `cap`). Commands
  that take options are marked `›` in the menu. The README's new "Commands and their options" table lists them all.
- Fixed: removing a key right after a key sync (same millisecond) could be undone by the next sync.
- Fixed: an MCP server's tools could be missing for a moment right after it connected (ready was signalled before its
  tools were routed), which also made a CI test flaky.
- In the composer box during a turn, "/" and "@" menus work like at the prompt: ↑ ↓ move, Tab or Enter fills in, Esc closes.

## v0.8.1 — Verified downloads and tidier releases

- **Downloads are checked.** Every release now ships `SHA256SUMS`. `install.sh`, `install.ps1` and `blaze update` refuse a
  binary whose hash doesn't match (older releases without the file still get the start-up smoke test).
- **Gemini models are picked again.** The "small model" filter matched the `mini` inside `gemini`, so Gemini models (also
  OpenRouter's free ones) were never chosen by the model picker.
- **Releases are never overwritten.** CI used to delete and re-create the release for the current VERSION on every push to
  main; now it publishes only when VERSION names a new release.
- **Two data races fixed** (found by the new race-detector run): background agents read the main chat's history to build
  their prompt while the main chat was writing it, and a local MCP server's process was waited on twice when it was
  stopped. History writes now go through a lock, and the server is waited on once.
- CI runs the tests with `-race` and runs staticcheck. First tests for `internal/llm` (streaming, tool-call assembly, error
  cleanup, model picking) and for checksum parsing. Dead code removed; README and installers no longer say the repo is private.

## v0.8.0 — MCP servers

- **Blaze can use MCP (Model Context Protocol) servers.** Add one with `blaze mcp add github -- npx -y
  @modelcontextprotocol/server-github` (a local server; `--env K=V` before `--`) or `blaze mcp add docs --url
  https://example.com/mcp --header Authorization="Bearer …"` (a remote one). They live in `~/.blaze/mcp.json` in the usual
  `{"mcpServers": {…}}` shape, the same file Blaze Desktop reads; a folder's `.mcp.json` or `.blaze/mcp.json` adds its own,
  and a project server replaces a global one with the same name. `${VAR}` in the file is filled from your environment.
- **Their tools just show up.** Servers start in the background when a chat opens, so Blaze doesn't start slower; each
  server's tools reach the model as `mcp__<server>__<tool>`. A server that won't start is mentioned once and skipped.
  While working Blaze says it in plain words ("Asking Github to list issues").
- **Same safety as everything else.** In Auto each MCP call asks first, with "Always allow" remembered per server and tool
  (`~/.blaze/mcp-allow.json`); Autopilot runs them; Plan only offers tools that say they only read (`readOnlyHint`).
  Background agents and specialists get the same tools under the same rules; their approvals wait at the prompt.
- `blaze mcp list | add | remove | enable | disable` and `/mcp` in chat (connected with N tools, failed and why, turned
  off; `/mcp reload` after editing the file). `blaze doctor` checks that each server starts.
- Under the hood: a small pure-Go client (`internal/mcp`) for stdio (newline-delimited JSON-RPC, the server in its own
  process group so Ctrl-C doesn't reach it, stopped with everything it started when Blaze exits; no console window on
  Windows) and Streamable HTTP (JSON or event-stream replies, `Mcp-Session-Id`, `MCP-Protocol-Version`). It asks for
  protocol 2025-06-18 and falls back to older versions; `tools/list` follows pages; a server that dies is restarted once.
  Text comes back to the model; images and other content are noted. The old HTTP+SSE transport isn't supported.
- Tests: a fake stdio server (the test binary itself) and an HTTP one: handshake, version fallback, pages, calls, tool
  errors, crashes, sessions, config merging and editing, and Plan/Auto/Autopilot gating through the agent.

## v0.7.2 — /clear really clears

- **`/clear` (and `/new`) now wipe the terminal and start over.** Besides starting a new conversation, Blaze erases the
  screen and the terminal's scrollback and draws the start screen again (no intro animation), so old replies, menus and
  half-drawn boxes are gone. The last turn's Ctrl+O details are dropped with them. When output isn't a terminal it just
  prints "New conversation." as before.

## v0.7.1 — No more "thinking": plain words from the first second

- **The working line never says "thinking" or "writing" any more.** While the model decides what to do, Blaze now says it in
  plain words like every other step, the way Blaze Desktop does: "Reading your request" on the first step, then what it is
  making of the last step ("Going over main.go", "Checking the test results", "Going through the search results",
  "Checking the change", "Working out the next step"). Streaming the answer shows "Writing the reply", and the end-of-turn
  learning check shows "Saving what I learned". New `steps.Thinking` with tests.
- If you still see `thinking` or no plain-words steps, you are on v0.6.x: run `blaze update` (or `/update`) to get the
  always-open prompt, specialists and plain-words progress from v0.7.0.

## v0.7.0 — Specialists, an always-open prompt and plain-words progress

- **The main chat is always reachable.** The prompt no longer disappears while Blaze works: type on the working line
  (`› your words▏`) and Enter queues the message; queued messages run right after the turn, in order, before agents'
  results (Blaze Desktop's composer behaviour). Half-typed text comes back in the prompt. Approvals and questions that open
  mid-turn take the keyboard while they are open and hand it back. Ctrl-C still stops the turn. (Linux/macOS; on Windows
  consoles keys typed meanwhile still wait for the prompt.)
- **Specialists (port of Blaze Desktop v0.6.0).** The main model works as a lead and stays light: it talks and steers, and
  hands real work to background agents or, for a task with independent parts, briefs **specialists** with
  `dispatch_specialist` / `check_specialist` / `message_specialist` / `cancel_specialist` / `apply_specialist`. They run in
  parallel under the agents' cap; a Build specialist works in its own git worktree (or folder copy) in
  `~/.blaze/cli/worktrees/` and can't write into your folder; the lead reviews its diff and applies or discards it. A
  specialist never gets more than the lead: Plan lead → Plan specialists, Autopilot's time budget, the lead's step budget.
  `internal/agents` now matches Desktop's package (specialists, isolation, result messages).
- **Handing off ends the turn at once.** A step that only briefs agents ends the main turn with one short line, so the prompt
  is yours again while they work. The lead's prompt adds Desktop's delegation and orchestration rules plus "you are the main
  chat, keep your turns short".
- **Plain words while working, details on Ctrl+O.** The working line says what Blaze is doing in Desktop's phrases
  ("Reading main.go", "Running the tests", "Searching the web for …", "Briefing a specialist") — never tool names, commands,
  JSON, code or diffs (new `internal/steps`, a port of Desktop's `steps.js`). Agents' and specialists' rows use the same
  phrases. A finished turn folds into one dim line, `Worked · 5 steps · 12s · ctrl+o to expand`; **Ctrl+O** at the prompt
  expands the last steps (exact commands, output, diffs) above the prompt and collapses them again without touching what
  you're typing. While working the terminal's IEXTEN is off, so macOS doesn't treat Ctrl+O as "discard output".
  `BLAZE_STEPS=all` prints a plain line per finished step. In /help and the footer.
- `/agents` shows specialists (mode, files to review); `/agents apply|reject NAME` reviews one yourself.
- Tests: Desktop's specialist suite (worktree/copy isolation, apply, parallel branches, conflicts, lifecycle, never above the
  lead, guard), lead → specialist → apply end to end with a fake model (one model call at the handoff, nothing reaches the
  folder before review), handoff rules, specialist permissions, the describer (tool call → phrase, never leaks commands),
  the composer queue and keyboard hand-over, and the Ctrl+O toggle.

## v0.6.3 — Keys shared with Blaze Desktop and your phone

- **One key store for Blaze CLI and Blaze Desktop** (`internal/keystore`, the same file in both repos): the OS keychain
  under service `blaze-cli` (account `<provider>`, plus `<provider>#extra` for Desktop's extra keys), or, with no
  keychain, `~/.blaze/keys.json` (0600) read fresh on every use, so two running apps never overwrite each other's keys.
  A key added in either app works in the other straight away; removing a provider removes its extra keys too.
- **One-time migration:** keys an older Blaze kept under `"keys"` in `config.json` are copied into the store (keys
  already there win). The legacy copy stays so an older Blaze on the same PC keeps working, is still read as a last
  resort, and is removed when you delete that key.
- **Keys follow your Blaze account** to Blaze on your phone and other computers: right after `blaze login`, when a key
  is saved or removed, and at start (at most hourly) while signed in. The apps encrypt the keys (AES-256-GCM) before
  upload; the service only stores the blob (see Blaze-dekstop `cloud/README.md`). Merge is per provider, newest change
  wins, and keys saved before syncing existed are merged rather than replaced. Connector tokens and the session never
  sync. Until the service's `/v1/keys` is live, keys simply stay shared on this computer.
- **`blaze keys`** lists saved keys and where they live; **`blaze keys sync`** (or `/keys sync`) syncs now.
- Tests: two apps on one store (keychain and keys.json), legacy migration, merge rules, sync between two devices
  against a fake service, deletions, idle sync writes nothing.

## v0.6.2 — No more stray ^[[O; /model is saved

- **Fixed a literal `^[[O` appearing on screen** (e.g. under "Resumed 2 messages" after `blaze -c`). That is the
  terminal's focus-out report: Blaze turns focus reporting on so it can notify you when you're looking elsewhere, but
  outside the prompt the terminal was in normal (cooked) mode and echoed the report before Blaze read it. While focus
  reporting is on, the terminal now stays in Blaze's quiet mode (no echo, no line buffering; Ctrl-C still stops a task)
  for the whole session, so reports are always read and dropped, never shown. Line prompts (`/login` email, agent
  questions) echo what you type themselves; the terminal is put back exactly as it was on exit and before a restart.
  Desktop notifications when the terminal is unfocused work as before.
- **`/model` is now saved.** `/model provider/model` makes that model the one Blaze uses from now on, also after
  `/exit` and in the banner, and keeps the rest of your chain behind it as fallback. It is stored the way Blaze
  Desktop's model picker stores it (`default` + `model_for`, and first in the chain), so both apps agree;
  `/model auto` goes back to plain chain order. `--model` stays a one-off for that run and is never saved.
- **`/model ` opens a picker** in the slash menu with your models (↑ ↓, Enter), plus `auto`. `/model` alone lists
  them with the one in use marked.
- OpenRouter ids that contain a slash work: `/model nvidia/nemotron-3.5-lightning:free` picks it on OpenRouter when
  OpenRouter is set up (a `:free` id, an id already in your chain, or an unknown prefix); `openrouter/vendor/model`
  always does.
- Saving `config.json` keeps settings this version doesn't know (Blaze Desktop's routing, daily cap, custom
  provider…) instead of dropping them.

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
