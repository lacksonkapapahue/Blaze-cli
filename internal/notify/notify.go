// Package notify sends native OS notifications cheaply. Blaze works out once
// (during setup or Doctor) which method works on this computer and caches it,
// with the full call template, in ~/.blaze/notify.json. Later a notification is
// one template fill: the model only supplies title, body and urgency.
//
// Two kinds of method:
//   - "native": in-process through Wails (Windows toast, macOS Notification
//     Center, Linux org.freedesktop.Notifications over D-Bus). Clicks reach Blaze.
//   - "exec": a command template (notify-send, terminal-notifier, osascript, a
//     PowerShell toast). Used when native isn't available, and by scheduled jobs,
//     which run without the app.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
)

// Method is one way to show a notification on this computer.
type Method struct {
	Kind   string   `json:"kind"`             // native | exec
	Name   string   `json:"name"`             // shown in Settings and Doctor
	Argv   []string `json:"argv,omitempty"`   // exec: argv with {title} {body} {urgency} {icon} {app}
	Stdin  string   `json:"stdin,omitempty"`  // exec: script fed on stdin, same placeholders (escaped)
	Escape string   `json:"escape,omitempty"` // how placeholders in Stdin are escaped: "ps-xml"
	Clicks bool     `json:"clicks"`           // a click opens Blaze
}

// Cache is ~/.blaze/notify.json.
type Cache struct {
	Version  int       `json:"version"`
	OS       string    `json:"os"`
	Host     string    `json:"host,omitempty"`    // ~/.blaze may be synced: the probe is per machine
	Desktop  string    `json:"desktop,omitempty"` // Linux: GNOME, Hyprland… (switching session re-probes)
	Probed   time.Time `json:"probed"`
	Primary  *Method   `json:"primary,omitempty"`  // what the app uses
	Fallback *Method   `json:"fallback,omitempty"` // an exec method (scheduled jobs, or when primary fails)
	Tried    []string  `json:"tried,omitempty"`    // what the probe found missing, for Doctor
	LastErr  string    `json:"last_error,omitempty"`
}

const CacheVersion = 2

func Path() string { return filepath.Join(config.Dir(), "notify.json") }

var mu sync.Mutex

// Here is the machine and session the cache is valid for.
func Here() (host, desktop string) {
	host, _ = os.Hostname()
	if runtime.GOOS == "linux" {
		desktop = os.Getenv("XDG_CURRENT_DESKTOP")
		if desktop == "" && os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "" {
			desktop = "Hyprland"
		}
		if desktop == "" {
			desktop = os.Getenv("DESKTOP_SESSION")
		}
	}
	return host, desktop
}

// NewCache starts an empty cache for this machine.
func NewCache(now time.Time) *Cache {
	h, d := Here()
	return &Cache{Version: CacheVersion, OS: runtime.GOOS, Host: h, Desktop: d, Probed: now}
}

// Load reads the cache; nil when there is none, or it was made for another OS,
// machine, desktop session or cache version (then Blaze probes again, once).
func Load() *Cache {
	b, err := os.ReadFile(Path())
	if err != nil {
		return nil
	}
	var c Cache
	if json.Unmarshal(b, &c) != nil || !c.Valid() {
		return nil
	}
	return &c
}

// Valid is true when the cache belongs to this machine and session.
func (c *Cache) Valid() bool {
	h, d := Here()
	return c != nil && c.Version == CacheVersion && c.OS == runtime.GOOS && c.Host == h && c.Desktop == d && c.Primary != nil
}

// Choose fills the cache from a probe: the in-process method when it works
// (clicks reach Blaze), the first command method as fallback and for scheduled
// jobs, and a note of everything that didn't work for Doctor.
func (c *Cache) Choose(native *Method, nativeErr string, execs []Method, missing []string) {
	c.Primary, c.Fallback, c.Tried = nil, nil, nil
	if native != nil {
		m := *native
		c.Primary = &m
	} else if nativeErr != "" {
		c.Tried = append(c.Tried, nativeErr)
	}
	if len(execs) > 0 {
		f := execs[0]
		c.Fallback = &f
		if c.Primary == nil {
			p := execs[0]
			c.Primary = &p
		}
	}
	c.Tried = append(c.Tried, missing...)
}

func (c *Cache) Save() {
	mu.Lock()
	defer mu.Unlock()
	_ = os.MkdirAll(config.Dir(), 0o700)
	b, _ := json.MarshalIndent(c, "", "  ")
	_ = os.WriteFile(Path(), b, 0o600)
}

// Note is one notification.
type Note struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	Urgency string `json:"urgency"` // low | normal | critical
	Kind    string `json:"kind"`    // task | job | suggest | tool | test
	Chat    string `json:"chat,omitempty"`
	Prompt  string `json:"prompt,omitempty"` // a suggestion's "help me" request
}

