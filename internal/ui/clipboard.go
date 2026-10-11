package ui

// Ctrl+V: most terminals paste on Ctrl+Shift+V (that arrives as a bracketed paste
// and is handled there), but a raw Ctrl+V reaches the program. Blaze then reads the
// system clipboard itself: wl-paste on Wayland (Omarchy, Hyprland, sway, GNOME),
// xclip or xsel on X11, pbpaste (and osascript for pictures) on macOS, PowerShell
// Get-Clipboard on Windows. A picture on the clipboard is saved as a PNG and
// attached; with no clipboard tool, a one-line hint says what to install.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Clip is what the clipboard held.
type Clip struct {
	Text  string
	Image []byte // PNG bytes when the clipboard held a picture
}

// ErrNoClipboard means no clipboard tool was found; Hint says what to install.
type ErrNoClipboard struct{ Hint string }

func (e ErrNoClipboard) Error() string { return e.Hint }

// clipboard hooks (tests replace them).
var (
	clipGOOS     = runtime.GOOS
	clipEnv      = os.Getenv
	clipLookPath = exec.LookPath
	clipRun      = func(name string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		hideConsole(cmd)
		return cmd.Output()
	}
)

func has(name string) bool { _, err := clipLookPath(name); return err == nil }

// ReadClipboard reads text, or a picture, from the system clipboard.
func ReadClipboard() (Clip, error) {
	switch clipGOOS {
	case "darwin":
		return clipDarwin()
	case "windows":
		return clipWindows()
	}
	return clipUnix()
}

func clipUnix() (Clip, error) {
	wayland := clipEnv("WAYLAND_DISPLAY") != ""
	if wayland && has("wl-paste") {
		if types, err := clipRun("wl-paste", "--list-types"); err == nil && bytes.Contains(types, []byte("image/png")) {
			if b, err := clipRun("wl-paste", "--no-newline", "--type", "image/png"); err == nil && len(b) > 0 {
				return Clip{Image: b}, nil
			}
		}
		b, err := clipRun("wl-paste", "--no-newline")
		if err != nil {
			return Clip{}, emptyOr(err)
		}
		return Clip{Text: string(b)}, nil
	}
	if has("xclip") && (clipEnv("DISPLAY") != "" || !wayland) {
		if t, err := clipRun("xclip", "-selection", "clipboard", "-t", "TARGETS", "-o"); err == nil && bytes.Contains(t, []byte("image/png")) {
			if b, err := clipRun("xclip", "-selection", "clipboard", "-t", "image/png", "-o"); err == nil && len(b) > 0 {
				return Clip{Image: b}, nil
			}
		}
		b, err := clipRun("xclip", "-selection", "clipboard", "-o")
		if err != nil {
			return Clip{}, emptyOr(err)
		}
		return Clip{Text: string(b)}, nil
	}
	if has("xsel") {
		b, err := clipRun("xsel", "--clipboard", "--output")
		if err != nil {
			return Clip{}, emptyOr(err)
		}
		return Clip{Text: string(b)}, nil
	}
	hint := "Ctrl+V needs a clipboard tool: install xclip (X11), or paste with Ctrl+Shift+V"
	if wayland {
		hint = "Ctrl+V needs wl-clipboard (wl-paste): install it, or paste with Ctrl+Shift+V"
	}
	return Clip{}, ErrNoClipboard{hint}
}

var pngData = regexp.MustCompile(`«data PNGf([0-9A-Fa-f]+)»`)

func clipDarwin() (Clip, error) {
	if has("osascript") {
		if out, err := clipRun("osascript", "-e", "the clipboard as «class PNGf»"); err == nil {
			if m := pngData.FindSubmatch(out); m != nil {
				if b, err := hex.DecodeString(string(m[1])); err == nil && len(b) > 0 {
					return Clip{Image: b}, nil
				}
			}
		}
	}
	if !has("pbpaste") {
		return Clip{}, ErrNoClipboard{"Ctrl+V couldn't find pbpaste; paste with Cmd+V"}
	}
	b, err := clipRun("pbpaste")
	if err != nil {
		return Clip{}, emptyOr(err)
	}
	return Clip{Text: string(b)}, nil
}

const psImage = `Add-Type -AssemblyName System.Windows.Forms; $i=[Windows.Forms.Clipboard]::GetImage(); if ($i) { $m=New-Object IO.MemoryStream; $i.Save($m,[Drawing.Imaging.ImageFormat]::Png); [Convert]::ToBase64String($m.ToArray()) }`

func clipWindows() (Clip, error) {
	ps := "powershell"
	if !has(ps) {
		if !has("pwsh") {
			return Clip{}, ErrNoClipboard{"Ctrl+V couldn't reach the Windows clipboard (no PowerShell); paste with Ctrl+Shift+V or right-click"}
		}
		ps = "pwsh"
	}
	if out, err := clipRun(ps, "-NoProfile", "-STA", "-Command", psImage); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) > 0 {
				return Clip{Image: b}, nil
			}
		}
	}
	b, err := clipRun(ps, "-NoProfile", "-Command", "Get-Clipboard -Raw")
	if err != nil {
		return Clip{}, emptyOr(err)
	}
	return Clip{Text: strings.TrimRight(string(b), "\r\n")}, nil
}

// emptyOr turns "the clipboard is empty" exits into an empty clip.
func emptyOr(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return nil
	}
	return err
}

// SaveClipImage writes a pasted picture into dir and returns its path.
func SaveClipImage(dir string, png []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(dir, "paste-"+time.Now().Format("20060102-150405.000")+".png")
	return p, os.WriteFile(p, png, 0o600)
}

// normalizePaste is pasted text as one block: CRLF and CR become LF, other control
// characters (escape sequences in particular) are dropped, a trailing newline goes.
func normalizePaste(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	var b strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\t' || r >= 32 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
