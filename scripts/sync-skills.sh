#!/bin/sh
# Copy the canonical skill pack from Blaze Desktop into this repo's skills/ folder
# (embedded into the CLI binary).
#
# Usage: scripts/sync-skills.sh [path-to-Blaze-dekstop | branch]
#   path    use a local checkout of the desktop repo
#   branch  clone that branch of lacksonkapapahue/Blaze-dekstop (default: main,
#           falling back to skills-library while the pack is not on main yet)
set -eu
here=$(cd "$(dirname "$0")/.." && pwd)
arg=${1:-}
repo=https://github.com/lacksonkapapahue/Blaze-dekstop
if [ -n "$arg" ] && [ -d "$arg" ]; then
  src=$arg
else
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  for ref in ${arg:-main skills-library}; do
    rm -rf "$tmp/desktop"
    if git clone -q --depth 1 -b "$ref" "$repo" "$tmp/desktop" 2>/dev/null && [ -f "$tmp/desktop/skills/index.json" ]; then
      src="$tmp/desktop"; echo "using desktop branch $ref"; break
    fi
  done
  src=${src:-}
fi
[ -n "$src" ] && [ -f "$src/skills/index.json" ] || { echo "no skills/index.json in ${src:-$repo ${arg:-main/skills-library}}" >&2; exit 1; }
rm -rf "$here/skills"
cp -R "$src/skills" "$here/skills"
( cd "$here" && go test ./internal/skillpack/ >/dev/null ) && echo "skills synced from $src ($(ls -d "$here"/skills/*/ | wc -l | tr -d ' ') skills)"
