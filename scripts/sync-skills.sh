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
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
if [ -n "$arg" ] && [ -d "$arg" ]; then
  src=$arg
else
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

# REA is sourced separately from its official, immutable release tag.
rea_repo=https://github.com/morluto/rea.git
rea_tag=rea-agents-6.5.0
rea_commit=3b79af28e80785afce9f5998d8f03289402a1847
git clone -q --depth 1 --branch "$rea_tag" --filter=blob:none --sparse "$rea_repo" "$tmp/rea"
test "$(git -C "$tmp/rea" rev-parse HEAD)" = "$rea_commit" || {
  echo "REA tag $rea_tag no longer resolves to the pinned commit $rea_commit" >&2
  exit 1
}
git -C "$tmp/rea" sparse-checkout set .agents/skills/reverse-engineer-anything LICENSE
rea_skill="$here/skills/reverse-engineer-anything"
mkdir -p "$rea_skill"
cp -R "$tmp/rea/.agents/skills/reverse-engineer-anything/." "$rea_skill/"
cp "$tmp/rea/LICENSE" "$rea_skill/LICENSE"

# Add Blaze-specific routing metadata and consent guidance without changing the
# pinned upstream files; this transformation is repeated on every sync.
awk '
  /^description: / {
    print "description: Reverse engineer native, managed, Electron/JavaScript, packaged, firmware, and browser targets with REA. Use shipped-artifact or requested runtime evidence to explain features, compare versions, decompile code, or guide a reconstruction. Skip REA for ordinary source-code repositories."
    print "tags: [reverse engineer, decompile, how does app do X, binary, APK, Electron app, .NET assembly, ASAR, firmware, compare versions]"
    next
  }
  /^---$/ && front {
    print
    print ""
    print "> **Blaze:** Only analyse software the user owns or is authorised to examine. If REA tools are not available, ask first before running `blaze rea enable`, or use the CLI fallback: `npx -y rea-agents@6.5.0 analyze-javascript-application <abs path> --json`."
    print ""
    front = 0
    next
  }
  /^---$/ { front = 1 }
  { print }
' "$rea_skill/SKILL.md" > "$rea_skill/SKILL.md.tmp"
mv "$rea_skill/SKILL.md.tmp" "$rea_skill/SKILL.md"
cat > "$rea_skill/SOURCE.md" <<EOF
# Source

- Repository: https://github.com/morluto/rea
- Tag: \`$rea_tag\`
- Commit: \`$rea_commit\`
- Date copied: $(date -u +%F)

Do not edit the upstream skill content; re-sync with \`scripts/sync-skills.sh\`.
EOF

( cd "$here" && go test ./internal/skillpack/ >/dev/null ) && echo "skills synced from $src ($(find "$here/skills" -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d ' ') skills, including REA $rea_tag)"
