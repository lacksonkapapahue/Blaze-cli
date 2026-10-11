# Blaze CLI

Blaze, the Android assistant, in your PC's terminal. One file, nothing to install first.
It detects your PC on first run, sets itself up, and works on Windows, macOS and Linux.

## Install

**Windows (PowerShell)**
```powershell
irm https://github.com/lacksonkapapahue/Blaze-cli/releases/latest/download/install.ps1 | iex
```

**macOS / Linux**
```sh
curl -fsSL https://github.com/lacksonkapapahue/Blaze-cli/releases/latest/download/install.sh | sh
```

While the repo is private the one-liners need a token: set `GITHUB_TOKEN` (repo read) first, or download
`blaze-<os>-<arch>` from the latest release and run `blaze setup`.

## Use

```
blaze                          chat in the current folder
blaze "fix the failing test"   one task, then exit (stdin is attached when piped)
blaze setup                    detect this PC, add free models, install missing tools
blaze doctor                   check models, shell, tools, scheduler
blaze pc                       what Blaze detected about this PC
blaze schedule list | remove NAME
blaze update                   download the newest release and replace this binary
blaze login [email|google|github|--signup]   sign in to your Blaze account
blaze whoami · blaze logout    who's signed in · sign out (here and in Blaze Desktop)
blaze connect [app] [--manual] list connectors, or connect google / github / notion / slack / spotify
blaze disconnect APP
blaze mcp list                 MCP servers: do they start, how many tools
blaze mcp add NAME -- CMD ARGS…           add a local (stdio) MCP server (--env K=V before --)
blaze mcp add NAME --url URL [--header K=V]   add a remote (Streamable HTTP) MCP server
blaze mcp remove NAME · blaze mcp enable|disable NAME
blaze skills [NAME]            skill library: list, show one, or on|off|delete|restore NAME
blaze learn [on|off]           learn memories and skills automatically (shared with Desktop)
```
`blaze update` (or `/update` in chat) uses `GITHUB_TOKEN`, or your `gh` login, while the repo is private.
Blaze checks for a new version at most once a day and mentions it under the banner (`BLAZE_NO_UPDATE_CHECK=1` turns that off).
Flags: `--autopilot` (or `--yolo`), `--plan`, `--model provider/model`.
In chat: `/clear /rewind /undo /model /plan /build /auto /autopilot /agents /login /whoami /logout /connect /mcp /notify /skills /learn /jobs /schedule /pc /doctor /setup /update /exit`. Ctrl-C stops the running task; Ctrl+O shows the last steps in full.

### Modes (same as Blaze Desktop)
| Mode | What it does |
|---|---|
| **Build** · work mode | every tool (default) |
| **Plan** · work mode | read and scout only: the tool list is filtered and anything that changes the PC is refused; ends with a plan you approve (into Auto or Autopilot) |
| **Auto** · approval | risky actions (outside-project writes, deletes, risky commands, installs, connector sends) ask first (default) |
| **Autopilot** · approval | nothing asks; a hard blocklist (wiping disks, the system or your home folder) stays blocked; questions get "decide yourself" automatically; bounded to 150 steps / 45 minutes per turn |

