package notify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func tempHome(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	t.Setenv("BLAZE_HOME", d)
	return d
}

func TestCacheRoundTrip(t *testing.T) {
	home := tempHome(t)
	if Load() != nil {
		t.Fatal("no cache yet, Load should be nil")
	}
	c := NewCache(time.Now())
	c.Choose(&Method{Kind: "native", Name: "D-Bus notifications · mako", Clicks: true}, "",
		[]Method{{Kind: "exec", Name: "notify-send", Argv: []string{"notify-send", "{title}", "{body}"}}}, []string{"dunstify"})
	c.Save()
	if _, err := os.Stat(filepath.Join(home, "notify.json")); err != nil {
		t.Fatalf("cache not written to BLAZE_HOME/notify.json: %v", err)
	}
	got := Load()
	if got == nil {
		t.Fatal("Load returned nil for a cache made on this machine")
	}
	if got.Primary == nil || got.Primary.Kind != "native" || !got.Primary.Clicks {
		t.Fatalf("primary = %+v", got.Primary)
	}
	if got.Fallback == nil || got.Fallback.Name != "notify-send" || len(got.Fallback.Argv) != 3 {
		t.Fatalf("fallback = %+v", got.Fallback)
	}
	if len(got.Tried) != 1 || got.Tried[0] != "dunstify" {
		t.Fatalf("tried = %v", got.Tried)
	}
}

func TestCacheInvalidElsewhere(t *testing.T) {
	tempHome(t)
	write := func(mut func(c *Cache)) {
		c := NewCache(time.Now())
		c.Choose(nil, "", []Method{{Kind: "exec", Name: "x", Argv: []string{"x"}}}, nil)
		mut(c)
		b, _ := json.Marshal(c)
		_ = os.MkdirAll(filepath.Dir(Path()), 0o700)
		if err := os.WriteFile(Path(), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cases := map[string]func(c *Cache){
		"other os":      func(c *Cache) { c.OS = "plan9" },
		"other host":    func(c *Cache) { c.Host = c.Host + "-elsewhere" },
		"other desktop": func(c *Cache) { c.Desktop = c.Desktop + "-other" },
		"old version":   func(c *Cache) { c.Version = 1 },
		"no method":     func(c *Cache) { c.Primary = nil },
	}
	for name, mut := range cases {
		write(mut)
		if Load() != nil {
			t.Errorf("%s: Load should force a new probe", name)
		}
	}
	write(func(c *Cache) {})
	if Load() == nil {
		t.Error("unchanged cache should load")
	}
	_ = os.WriteFile(Path(), []byte("{not json"), 0o600)
	if Load() != nil {
		t.Error("corrupt cache should be ignored")
	}
}

func TestChoose(t *testing.T) {
	c := NewCache(time.Now())
	execs := []Method{{Kind: "exec", Name: "notify-send"}, {Kind: "exec", Name: "dunstify"}}
	c.Choose(nil, "D-Bus notifications: no notification daemon is running", execs, []string{"libnotify"})
	if c.Primary == nil || c.Primary.Name != "notify-send" || c.Fallback == nil || c.Fallback.Name != "notify-send" {
		t.Fatalf("without native the first command should be primary and fallback: %+v %+v", c.Primary, c.Fallback)
	}
	if len(c.Tried) != 2 || !strings.Contains(c.Tried[0], "daemon") {
		t.Fatalf("tried = %v", c.Tried)
	}
	c.Choose(&Method{Kind: "native", Name: "Windows toast", Clicks: true}, "", execs, nil)
	if c.Primary.Kind != "native" || c.Fallback.Name != "notify-send" || len(c.Tried) != 0 {
		t.Fatalf("native should win: %+v %+v %v", c.Primary, c.Fallback, c.Tried)
	}
	c.Choose(nil, "", nil, []string{"notify-send"})
	if c.Primary != nil || c.Fallback != nil || c.Valid() {
		t.Fatal("nothing found: no method and not valid")
	}
}

func TestFillAndClean(t *testing.T) {
	n := Note{Title: "  Build\n failed  ", Body: strings.Repeat("x", 400), Urgency: "loud"}
	n.Clean()
	if n.Title != "Build failed" || n.Urgency != "normal" || len([]rune(n.Body)) != 280 {
		t.Fatalf("clean: %q %q %d", n.Title, n.Urgency, len([]rune(n.Body)))
	}
	n = Note{Title: `It's <done> & "ok"`, Body: "b"}
	got := fill("<text>{title}</text>", n, psXML)
	if got != "<text>It&#39;s &lt;done&gt; &amp; &#34;ok&#34;</text>" {
		t.Fatalf("ps-xml escape (no bare quote may reach the PowerShell string): %s", got)
	}
	if fill("{title}", n, func(s string) string { return s }) != n.Title {
		t.Fatal("argv fill must not escape")
	}
}

func TestRunExec(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	tempHome(t)
	out := filepath.Join(t.TempDir(), "got")
	m := Method{Kind: "exec", Name: "sh", Argv: []string{"sh", "-c", `printf '%s|%s' "$1" "$2" > ` + out, "sh", "{title}", "{body}"}}
	if err := RunExec(m, Note{Title: "Tests `rm -rf` $(x)", Body: "done; ok"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	if string(b) != "Tests `rm -rf` $(x)|done; ok" {
		t.Fatalf("placeholders must reach the program unquoted and unexpanded, got %q", b)
	}
	if err := RunExec(Method{Kind: "exec", Name: "false", Argv: []string{"false"}}, Note{Title: "x"}); err == nil {
		t.Fatal("a failing command must return an error")
	}
	if err := RunExec(Method{Kind: "native"}, Note{}); err == nil {
		t.Fatal("native is not a command")
	}
}

func TestHistory(t *testing.T) {
	tempHome(t)
	now := time.Now()
	Record(Entry{Note: Note{ID: "a", Kind: "suggest", Title: "Your build failed", Body: "The test in price.ts fails"}, At: now.Add(-20 * time.Minute)})
	Record(Entry{Note: Note{ID: "b", Kind: "task", Title: "Done"}, At: now.Add(-time.Minute)})
	h := History()
	if len(h) != 2 || h[0].ID != "b" {
		t.Fatalf("history newest first: %+v", h)
	}
	if !Similar(h, "Build failed", "The price.ts test fails") {
		t.Error("near-duplicate suggestion should be similar")
	}
	if Similar(h, "Form looks long", "Want help filling the visa form") {
		t.Error("different suggestion is not similar")
	}
	if IgnoredAt(h, now) != 1 {
		t.Errorf("ignored = %d", IgnoredAt(h, now))
	}
	if _, ok := Mark("a", true); !ok {
		t.Fatal("mark")
	}
	if IgnoredAt(History(), now) != 0 {
		t.Error("a clicked suggestion resets the backoff")
	}
	for i := 0; i < historyMax+5; i++ {
		Record(Entry{Note: Note{ID: "x"}, At: now})
	}
	if len(History()) != historyMax {
		t.Errorf("history is capped at %d", historyMax)
	}
}