// Clean trims a note to what a notification can show.
func (n *Note) Clean() {
	n.Title = clip(oneLine(n.Title), 80)
	n.Body = clip(strings.TrimSpace(n.Body), 280)
	switch n.Urgency {
	case "low", "normal", "critical":
	default:
		n.Urgency = "normal"
	}
	if n.Title == "" {
		n.Title = "Blaze"
	}
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// IconPath is Blaze's icon on disk (written once by WriteIcon) for exec methods.
func IconPath() string { return filepath.Join(config.Dir(), "desktop", "blaze.png") }

func WriteIcon(png []byte) {
	if _, err := os.Stat(IconPath()); err == nil || len(png) == 0 {
		return
	}
	_ = os.MkdirAll(filepath.Dir(IconPath()), 0o700)
	_ = os.WriteFile(IconPath(), png, 0o600)
}

func have(bin string) bool { _, err := exec.LookPath(bin); return err == nil }

// ExecCandidates are the command methods for this OS, best first, that exist here.
// missing lists what was looked for and isn't installed.
func ExecCandidates() (out []Method, missing []string) {
	switch runtime.GOOS {
	case "linux":
		if have("notify-send") {
			out = append(out, Method{Kind: "exec", Name: "notify-send", Argv: []string{"notify-send", "--app-name=Blaze", "--urgency={urgency}", "--icon={icon}", "{title}", "{body}"}})
		} else {
			missing = append(missing, "notify-send (install libnotify: sudo pacman -S libnotify / sudo apt install libnotify-bin)")
		}
		if have("dunstify") {
			out = append(out, Method{Kind: "exec", Name: "dunstify", Argv: []string{"dunstify", "--appname=Blaze", "--urgency={urgency}", "--icon={icon}", "{title}", "{body}"}})
		}
	case "darwin":
		if have("terminal-notifier") {
			out = append(out, Method{Kind: "exec", Name: "terminal-notifier", Clicks: true, Argv: []string{"terminal-notifier", "-title", "{title}", "-message", "{body}", "-group", "blaze", "-activate", "com.wails.Blaze"}})
		}
		out = append(out, Method{Kind: "exec", Name: "osascript", Argv: []string{"/usr/bin/osascript", "-e", "on run argv\ndisplay notification (item 2 of argv) with title (item 1 of argv)\nend run", "{title}", "{body}"}})
	case "windows":
		out = append(out, Method{Kind: "exec", Name: "PowerShell toast", Escape: "ps-xml",
			Argv: []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-Command", "-"},
			Stdin: `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] > $null
$x = New-Object Windows.Data.Xml.Dom.XmlDocument
$x.LoadXml('<toast><visual><binding template="ToastGeneric"><text>{title}</text><text>{body}</text></binding></visual></toast>')
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe').Show([Windows.UI.Notifications.ToastNotification]::new($x))
`})
	}
	return out, missing
}

// fill substitutes the placeholders. Argv entries are passed straight to the program
// (no shell), so no quoting is needed there; stdin scripts escape per method.
func fill(s string, n Note, esc func(string) string) string {
	r := strings.NewReplacer("{title}", esc(n.Title), "{body}", esc(n.Body), "{urgency}", n.Urgency, "{icon}", IconPath(), "{app}", "Blaze")
	return r.Replace(s)
}

func psXML(s string) string {
	return strings.ReplaceAll(html.EscapeString(s), "'", "''")
}

// RunExec sends n with an exec method.
func RunExec(m Method, n Note) error {
	if m.Kind != "exec" || len(m.Argv) == 0 {
		return errors.New("not a command method")
	}
	n.Clean()
	id := func(s string) string { return s }
	argv := make([]string, len(m.Argv))
	for i, a := range m.Argv {
		argv[i] = fill(a, n, id)
	}
	if m.Name == "notify-send" || m.Name == "dunstify" {
		if _, err := os.Stat(IconPath()); err != nil {
			// no icon on disk: drop the flag rather than pass a missing path
			var keep []string
			for _, a := range argv {
				if !strings.HasPrefix(a, "--icon=") {
					keep = append(keep, a)
				}
			}
			argv = keep
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	hide(cmd)
	if m.Stdin != "" {
		esc := id
		if m.Escape == "ps-xml" {
			esc = psXML
		}
		cmd.Stdin = strings.NewReader(fill(m.Stdin, n, esc))
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		if msg != "" {
			return errors.New(m.Name + ": " + msg)
		}
		return errors.New(m.Name + ": " + err.Error())
	}
	if m.Name == "PowerShell toast" && strings.Contains(strings.ToLower(string(out)), "exception") {
		return errors.New(m.Name + ": " + strings.TrimSpace(string(out)))
	}
	return nil
}

// ---------------- quiet hours ----------------

// InQuiet is true when now falls between from and to ("22:00", "07:30"; may wrap midnight).
func InQuiet(now time.Time, from, to string) bool {
	f, ok1 := hm(from)
	t, ok2 := hm(to)
	if !ok1 || !ok2 || f == t {
		return false
	}
	m := now.Hour()*60 + now.Minute()
	if f < t {
		return m >= f && m < t
	}
	return m >= f || m < t
}

func hm(s string) (int, bool) {
	var h, m int
	if len(s) < 4 {
		return 0, false
	}
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}
