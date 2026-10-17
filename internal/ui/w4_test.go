package ui

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/notify"
)

func TestPresetContrast(t *testing.T) {
	for _, p := range Presets {
		for _, paper := range []string{darkPaper, "0;0;0", "18;18;18"} {
			if c := contrast(p.Dark, paper); c < 4.5 {
				t.Errorf("%s dark accent %s on %s: %.2f < 4.5", p.Name, p.Dark, paper, c)
			}
		}
		for _, paper := range []string{lightPaper, "255;255;255", "250;250;248"} {
			if c := contrast(p.Light, paper); c < 4.5 {
				t.Errorf("%s light accent %s on %s: %.2f < 4.5", p.Name, p.Light, paper, c)
			}
		}
	}
	if Presets[0].Name != "blaze" || Presets[0].Dark != "107;140;255" || Presets[0].Light != "31;75;216" {
		t.Error("blaze must stay the default and keep its blue")
	}
}

func TestHighContrastGreys(t *testing.T) {
	for _, c := range []struct{ name, col, paper string }{
		{"dark sub", highDark.sub, darkPaper}, {"dark weak", highDark.weak, darkPaper},
		{"light sub", highLight.sub, lightPaper}, {"light weak", highLight.weak, lightPaper},
	} {
		if r := contrast(c.col, c.paper); r < 4.5 {
			t.Errorf("high contrast %s: %.2f < 4.5", c.name, r)
		}
	}
	if contrast(highDark.hair, darkPaper) < 3 || contrast(highLight.hair, lightPaper) < 3 {
		t.Error("high contrast hairlines should reach 3:1")
	}
	if contrast(highDark.weak, darkPaper) <= contrast(baseDark.weak, darkPaper) {
		t.Error("high contrast must be stronger than normal")
	}
}

func TestApplyThemeKeepsPalIdentity(t *testing.T) {
	saveL, saveH, saveA := isLight, highCon, accentName
	defer func() { isLight, highCon, accentName = saveL, saveH, saveA; applyTheme() }()
	isLight, highCon = true, false
	if err := SetAccent("ember"); err != nil {
		t.Fatal(err)
	}
	if pal != light || pal.accent != "180;62;10" {
		t.Errorf("light ember: pal=%v", pal)
	}
	isLight, highCon = false, true
	applyTheme()
	if pal != dark || pal.accent != "255;140;90" || pal.weak != highDark.weak {
		t.Errorf("dark high ember: pal=%v", pal)
	}
	if SetAccent("rainbow") == nil {
		t.Error("unknown preset should fail")
	}
}

func TestSaveAccent(t *testing.T) {
	t.Setenv("BLAZE_HOME", t.TempDir())
	saveA := accentName
	defer func() { accentName = saveA; applyTheme() }()
	if err := SaveAccent("mint"); err != nil {
		t.Fatal(err)
	}
	if savedAccent() != "mint" || AccentName() != "mint" {
		t.Errorf("saved %q, in use %q", savedAccent(), AccentName())
	}
}

func TestParseOSC11(t *testing.T) {
	cases := []struct {
		in    string
		light bool
		ok    bool
	}{
		{"\x1b]11;rgb:ffff/ffff/ffff\x1b\\", true, true},
		{"\x1b]11;rgb:0000/0000/0000\x07", false, true},
		{"\x1b]11;rgb:1e1e/1e1e/2e2e\x1b\\", false, true},
		{"\x1b]11;rgb:fd/f6/e3\x1b\\", true, true}, // solarized light, 2-digit
		{"\x1b]11;rgba:2828/2c2c/3434/ffff\x07", false, true},
		{"\x1b]11;#fafafa\x07", true, true},
		{"\x1b]11;garbage\x07", false, false},
	}
	for _, c := range cases {
		r, g, b, s, e, ok := parseOSC11([]byte(c.in))
		if ok != c.ok || s != 0 || e != len(c.in) {
			t.Errorf("%q: ok=%v span=%d..%d", c.in, ok, s, e)
			continue
		}
		if ok && bgIsLight(r, g, b) != c.light {
			t.Errorf("%q: light=%v", c.in, !c.light)
		}
	}
	if _, _, _, _, _, ok := parseOSC11([]byte("\x1b]11;rgb:ffff/ffff")); ok {
		t.Error("unterminated reply must not parse")
	}
}

