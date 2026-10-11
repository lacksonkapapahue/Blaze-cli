---
name: shell-sysadmin
description: Run and fix things on the user's computer across Windows (PowerShell), macOS and Linux including Arch - install software, manage services, disk space, processes, ports, networks, permissions, startup apps, scheduled jobs and updates - safely and with the right commands for that OS. Use for any "my PC…", "install…", "why is X slow/full/broken" or system-level task.
tags: [shell, sysadmin, windows, macos, linux, arch, powershell]
version: 1
---

# Shell and system admin (Windows, macOS, Linux, Arch)

The PC profile in your prompt already says the OS, shell and package managers - never ask.
Write commands for **that** shell. Read before you change; explain anything that needs admin
rights or deletes data and get a yes first. Prefer reversible steps and back up config files
(`cp file file.bak`) before editing them.

## Quick map

| Task | Windows (PowerShell) | macOS | Linux (Arch first, then Debian/Ubuntu, Fedora) |
|---|---|---|---|
| Install app | `winget install --id Git.Git -e` (or `scoop`, `choco`) | `brew install git` / `brew install --cask app` | `sudo pacman -S git` (AUR: `yay -S pkg`) / `sudo apt install git` / `sudo dnf install git` |
| Update all | `winget upgrade --all` | `brew update && brew upgrade`; `softwareupdate -l` | `sudo pacman -Syu` / `sudo apt update && sudo apt upgrade` |
| Find a program | `Get-Command git`, `where.exe git` | `which git` | `which git`, `pacman -Qs name` |
| Disk usage | `Get-PSDrive -PSProvider FileSystem`; big folders: `Get-ChildItem -Recurse … \| Sort Length -Desc` | `df -h`; `du -sh ~/* \| sort -h` | `df -h`; `du -sh ~/* \| sort -h`; `ncdu` |
| Processes / CPU | `Get-Process \| Sort CPU -Desc \| Select -First 10` | `top -o cpu`, Activity Monitor | `top`/`htop`, `ps aux --sort=-%cpu \| head` |
| Kill process | `Stop-Process -Name app` | `pkill app` | `pkill app` |
| What uses a port | `Get-NetTCPConnection -LocalPort 3000` | `lsof -i :3000` | `ss -ltnp \| grep 3000` |
| Services | `Get-Service x`, `Restart-Service x` (admin) | `brew services list`, `launchctl` | `systemctl status x`, `sudo systemctl restart x`, `journalctl -u x -e` |
| Network | `Test-NetConnection host -Port 443`, `ipconfig /all` | `ping`, `networksetup`, `ifconfig` | `ping`, `ip a`, `nmcli`, `resolvectl status` |
| Env var (persist) | `[Environment]::SetEnvironmentVariable("X","v","User")` | add `export X=v` to `~/.zshrc` | add to `~/.bashrc`/`~/.zshrc`, or `~/.config/environment.d/*.conf` |
| Scheduled job | Task Scheduler (`Register-ScheduledTask`) | `launchd` plist or `crontab -e` | `crontab -e` or a systemd timer |
| Logs | Event Viewer, `Get-WinEvent -LogName System -MaxEvents 50` | `log show --last 1h` | `journalctl -b -p warning` |

## Rules by OS

- **Windows**: PowerShell, not cmd syntax. Paths with spaces in quotes; `$env:USERPROFILE`. Admin needs an elevated shell: say so instead of failing silently. Execution policy errors: `Set-ExecutionPolicy -Scope CurrentUser RemoteSigned` (explain first). Prefer `winget`; reopen the terminal after PATH changes.
- **macOS**: zsh default; Homebrew under `/opt/homebrew` (Apple Silicon). Gatekeeper blocks unsigned apps: `xattr -d com.apple.quarantine App.app` only if the user trusts the source. Privacy permissions (Screen Recording, Accessibility) are granted in System Settings, not by command.
- **Linux / Arch**: never `sudo pip install`; use the distro package, `pipx`, or a venv. On Arch: full upgrades only (`pacman -Syu`, no partial `-Sy pkg`); read Arch news before big upgrades; AUR via `yay`/`paru` after reading the PKGBUILD. Omarchy/Hyprland: config in `~/.config/hypr/`, reload with `hyprctl reload`. Wayland vs X11 matters for screenshots, hotkeys and clipboard (`wl-copy` vs `xclip`).

## Troubleshooting order

1. Read the exact error; check the obvious (disk full, offline, wrong directory, typo, not installed, needs a restart).
2. Logs (table above), then versions (`app --version`), then config files.
3. Search the exact error text with the OS name and version.
4. Change one thing, retest, keep notes; undo what didn't help.

## Safety

- Show a command before running anything that deletes, formats, changes boot/partition, firewall, users or permissions recursively (`rm -rf`, `chmod -R`, `chown -R`, `dd`, `mkfs`, `diskpart`, registry edits).
- Long jobs (`pacman -Syu`, builds, servers): run in the background and poll the log.
- Never paste secrets into commands that end up in shell history; use env vars or the OS keychain.
- After changes, verify (service running, port open, app launches) and tell the user what changed and how to undo it.

## Checklist

- [ ] Commands match the user's OS and shell (from the PC profile).
- [ ] Anything destructive or needing admin was shown and approved first.
- [ ] Config files backed up before edits.
- [ ] Result verified, and the user told what changed and how to undo it.