**Shift+Tab** cycles Build·Auto → Build·Autopilot → Plan (terminals can't tell Ctrl+Tab from Tab, so one key walks all three).
The choice is kept in `~/.blaze/cli.json`.

### Background agents
Ask for long work and Blaze hands it to a background agent (`dispatch_agent`) so you keep chatting. Up to 3 run at once
(1 on a 2-core PC); the rest queue. When one finishes, the main agent wakes up and tells you what it found; if the terminal
isn't focused you get a desktop notification. An agent's approvals and questions wait for you at the prompt (it never draws
over your typing). Agents are saved in `~/.blaze/cli/agents/`; if Blaze closes mid-run they come back as *interrupted*
(`/agents resume NAME`), and after an in-chat update they resume by themselves.
`/agents` lists them · `/agents NAME` shows its steps and report · `/agents msg NAME text` steers it ·
`/agents cancel|resume|discard NAME` · `/agents new NAME task` starts one yourself.

### Specialists and an always-open prompt
The main chat stays light: it talks, plans and steers, and hands real work to agents. A big task with independent parts is
split between **specialists** (`dispatch_specialist`), the way Blaze Desktop does it: they run in parallel (sharing the agents'
cap), each Build specialist in its own git worktree (or a copy of the folder) under `~/.blaze/cli/worktrees/`, and never with
more than the lead has (Plan lead → Plan specialists; Autopilot's budgets). When one reports back the main chat reviews its
diff and applies or discards it; you can too with `/agents apply|reject NAME`.

The prompt never goes away: while Blaze works you can keep typing on the working line, and Enter queues the message for its
next turn (approvals and questions still take the keys while they are open). Handing work off ends the main turn at once.

While working Blaze says what it is doing in plain words ("Reading main.go", "Running the tests"), never commands or code. A
finished turn folds into one line, `Worked · 5 steps · 12s · ctrl+o to expand`; **Ctrl+O** at the prompt shows the exact
commands, output and diffs, and hides them again. `BLAZE_STEPS=all` also prints a plain line per finished step.

### Checkpoints and rewind (like Claude Code)
Every message you send is a checkpoint. Before Blaze writes, edits or deletes a file it keeps the file's old content (or
notes that it didn't exist), so you can go back.

- **`/rewind`** (or **Esc twice** on an empty prompt) lists your messages, newest first, with the time and how many files
  each one changed. Pick one with ↑ ↓ and Enter, then choose what to put back:
  - **Code and chat**: files go back to how they were before that message, and the chat goes back to just before it. Your
    message comes back in the prompt so you can change it and send it again.
  - **Chat only**: the conversation goes back; files stay as they are now.
  - **Code only**: files go back; the conversation stays.
- **`/undo`** rewinds the last message's code and chat in one step.
- Files Blaze created are removed; files it deleted come back.
- **Your own edits are safe.** If a file changed outside Blaze since the checkpoint (you edited it, or another program did),
  Blaze lists it and asks: keep your changes, overwrite them, or cancel.
- **In a git repo, shell commands are covered too.** At the start and end of each message Blaze takes a lightweight snapshot
  of the working tree (a git tree kept under `refs/blaze/checkpoints/…`, written with a private index), so a rewind also
  undoes what commands like formatters or code generators changed. Your index, branch, stash and files are never touched.
  Outside a git repo only Blaze's own file edits are tracked, and `/rewind` says so.
- Specialists' worktrees aren't tracked; when their changes are applied, they count as a change in that message.
- Checkpoints live in `~/.blaze/cli/checkpoints/` (one folder per chat; the same content is stored once). Files over 5 MB
  are noted but not copied. Chats untouched for 30 days are pruned. `/clear` starts fresh checkpoints; `blaze -c` and
  `/resume` pick up the folder's last ones; after `/compact` older messages can rewind code only. `BLAZE_CHECKPOINTS=off`
  turns them off.

### Account and connectors
`blaze login` signs in through Blaze's sign-in service (`https://blaze-auth.blaze-app.workers.dev`): email + password, or
Google / GitHub in the browser (PKCE S256, state, a one-time 127.0.0.1 loopback redirect). The session lives in the OS keychain
under the same entry as Blaze Desktop, so signing in on one signs in both. `blaze connect google` (GitHub, Notion, Spotify)
is one click through the same service: the Worker holds the client secrets, tokens come back to the loopback and go into the
keychain. Connected apps become tools (Gmail, Calendar, Drive, GitHub issues, Notion pages…); sending or creating things
asks first unless you choose "Always allow". `--manual` uses your own developer app instead. `BLAZE_CLOUD_URL` points at another Worker.

### MCP servers
Blaze is an MCP (Model Context Protocol) client. Servers live in `~/.blaze/mcp.json` (`$BLAZE_HOME/mcp.json`), the same
file Blaze Desktop reads, in the usual shape:

```json
{
  "mcpServers": {
    "github": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"], "env": { "GITHUB_TOKEN": "${GITHUB_TOKEN}" } },
    "docs":   { "url": "https://example.com/mcp", "headers": { "Authorization": "Bearer ${DOCS_TOKEN}" } },
    "old":    { "command": "old-server", "disabled": true }
  }
}
```

A project can add its own in `.mcp.json` or `.blaze/mcp.json` in the folder you start Blaze in; a project server with the
same name replaces the global one. `${VAR}` and `${VAR:-default}` are filled from your environment, so tokens don't have
to sit in the file. Local servers talk JSON-RPC over stdin/stdout; remote ones use Streamable HTTP (JSON or event-stream
replies, `Mcp-Session-Id` sessions). The old HTTP+SSE transport isn't supported.

Servers start in the background when a chat opens, so startup isn't slower; one that fails is mentioned once and skipped
(`/mcp` says why). Their tools reach the model as `mcp__<server>__<tool>`. In **Auto** each call asks first (Yes / Always
allow / No; "Always allow" is kept per server and tool in `~/.blaze/mcp-allow.json`), in **Autopilot** they just run, and in
**Plan** only tools that declare `readOnlyHint: true` are offered. Text answers go to the model; images and other content
are noted but not shown. `/mcp` shows each server (connected with N tools, failed and why, turned off), `/mcp reload`
restarts them after you edit the file, and `blaze doctor` checks that each one starts.

### Notifications and updates
Blaze shows native notifications (notify-send / dunstify on Linux, terminal-notifier or osascript on macOS, a PowerShell toast
on Windows) for finished background agents and when the agent calls `notify`, at most 6 an hour and never the same one twice in
10 minutes. `/notify text` sends a test. `/update` (or asking "update yourself", the `update_blaze` tool) downloads the build for
this PC, checks it starts, swaps the binary atomically (one rename; on Windows the old exe is moved aside and restored if the
swap fails) and restarts Blaze into the same chat.

### Skills and learning (same as Blaze Desktop)
Blaze ships the desktop's skill library (19 skills: code review, debugging, deep research, document and email writing,
git/PR workflow, slide decks, landing pages, web design, writing tests and more). They are embedded in the binary and
seeded into `~/.blaze/skills` on start; seeding is idempotent, a newer pack upgrades untouched skills, and a skill you
edited, deleted or wrote yourself is never overwritten (`~/.blaze/skills/.bundled.json` tracks the pack). Only skills that
match the conversation go into the prompt; the agent opens one with `load_skill`. `/skills` lists them, `/skills NAME` shows
one, `/skills on|off|delete|restore NAME` manages them; restore brings back the bundled version.

Blaze also learns as it goes: it saves durable facts (`remember`, replacing an outdated one instead of adding a second,
`forget` on request, `recall` to search), and turns repeated or taught workflows into skills (`write_skill`). After an
interactive turn a cheap local check decides whether a short reflection call is worth it (at most once a minute); changes
show as a `◆ Remembered · …` / `◆ New skill · …` line. `/learn off` (or `blaze learn off`, or Blaze Desktop › Settings ›
Behaviour › Learning) turns automatic learning off; Blaze then saves only when you ask. `BLAZE_LEARN=off|on` overrides it
for one shell. The library is copied from Blaze Desktop with `scripts/sync-skills.sh [desktop-checkout | branch]`.

### Harness (borrowed from Claude Code)
| | |
|---|---|
| **Shift+Tab** | cycle modes: Build·Auto → Build·Autopilot → **Plan** (look and propose, nothing changes; then approve the plan to run it) |
| **Checklist** | for multi-step work Blaze keeps a live plan card (■ done · ▸ doing · □ to do) |
| **Diffs** | every file write or edit shows a coloured diff with +/- counts |
| `@path` | attach a file or folder to your message (Tab completes paths) |
| `!command` | run a command yourself; Blaze sees the output |
| `#note` | save a note to Blaze's memory |
| `BLAZE.md` | project instructions loaded every session (`AGENTS.md` / `CLAUDE.md` also work; `~/.blaze/BLAZE.md` is global). `/init` writes one |
| `/clear` | new conversation on a freshly cleared screen (scrollback too) · `/compact` summarise the chat to free context · `/resume` or `blaze -c` continues this folder's last chat |
| `/rewind` · Esc Esc | go back to before a message: restore code, chat or both · `/undo` rewinds the last one |

### Keys at the prompt
| Key | Does |
|---|---|
| ← → · Home End · Ctrl-A Ctrl-E | move the cursor |
| Ctrl-← Ctrl-→ · Alt-B Alt-F | jump by word |
| ↑ ↓ · Ctrl-P Ctrl-N | history (kept in `~/.blaze/history`) |
| Tab · → at the end | complete a `/command`, or accept the dim suggestion |
| Backspace · Delete · Ctrl-W · Ctrl-U · Ctrl-K · Ctrl-Y | edit, delete word, cut to start / end, paste back |
| Ctrl-L | clear the screen |
| Ctrl-C | clear the line; on an empty line, twice to quit · Ctrl-D quits |
| Esc Esc | on an empty line: `/rewind` |

Pasting multi-line text keeps the newlines (shown as ↵) and doesn't send until you press Enter.
Approval prompts and the setup picker use ← → / ↑ ↓, Space and Enter.

Motion (intro, status glint, step marks) turns off with `BLAZE_NO_ANIM=1`, `NO_COLOR`, or when output is piped.

## What it detects
OS and version, CPU, RAM, GPU (NVIDIA, AMD, Intel, Apple), free disk, your shell (PowerShell, bash, zsh…),
package managers (winget, choco, scoop, brew, apt, dnf, pacman, zypper, apk) and tools (git, python, node,
go, java, docker, cargo, gcc, ollama, gh…). It all goes into Blaze's prompt so it never asks.

## Models (free)
Same free chain as the app: Gemini, Groq, OpenRouter, Mistral, NVIDIA NIM (plus DeepSeek, Kimi, OpenAI, Claude,
xAI, local Ollama). Keys live in the OS keychain (Credential Manager / Keychain / Secret Service); env vars
like `GEMINI_API_KEY` also work and set Blaze up with no questions. If no key works, a keyless model answers.

## Tools
`run_shell` (cwd sticks, right syntax per OS, background jobs), `job_log/job_input/job_stop`, `file_read/write/edit/list/search/delete`,
`run_code` (Python venv / Node, installs packages), `git`, `install_tool` (package manager, asks first),
`web_search`, `fetch_page`, `http_request`, `download_file`, `open_url`, `system_info`, `schedule`
(Task Scheduler / cron), `remember/forget/recall`, `load_skill/write_skill` (SKILL.md, same format as the app).

Risky commands (sudo, rm -rf, format, registry edits, force-push, reset --hard), file writes outside the
project folder, installs and deletes ask first.

## Files
`~/.blaze/` (or `BLAZE_HOME`): `config.json`, `blaze.db` (memories + skills index, shared with Blaze Desktop), `skills/` (+ `.bundled.json`), `desktop.json` (shared settings such as `learn.off`), `learn-workflows.json`, `schedule.json`, `logs/`, `venv/`, `cli/checkpoints/` (rewind).

## Memory (shared with Blaze Desktop)
Since v0.6.0 memories live in `blaze.db`, the same SQLite file Blaze Desktop (v0.5.1+) uses, so a fact saved in
the terminal shows up in Desktop straight away and the other way round. Memory ids (`#12`) are the same in both.

| OS | blaze.db |
|---|---|
| Linux (Arch etc.) | `~/.blaze/blaze.db` (`/home/<you>/.blaze/blaze.db`) |
| macOS | `~/.blaze/blaze.db` (`/Users/<you>/.blaze/blaze.db`) |
| Windows | `%USERPROFILE%\.blaze\blaze.db` (`C:\Users\<you>\.blaze\blaze.db`) |

`BLAZE_HOME` moves it (and everything else) for both apps.

- **First run:** an existing `memory.md` (and its `memory.meta.json`) is imported once, duplicates skipped, and
  kept as `memory.md.migrated` (`memory.md.migrated-<time>` if a backup is already there). The CLI never writes
  `memory.md` again; if one reappears (an older Blaze, a hand edit) it is imported the same way on the next start.
- **Skills** stay folders in `~/.blaze/skills/<name>/SKILL.md` (shared with Desktop and the Android app); bundled
  skills are still seeded there without overwriting your edits, and `blaze.db` keeps the searchable index both apps use.
- **Both open at once** is fine: WAL journal, a 5 s busy timeout, short write transactions, and dedupe in a single
  statement so two apps saving the same fact keep one row.
- **Newer database:** if `blaze.db` was written by a newer Blaze than this CLI knows, the CLI leaves it untouched,
  runs without memory, and `blaze doctor` says to update. It never downgrades or rewrites the schema.
- The SQLite driver is `modernc.org/sqlite` (pure Go, no cgo), so the six cross-builds are unchanged.
  `internal/store` is a vendor copy of Desktop's `internal/store`; see the sync note at the top of `store.go`.

## Build
Go 1.26+. `go build .` CI cross-builds six targets on every push and publishes them as a GitHub release.