func TestSplitRepliesKeepsKeys(t *testing.T) {
	in := []byte("ab\x1b]11;rgb:ffff/ffff/ffff\x1b\\c\x1b[?62;22c")
	light, found, rest := splitReplies(in)
	if !light || !found || string(rest) != "abc" {
		t.Errorf("light=%v found=%v rest=%q", light, found, rest)
	}
	// no OSC 11 support: only DA1 comes back
	light, found, rest = splitReplies([]byte("\x1b[?1;2c"))
	if light || found || len(rest) != 0 {
		t.Errorf("DA1 only: light=%v found=%v rest=%q", light, found, rest)
	}
	if s, _ := da1End([]byte("\x1b[?1;2")); s >= 0 {
		t.Error("incomplete DA1 must not count")
	}
}

func TestOSC11ReplyComplete(t *testing.T) {
	osc := "\x1b]11;rgb:ffff/ffff/ffff\x1b\\"
	for _, tc := range []struct {
		name string
		got  string
		want bool
	}{
		{"no reply", "", false},
		{"incomplete OSC", "\x1b]11;rgb:ffff/ffff/ffff", false},
		{"complete OSC", osc, true},
		{"complete unsupported OSC", "\x1b]11;unsupported\x07", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := osc11ReplyComplete([]byte(tc.got)); got != tc.want {
				t.Fatalf("osc11ReplyComplete() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRemoteTerminal(t *testing.T) {
	keys := []string{"CODESPACES", "SSH_TTY", "SSH_CONNECTION", "TERM_PROGRAM"}
	original := make(map[string]string)
	exists := make(map[string]bool)
	for _, key := range keys {
		original[key], exists[key] = os.LookupEnv(key)
		_ = os.Unsetenv(key)
	}
	defer func() {
		for _, key := range keys {
			if exists[key] {
				_ = os.Setenv(key, original[key])
			} else {
				_ = os.Unsetenv(key)
			}
		}
	}()
	if remoteTerminal() {
		t.Fatal("local terminal detected as remote")
	}
	for _, key := range keys[:3] {
		_ = os.Setenv(key, "remote")
		if !remoteTerminal() {
			t.Errorf("%s did not identify a remote terminal", key)
		}
		_ = os.Unsetenv(key)
	}
	_ = os.Setenv("TERM_PROGRAM", "vscode")
	if !remoteTerminal() {
		t.Error("TERM_PROGRAM=vscode did not identify a remote terminal")
	}
	_ = os.Setenv("TERM_PROGRAM", "xterm")
	if remoteTerminal() {
		t.Error("TERM_PROGRAM=xterm identified a remote terminal")
	}
}

func TestLateTerminalRepliesDoNotCancelChooser(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply string
		split bool
	}{
		{"OSC", "]11;rgb:1e1e/1e1e/1e1e\x1b\\", false},
		{"DA1", "[?1;2c", false},
		{"split OSC", "]11;rgb:1e1e/1e1e/1e1e\x1b\\", true},
		{"split DA1", "[?1;2c", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var source io.Reader = strings.NewReader("\x1b" + tc.reply + "\r")
			if tc.split {
				reader, writer := io.Pipe()
				source = reader
				go func() {
					_, _ = writer.Write([]byte("\x1b"))
					time.Sleep(20 * time.Millisecond)
					_, _ = writer.Write([]byte(tc.reply + "\r"))
					_ = writer.Close()
				}()
			}
			in := bufio.NewReader(source)
			readRune := func() (rune, error) {
				r, _, err := in.ReadRune()
				return r, err
			}
			waited := false
			wait := func(timeout time.Duration) bool {
				waited = true
				if timeout != 50*time.Millisecond {
					t.Errorf("escape continuation timeout = %s, want 50ms", timeout)
				}
				time.Sleep(20 * time.Millisecond)
				return true
			}
			selected := -1
			done := rawKeysRead(func(key string) bool {
				if key == "esc" {
					selected = -2
					return true
				}
				if key == "enter" {
					selected = 0
					return true
				}
				return false
			}, readRune, in.Buffered, wait)
			if !done {
				t.Fatal("chooser did not finish on Enter")
			}
			if tc.split && !waited {
				t.Fatal("split ESC sequence did not wait for its continuation")
			}
			if selected != 0 {
				t.Fatalf("selected option %d; want default option 0", selected)
			}
		})
	}
}

func TestComposerSkipsTerminalReplies(t *testing.T) {
	oldIn := In
	defer func() { In = oldIn }()
	for _, tc := range []struct {
		name string
		data string
	}{
		{"OSC", "]11;rgb:1e1e/1e1e/1e1e\x1b\\\r"},
		{"DA1", "[?1;2c\r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			In = bufio.NewReader(strings.NewReader(tc.data))
			if _, err := In.Peek(1); err != nil {
				t.Fatal(err)
			}
			e := &Editor{}
			if e.escape() {
				t.Fatal("terminal reply triggered double-Esc")
			}
			if len(e.buf) != 0 {
				t.Fatalf("terminal reply inserted into composer: %q", string(e.buf))
			}
			next, _, err := In.ReadRune()
			if err != nil || next != '\r' {
				t.Fatalf("next input = %q, %v; want Enter", next, err)
			}
		})
	}
}

func TestTo256(t *testing.T) {
	cases := map[[3]int]int{
		{0, 0, 0}:       16,
		{255, 255, 255}: 231,
		{255, 0, 0}:     196,
		{0, 0, 255}:     21,
		{128, 128, 128}: 244,
		{240, 240, 238}: 255,
		{95, 135, 175}:  67,
		{18, 18, 18}:    233,
	}
	for in, want := range cases {
		if got := to256(in[0], in[1], in[2]); got != want {
			t.Errorf("to256(%v) = %d, want %d", in, got, want)
		}
	}
}

func TestColorDepth(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if !detectTruecolor(env(map[string]string{"COLORTERM": "truecolor"}), "linux") {
		t.Error("COLORTERM=truecolor")
	}
	if !detectTruecolor(env(map[string]string{"TERM": "xterm-direct"}), "linux") {
		t.Error("TERM direct")
	}
	if !detectTruecolor(env(map[string]string{"WT_SESSION": "x"}), "linux") {
		t.Error("Windows Terminal")
	}
	if detectTruecolor(env(map[string]string{"TERM": "xterm-256color"}), "darwin") {
		t.Error("plain xterm-256color should fall back")
	}
	if detectTruecolor(env(map[string]string{"COLORTERM": "truecolor", "BLAZE_COLOR": "256"}), "linux") {
		t.Error("BLAZE_COLOR=256 wins")
	}

	saveC, saveT := color, truecolor
	defer func() { color, truecolor = saveC, saveT }()
	color, truecolor = true, false
	if got := fg("255;0;0"); got != "\x1b[38;5;196m" {
		t.Errorf("256 fg = %q", got)
	}
	if got := bgEsc("0;0;0"); got != "\x1b[48;5;16m" {
		t.Errorf("256 bg = %q", got)
	}
	truecolor = true
	if got := fg("1;2;3"); got != "\x1b[38;2;1;2;3m" {
		t.Errorf("truecolor fg = %q", got)
	}
	color = false
	if fg("1;2;3") != "" {
		t.Error("no colour: no escape")
	}
}

func TestCleanTitleAndProgress(t *testing.T) {
	if got := cleanTitle("a\x07b\x1b]0;x\nc"); strings.ContainsAny(got, "\x07\x1b\n") {
		t.Errorf("control chars kept: %q", got)
	}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if !progressOK(env(map[string]string{"WT_SESSION": "1"})) || !progressOK(env(map[string]string{"TERM_PROGRAM": "ghostty"})) {
		t.Error("WT / ghostty draw progress")
	}
	if progressOK(env(map[string]string{"TERM_PROGRAM": "iTerm.app"})) {
		t.Error("unknown terminals: no OSC 9 (it can mean notify)")
	}
	if progressOK(env(map[string]string{"WT_SESSION": "1", "BLAZE_PROGRESS": "0"})) {
		t.Error("BLAZE_PROGRESS=0 turns it off")
	}
}

func TestShouldPing(t *testing.T) {
	if shouldPing(19*time.Second, false) || shouldPing(time.Minute, true) || !shouldPing(20*time.Second, false) {
		t.Error("ping only for 20s+ turns while unfocused")
	}
	sent := make(chan notify.Note, 1)
	old := pingSend
	pingSend = func(n notify.Note) { sent <- n }
	defer func() { pingSend = old; focused.Store(true) }()
	focused.Store(false)
	TurnDone(25 * time.Second)
	select {
	case n := <-sent:
		if !strings.Contains(n.Body, "25s") {
			t.Errorf("body %q", n.Body)
		}
	case <-time.After(time.Second):
		t.Error("no notification")
	}
	focused.Store(true)
	TurnDone(25 * time.Second)
	select {
	case <-sent:
		t.Error("focused: no notification")
	case <-time.After(50 * time.Millisecond):
	}
}

func day(s string) time.Time {
	t, _ := time.ParseInLocation("2006-01-02 15:04", s+" 12:00", time.Local)
	return t
}

func TestStreakAndWeek(t *testing.T) {
	var s Stats
	for _, d := range []string{"2026-10-12", "2026-10-13", "2026-10-14", "2026-10-16"} {
		s.add(day(d), time.Minute, 3, 1)
	}
	s.add(day("2026-10-16"), 30*time.Second, 2, 0)
	if n := s.Streak(day("2026-10-16")); n != 1 {
		t.Errorf("streak on the 16th = %d, want 1 (gap on the 15th)", n)
	}
	if n := s.Streak(day("2026-10-17")); n != 1 {
		t.Errorf("streak alive on the 17th before any task = %d, want 1", n)
	}
	if n := s.Streak(day("2026-10-18")); n != 0 {
		t.Errorf("streak broken on the 18th = %d", n)
	}
	if n := s.Streak(day("2026-10-14")); n != 3 {
		t.Errorf("streak on the 14th = %d, want 3", n)
	}
	// 2026-10-17 is a Saturday: the week is Mon 12th..Sat 17th
	tasks, secs, files := s.Week(day("2026-10-17"))
	if tasks != 5 || secs != 270 || files != 4 {
		t.Errorf("week = %d tasks, %.0fs, %d files", tasks, secs, files)
	}
	if got := s.Last7(day("2026-10-17")); got != [7]int{0, 1, 1, 1, 0, 2, 0} {
		t.Errorf("last7 = %v", got)
	}
}

func TestStatsCap(t *testing.T) {
	var s Stats
	start := day("2025-01-01")
	for i := 0; i < 500; i++ {
		s.add(start.AddDate(0, 0, i), time.Second, 1, 0)
	}
	if len(s.Days) != statsKeepDays {
		t.Errorf("kept %d days, want %d", len(s.Days), statsKeepDays)
	}
}

func TestRecordTurnFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BLAZE_HOME", home)
	RecordTurn(3*time.Second, 4, 2)
	RecordTurn(2*time.Second, 1, 0)
	s := LoadStats()
	if len(s.Days) != 1 || s.Days[0].Tasks != 2 || s.Days[0].Steps != 5 || s.Days[0].Files != 2 {
		t.Errorf("stats %+v", s)
	}
	ents, _ := os.ReadDir(home)
	for _, e := range ents {
		if e.Name() != "stats.json" {
			t.Errorf("leftover %s", filepath.Join(home, e.Name()))
		}
	}
}

func TestSparkline(t *testing.T) {
	if got := SparkText([]int{0, 1, 2, 4, 8, 0, 8}); got != "▁▂▂▃▇▁▇" {
		t.Errorf("spark = %q", got)
	}
	if got := SparkText([]int{0, 0, 1}); got != "▁▁▇" {
		t.Errorf("single = %q", got)
	}
	l := sparkline([]int{0, 3})
	if l[0] != -1 || l[1] != len(sparkRunes)-1 {
		t.Errorf("levels %v", l)
	}
}

func TestCardsRender(t *testing.T) {
	saveC := color
	defer func() { color = saveC }()
	color = false
	var s Stats
	s.add(day("2026-10-17"), 90*time.Second, 5, 2)
	statsCard(s, day("2026-10-17"))
	ThemeCard()
}
