// Package clip reads the system clipboard without cgo, through the tools each
// OS ships or commonly has: osascript / pngpaste / pbpaste on macOS, wl-paste,
// xclip or xsel on Linux, and PowerShell on Windows.
//
// Image reads a clipboard image into a PNG file; Text reads clipboard text.
package clip

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ErrNoImage means the clipboard holds no image (it may hold text).
var ErrNoImage = errors.New("no image on the clipboard")

// ErrEmpty means the clipboard holds no text.
var ErrEmpty = errors.New("the clipboard is empty")

// NoToolError means no clipboard tool was found; Hint says what to install.
type NoToolError struct{ Hint string }

func (e *NoToolError) Error() string { return e.Hint }

// Timeout bounds each clipboard command. PowerShell starts slowly, so Windows
// gets WinTimeout instead.
var (
	Timeout    = 1500 * time.Millisecond
	WinTimeout = 4 * time.Second
)

// hooks (tests fake them)
var (
	goos     = runtime.GOOS
	lookPath = exec.LookPath
	getenv   = os.Getenv
	run      = runCmd
	tempDir  = os.TempDir
)

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

func runCmd(ctx context.Context, name string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, name, args...)
	hideCmd(c)
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	err := c.Run()
	if ctx.Err() != nil {
		return nil, errors.New("the clipboard took too long to answer")
	}
	if err != nil {
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return out.Bytes(), errors.New(msg)
		}
		return out.Bytes(), err
	}
	return out.Bytes(), nil
}

// hideCmd keeps a console window from flashing on Windows (proc_windows.go).
var hideCmd = func(*exec.Cmd) {}

func timeout() time.Duration {
	if goos == "windows" {
		return WinTimeout
	}
	return Timeout
}

func has(name string) bool { _, err := lookPath(name); return err == nil }

// tempPNG is a fresh path for a clipboard image. Older clipboard images Blaze
// left in the temp folder (over a day old) are tidied away.
func tempPNG() string {
	dir := tempDir()
	if old, _ := filepath.Glob(filepath.Join(dir, "blaze-clip-*.png")); len(old) > 0 {
		for _, p := range old {
			if st, err := os.Stat(p); err == nil && time.Since(st.ModTime()) > 24*time.Hour {
				_ = os.Remove(p)
			}
		}
	}
	return filepath.Join(dir, fmt.Sprintf("blaze-clip-%d.png", time.Now().UnixNano()))
}

// Image saves the clipboard's image as a PNG file and returns its path. It
// returns ErrNoImage when there is none, or a *NoToolError when no tool that
// can read it is installed.
func Image(ctx context.Context) (string, error) {
	path := tempPNG()
	err := imageTo(ctx, path)
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	b, rerr := os.ReadFile(path)
	if rerr != nil || !bytes.HasPrefix(b, pngMagic) {
		_ = os.Remove(path)
		return "", ErrNoImage
	}
	return path, nil
}

func imageTo(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout())
	defer cancel()
	switch goos {
	case "darwin":
		return darwinImage(ctx, path)
	case "windows":
		return windowsImage(ctx, path)
	default:
		return unixImage(ctx, path)
	}
}

// writeOut saves a tool's stdout as the image when it is a PNG.
func writeOut(out []byte, err error, path string) error {
	if err != nil || !bytes.HasPrefix(out, pngMagic) {
		return ErrNoImage
	}
	return os.WriteFile(path, out, 0o600)
}

func darwinImage(ctx context.Context, path string) error {
	if has("osascript") {
		script := fmt.Sprintf(`try
	set d to (the clipboard as «class PNGf»)
on error
	return "none"
end try
set f to open for access (POSIX file %q) with write permission
set eof f to 0
write d to f
close access f
return "ok"`, path)
		out, err := run(ctx, "osascript", "-e", script)
		if err == nil && strings.TrimSpace(string(out)) == "ok" {
			return nil
		}
	}
	if has("pngpaste") {
		if _, err := run(ctx, "pngpaste", path); err == nil {
			return nil
		}
		return ErrNoImage
	}
	if !has("osascript") {
		return &NoToolError{"Can't read the clipboard: osascript is missing (brew install pngpaste also works)"}
	}
	return ErrNoImage
}

