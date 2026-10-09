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
```
Flags: `--yolo` (skip approvals; deletes still ask), `--model provider/model`.
In chat: `/clear /model /yolo /jobs /schedule /pc /doctor /setup /exit`. Ctrl-C stops the running task.

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
(Task Scheduler / cron), `remember/forget`, `use_skill/write_skill` (SKILL.md, same format as the app).

Risky commands (sudo, rm -rf, format, registry edits, force-push, reset --hard), file writes outside the
project folder, installs and deletes ask first.

## Files
`~/.blaze/` (or `BLAZE_HOME`): `config.json`, `memory.md`, `skills/`, `schedule.json`, `logs/`, `venv/`.

## Build
Go 1.23. `go build .` CI cross-builds six targets on every push and publishes them as a GitHub release.
