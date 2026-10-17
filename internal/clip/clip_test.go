package clip

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// fake sets the OS, the installed tools and what each command prints.
func fake(t *testing.T, os_ string, env map[string]string, tools map[string]func(args []string) ([]byte, error)) *[]string {
	t.Helper()
	var calls []string
	og, ol, oe, or, ot := goos, lookPath, getenv, run, tempDir
	dir := t.TempDir()
	goos = os_
	tempDir = func() string { return dir }
	lookPath = func(n string) (string, error) {
		if _, ok := tools[n]; ok {
			return "/bin/" + n, nil
		}
		return "", exec.ErrNotFound
	}
	getenv = func(k string) string { return env[k] }
	run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return tools[name](args)
	}
	t.Cleanup(func() { goos, lookPath, getenv, run, tempDir = og, ol, oe, or, ot })
	return &calls
}

var png = append([]byte("\x89PNG\r\n\x1a\n"), "rest"...)

func TestLinuxImageXclip(t *testing.T) {
	calls := fake(t, "linux", nil, map[string]func([]string) ([]byte, error){
		"xclip": func(a []string) ([]byte, error) { return png, nil },
	})
	p, err := Image(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != string(png) || !strings.Contains((*calls)[0], "-t image/png -o") {
		t.Fatalf("%q %v", b, *calls)
	}
}

func TestLinuxWaylandFirst(t *testing.T) {
	calls := fake(t, "linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, map[string]func([]string) ([]byte, error){
		"wl-paste": func(a []string) ([]byte, error) { return png, nil },
		"xclip":    func(a []string) ([]byte, error) { return nil, errors.New("no") },
	})
	if _, err := Image(context.Background()); err != nil || len(*calls) != 1 || !strings.HasPrefix((*calls)[0], "wl-paste") {
		t.Fatalf("%v %v", err, *calls)
	}
}

func TestLinuxNoImage(t *testing.T) {
	fake(t, "linux", nil, map[string]func([]string) ([]byte, error){
		"xclip": func(a []string) ([]byte, error) { return []byte("plain text"), nil },
	})
	if _, err := Image(context.Background()); !errors.Is(err, ErrNoImage) {
		t.Fatal(err)
	}
	left, _ := os.ReadDir(tempDir())
	if len(left) != 0 {
		t.Fatal("temp file left behind")
	}
}

func TestNoTools(t *testing.T) {
	fake(t, "linux", nil, nil)
	_, err := Image(context.Background())
	var nt *NoToolError
	if !errors.As(err, &nt) || !strings.Contains(nt.Hint, "xclip") {
		t.Fatal(err)
	}
	_, err = Text(context.Background())
	if !errors.As(err, &nt) || !strings.Contains(nt.Hint, "xsel") {
		t.Fatal(err)
	}
}

func TestDarwinFallsBackToPngpaste(t *testing.T) {
	calls := fake(t, "darwin", nil, map[string]func([]string) ([]byte, error){
		"osascript": func(a []string) ([]byte, error) { return []byte("none\n"), nil },
		"pngpaste": func(a []string) ([]byte, error) {
			return nil, os.WriteFile(a[0], png, 0o600)
		},
	})
	if _, err := Image(context.Background()); err != nil || len(*calls) != 2 {
		t.Fatalf("%v %v", err, *calls)
	}
}

func TestWindowsImageAndText(t *testing.T) {
	calls := fake(t, "windows", nil, map[string]func([]string) ([]byte, error){
		"powershell.exe": func(a []string) ([]byte, error) {
			cmd := a[len(a)-1]
			if strings.Contains(cmd, "GetImage") {
				return nil, errors.New("exit status 3")
			}
			return []byte("line1\r\nline2\r\n"), nil
		},
	})
	if _, err := Image(context.Background()); !errors.Is(err, ErrNoImage) {
		t.Fatal(err)
	}
	if !strings.Contains((*calls)[0], "-STA") {
		t.Fatalf("%v", *calls)
	}
	s, err := Text(context.Background())
	if err != nil || s != "line1\nline2\n" {
		t.Fatalf("%q %v", s, err)
	}
}

func TestTextFallsThrough(t *testing.T) {
	fake(t, "linux", nil, map[string]func([]string) ([]byte, error){
		"xclip": func(a []string) ([]byte, error) { return nil, errors.New("Error: target STRING not available") },
		"xsel":  func(a []string) ([]byte, error) { return []byte("hi"), nil },
	})
	if s, err := Text(context.Background()); err != nil || s != "hi" {
		t.Fatalf("%q %v", s, err)
	}
}

func TestPSQuote(t *testing.T) {
	if psQuote(`C:\a'b.png`) != `'C:\a''b.png'` {
		t.Fatal(psQuote(`C:\a'b.png`))
	}
}