func unixImage(ctx context.Context, path string) error {
	tried := false
	wl := func() bool {
		tried = true
		out, err := run(ctx, "wl-paste", "--type", "image/png")
		return writeOut(out, err, path) == nil
	}
	if getenv("WAYLAND_DISPLAY") != "" && has("wl-paste") && wl() {
		return nil
	}
	if has("xclip") {
		tried = true
		out, err := run(ctx, "xclip", "-selection", "clipboard", "-t", "image/png", "-o")
		if writeOut(out, err, path) == nil {
			return nil
		}
	}
	if !tried && has("wl-paste") && wl() { // Wayland without WAYLAND_DISPLAY exported (rare)
		return nil
	}
	if !tried {
		return &NoToolError{unixHint()}
	}
	return ErrNoImage
}

func unixHint() string {
	if getenv("WAYLAND_DISPLAY") != "" {
		return "Can't read the clipboard: install wl-clipboard (wl-paste)"
	}
	return "Can't read the clipboard: install xclip (or wl-clipboard on Wayland)"
}

func powershell() string {
	for _, n := range []string{"powershell.exe", "powershell", "pwsh.exe", "pwsh"} {
		if has(n) {
			return n
		}
	}
	return ""
}

func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func windowsImage(ctx context.Context, path string) error {
	ps := powershell()
	if ps == "" {
		return &NoToolError{"Can't read the clipboard: PowerShell is missing"}
	}
	script := `Add-Type -AssemblyName System.Windows.Forms; Add-Type -AssemblyName System.Drawing; ` +
		`$i = [Windows.Forms.Clipboard]::GetImage(); if ($i -eq $null) { exit 3 }; ` +
		`$i.Save(` + psQuote(path) + `, [System.Drawing.Imaging.ImageFormat]::Png); exit 0`
	if _, err := run(ctx, ps, "-NoProfile", "-NonInteractive", "-STA", "-Command", script); err != nil {
		return ErrNoImage
	}
	return nil
}

// Text returns the clipboard's text (line ends as \n). It returns ErrEmpty when
// there is none, or a *NoToolError when no tool that can read it is installed.
func Text(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout())
	defer cancel()
	var cands [][]string
	switch goos {
	case "darwin":
		cands = [][]string{{"pbpaste"}}
	case "windows":
		if ps := powershell(); ps != "" {
			cands = [][]string{{ps, "-NoProfile", "-NonInteractive", "-Command", "Get-Clipboard -Raw"}}
		}
	default:
		if getenv("WAYLAND_DISPLAY") != "" {
			cands = append(cands, []string{"wl-paste", "--no-newline", "--type", "text/plain"})
		}
		cands = append(cands, []string{"xclip", "-selection", "clipboard", "-o"},
			[]string{"xsel", "--clipboard", "--output"}, []string{"wl-paste", "--no-newline"})
	}
	tried := false
	for _, c := range cands {
		if !has(c[0]) {
			continue
		}
		tried = true
		out, err := run(ctx, c[0], c[1:]...)
		if err != nil {
			continue
		}
		s := strings.ReplaceAll(strings.ReplaceAll(string(out), "\r\n", "\n"), "\r", "\n")
		if s == "" {
			return "", ErrEmpty
		}
		return s, nil
	}
	if !tried {
		switch goos {
		case "darwin":
			return "", &NoToolError{"Can't read the clipboard: pbpaste is missing"}
		case "windows":
			return "", &NoToolError{"Can't read the clipboard: PowerShell is missing"}
		}
		return "", &NoToolError{strings.Replace(unixHint(), "xclip", "xclip or xsel", 1)}
	}
	return "", ErrEmpty
}
