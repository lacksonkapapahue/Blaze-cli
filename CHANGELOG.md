# Changelog

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

Not yet: SQLite memory (waits for the desktop's memory-db), screen vision, voice input.

## v0.3.0
Site look (boxed welcome, particles, ember spinner, boxed input) and the Claude Code-style harness: plan/auto modes, todo checklist, diffs, `@files`, `!` shell, `#` memory, BLAZE.md, `/init`, `/compact`, `/resume`, `-c`.

## v0.2.1
`blaze update` / `/update` self-update with a progress bar; daily new-version hint.

## v0.2.0
Real line editor, arrow-key pickers, animated intro, step marks with timing.

## v0.1.0
Blaze CLI: PC detection, setup wizard, free model router, tools, scheduler, doctor.
