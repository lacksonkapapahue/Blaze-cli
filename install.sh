#!/bin/sh
# Blaze CLI installer for macOS and Linux.
#   curl -fsSL https://github.com/lacksonkapapahue/Blaze-cli/releases/latest/download/install.sh | sh
# Private repo? export GITHUB_TOKEN=<token with repo read> first.
set -e
REPO="lacksonkapapahue/Blaze-cli"
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in linux|darwin) ;; *) echo "Unsupported OS: $os (on Windows use install.ps1)"; exit 1 ;; esac
case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; *) echo "Unsupported CPU: $(uname -m)"; exit 1 ;; esac
asset="blaze-$os-$arch"
dir="${BLAZE_BIN:-$HOME/.local/bin}"
mkdir -p "$dir"
printf '\n  \033[38;2;107;140;255m■\033[0m  \033[1mB L A Z E\033[0m  installer\n     %s/%s → %s\n\n' "$os" "$arch" "$dir"
if [ -n "$GITHUB_TOKEN" ]; then
  id=$(curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" "https://api.github.com/repos/$REPO/releases/latest" \
    | tr ',' '\n' | grep -B3 "\"name\": *\"$asset\"" | grep '"id"' | head -1 | tr -dc '0-9')
  curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" -H "Accept: application/octet-stream" \
    "https://api.github.com/repos/$REPO/releases/assets/$id" -o "$dir/blaze"
else
  curl -fsSL "https://github.com/$REPO/releases/latest/download/$asset" -o "$dir/blaze"
fi
chmod +x "$dir/blaze"
case ":$PATH:" in *":$dir:"*) ;; *)
  for rc in "$HOME/.bashrc" "$HOME/.zshrc"; do [ -f "$rc" ] && echo "export PATH=\"$dir:\$PATH\"" >> "$rc"; done
  export PATH="$dir:$PATH"
  echo "  Added $dir to your PATH (open a new terminal to use it everywhere)." ;;
esac
echo "  Installed $("$dir/blaze" version)."
if [ -t 0 ] || [ -e /dev/tty ]; then "$dir/blaze" setup < /dev/tty; fi
