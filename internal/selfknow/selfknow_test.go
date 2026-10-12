package selfknow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sample() Facts {
	return Facts{
		App: "Blaze Desktop", Version: "v0.12.0", Build: "go1.25 · abc1234", DataDir: "/home/luke/.blaze",
		Account: Account{SignedIn: true, Email: "luke@example.com", Service: Worker, Online: true},
		Router: Router{Present: true, Running: true, Enabled: true, URL: "http://127.0.0.1:20128", Port: 20128, Ready: 3, Models: 42,
			Presets: []Entry{{Name: "Auto"}, {Name: "Best Free"}}},
		Views:    []Entry{{Name: "Memory", Desc: "What Blaze remembers"}},
		Commands: []Entry{{Name: "new", Desc: "Start a new chat"}},
		Skills:   []Entry{{Name: "deploy-site"}}, SkillsOff: 2, Memories: 17,
		PhonePrev: true,
		System:    System{OSName: "Arch Linux", OS: "linux", Arch: "amd64", CPU: "Ryzen 7 7840U", Cores: 16, RAMGB: 32, Desktop: "Hyprland", Session: "wayland"},
	}
}

func TestBriefHasAboutYouAndRule(t *testing.T) {
	b := Brief(sample())
	for _, want := range []string{"ABOUT YOU", "Blaze Desktop v0.12.0", "Lackson Kapapa", "Blaze Android", "Blaze CLI", "BlazeRouter: on", "127.0.0.1:20128",
		"signed in as luke@example.com", "Arch Linux", "Hyprland wayland", ToolName, "Never ask the user which apps they are building"} {
		if !strings.Contains(b, want) {
			t.Errorf("brief misses %q:\n%s", want, b)
		}
	}
	if n := len(strings.Fields(b)); n > 330 {
		t.Errorf("brief is %d words; keep ABOUT YOU to a few hundred tokens", n)
	}
}

func TestLinkHonestWhenNotInBuild(t *testing.T) {
	f := sample()
	r := Report(f, "link")
	if !strings.Contains(r, "NOT in this Blaze Desktop v0.12.0 build") || !strings.Contains(r, "Settings › Blaze Desktop") || !strings.Contains(r, "Paired phones: none") {
		t.Fatalf("link report:\n%s", r)
	}
	f.Link = Link{InBuild: true, Devices: []Device{{ID: "d1", Name: "Pixel 8", PairedAt: time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)}}, Online: "Pixel 8"}
	r = Report(f, "phone")
	for _, want := range []string{"Pair, step by step", "Show pairing QR code", "Paired phone: Pixel 8", "Online now: Pixel 8", "7377"} {
		if !strings.Contains(r, want) {
			t.Errorf("paired link report misses %q:\n%s", want, r)
		}
	}
	if !strings.Contains(Brief(f), "1 phone(s) paired (Pixel 8), Pixel 8 online") {
		t.Errorf("brief link line: %s", Brief(f))
	}
}

func TestReportSections(t *testing.T) {
	all := Report(sample(), "")
	for _, s := range Sections {
		if !strings.Contains(all, strings.ToUpper(s[:3])) {
			t.Errorf("full report misses section %s", s)
		}
	}
	for q, want := range map[string]string{"router": "Presets (2): Auto, Best Free", "commands": "/new: Start a new chat", "views": "Memory: What Blaze remembers",
		"skills": "1 on, 2 off", "memory": "17 memories", "system": "Ryzen 7 7840U (16 threads)", "account": "signed in as luke@example.com", "apps": "Design › Phone"} {
		if r := Report(sample(), q); !strings.Contains(r, want) {
			t.Errorf("section %s misses %q:\n%s", q, want, r)
		}
	}
	if r := Report(sample(), "nonsense"); !strings.HasPrefix(r, "Error:") {
		t.Errorf("unknown section: %s", r)
	}
}

func TestDevicesAndLayout(t *testing.T) {
	d := t.TempDir()
	if Devices(d) != nil {
		t.Fatal("no file should mean no devices")
	}
	_ = os.MkdirAll(filepath.Join(d, "pair"), 0o700)
	_ = os.WriteFile(filepath.Join(d, "pair", "devices.json"), []byte(`[{"id":"a","name":"Galaxy","token_hash":"x","paired_at":"2026-10-10T08:00:00Z"}]`), 0o600)
	ds := Devices(d)
	if len(ds) != 1 || ds[0].Name != "Galaxy" {
		t.Fatalf("devices: %+v", ds)
	}
	_ = os.WriteFile(filepath.Join(d, "blaze.db"), nil, 0o600)
	l := Layout(d)
	if len(l) != 2 || !strings.Contains(l[0].Desc, "memories") || !strings.HasSuffix(l[1].Name, string(filepath.Separator)) {
		t.Fatalf("layout: %+v", l)
	}
}

func TestDesktopEnv(t *testing.T) {
	env := map[string]string{"XDG_CURRENT_DESKTOP": "Hyprland", "WAYLAND_DISPLAY": "wayland-1"}
	de, se := DesktopEnv(func(k string) string { return env[k] })
	if de != "Hyprland" || se != "wayland" {
		t.Fatalf("%q %q", de, se)
	}
}
