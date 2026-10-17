package ui

// Themes: one accent at a time (blaze, ember, mint, violet, mono), a dark and a
// light paper, and a high-contrast set of greys. A preset only swaps the single
// accent; everything else stays ink on paper.
//
// Choice order for the accent: BLAZE_ACCENT, then the saved choice
// (~/.blaze/cli/theme.json, written by /theme), then blaze.
// Light or dark: BLAZE_THEME=light|dark, else the terminal's own background
// (OSC 11 query), else COLORFGBG, else dark.
// BLAZE_CONTRAST=high strengthens the greys (sub, weak, hair).

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
)

// Preset is one accent with a value for each paper.
type Preset struct {
	Name, Desc  string
	Dark, Light string // "r;g;b"; each at least 4.5:1 against its paper (WCAG AA)
}

// Presets in menu order. The first is the default.
var Presets = []Preset{
	{"blaze", "Blaze blue (default)", "107;140;255", "31;75;216"},
	{"ember", "Warm orange", "255;140;90", "180;62;10"},
	{"mint", "Fresh green", "72;210;160", "10;120;82"},
	{"violet", "Soft purple", "176;148;255", "108;52;214"},
	{"mono", "No colour: the accent is ink", "240;240;238", "17;17;16"},
}

// Papers the presets are checked against (the darkest-light and lightest-dark
// backgrounds Blaze paints on; real terminals are usually further apart).
const (
	darkPaper  = "38;38;42"
	lightPaper = "252;252;252"
)

// The base palettes (normal and high contrast), before an accent is applied.
var (
	baseDark   = palette{"240;240;238", "214;214;212", "160;160;158", "110;110;108", "70;70;74", "107;140;255"}
	baseLight  = palette{"17;17;16", "45;45;44", "100;100;98", "150;150;148", "205;205;203", "31;75;216"}
	highDark   = palette{"255;255;255", "235;235;233", "205;205;203", "170;170;168", "125;125;128", "107;140;255"}
	highLight  = palette{"0;0;0", "20;20;20", "58;58;56", "92;92;90", "125;125;123", "31;75;216"}
	isLight    bool
	highCon    bool
	accentName = "blaze"
)

// PresetByName finds a preset ("" or "default" is blaze).
func PresetByName(name string) (Preset, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || name == "default" {
		name = "blaze"
	}
	for _, p := range Presets {
		if p.Name == name {
			return p, true
		}
	}
	return Preset{}, false
}

// AccentName is the preset in use.
func AccentName() string { return accentName }

// IsLight is true on a light paper.
func IsLight() bool { return isLight }

// HighContrast is true with BLAZE_CONTRAST=high.
func HighContrast() bool { return highCon }

// AccentFromEnv is true when BLAZE_ACCENT picks the accent (it beats /theme).
func AccentFromEnv() bool {
	_, ok := PresetByName(os.Getenv("BLAZE_ACCENT"))
	return ok && os.Getenv("BLAZE_ACCENT") != ""
}

// applyTheme rebuilds dark/light from the base greys and the chosen accent and
// points pal at the right one. (Other files compare pal == light, so the light and
// dark variables themselves are rebuilt, never pal alone.)
func applyTheme() {
	p, ok := PresetByName(accentName)
	if !ok {
		p = Presets[0]
		accentName = p.Name
	}
	d, l := baseDark, baseLight
	if highCon {
		d, l = highDark, highLight
	}
	d.accent, l.accent = p.Dark, p.Light
	dark, light = d, l
	if isLight {
		pal = light
	} else {
		pal = dark
	}
}

// SetAccent switches to a preset for this run (Theme saves it).
func SetAccent(name string) error {
	p, ok := PresetByName(name)
	if !ok {
		return errors.New("no theme " + name + " (try " + PresetNames() + ")")
	}
	accentName = p.Name
	applyTheme()
	return nil
}

// PresetNames is "blaze, ember, mint, violet, mono".
func PresetNames() string {
	n := make([]string, len(Presets))
	for i, p := range Presets {
		n[i] = p.Name
	}
	return strings.Join(n, ", ")
}

func themeFile() string { return filepath.Join(config.Dir(), "cli", "theme.json") }

type themePrefs struct {
	Accent string `json:"accent"`
}

func savedAccent() string {
	var t themePrefs
	if b, err := os.ReadFile(themeFile()); err == nil {
		_ = json.Unmarshal(b, &t)
	}
	return t.Accent
}

// SaveAccent switches to a preset and keeps it for next time.
func SaveAccent(name string) error {
	if err := SetAccent(name); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(themeFile()), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(themePrefs{Accent: accentName}, "", "  ")
	return writeAtomic(themeFile(), b)
}

// writeAtomic writes through a temp file and a rename, so a crash never leaves
// half a file.
func writeAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	_ = os.Chmod(name, 0o600)
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// initTheme picks paper, contrast and accent (called from Init).
func initTheme() {
	highCon = strings.EqualFold(os.Getenv("BLAZE_CONTRAST"), "high")
	switch strings.ToLower(os.Getenv("BLAZE_THEME")) {
	case "light":
		isLight = true
	case "dark":
		isLight = false
	default:
		if l, ok := queryBackground(); ok {
			isLight = l
		} else {
			isLight = colorfgbgLight(os.Getenv("COLORFGBG"))
		}
	}
	accentName = "blaze"
	if v := os.Getenv("BLAZE_ACCENT"); v != "" {
		if p, ok := PresetByName(v); ok {
			accentName = p.Name
		}
	} else if v := savedAccent(); v != "" {
		if p, ok := PresetByName(v); ok {
			accentName = p.Name
		}
	}
	applyTheme()
}

// colorfgbgLight: COLORFGBG="0;15" means dark text on a light background.
func colorfgbgLight(v string) bool {
	if v == "" {
		return false
	}
	parts := strings.Split(v, ";")
	bg := parts[len(parts)-1]
	return bg == "15" || bg == "7"
}

// Swatch is a preset's accent painted as "■■" on the current paper.
func Swatch(p Preset) string {
	c := p.Dark
	if isLight {
		c = p.Light
	}
	return fgEsc(c) + "■■" + reset()
}

// ThemeCard shows every preset (01 / 02 list with a swatch, the one in use marked)
// and a preview row of the current accent in Blaze's pieces.
func ThemeCard() {
	var lines []string
	for i, p := range Presets {
		mark := Weak("□")
		name := Body(p.Name)
		if p.Name == accentName {
			mark, name = Accent("■"), Ink(p.Name)
		}
		lines = append(lines, Weak(pad2(i+1))+"  "+Swatch(p)+"  "+mark+" "+name+strings.Repeat(" ", max(1, 8-len(p.Name)))+Sub(p.Desc))
	}
	lines = append(lines, "")
	row := ""
	for _, p := range Presets {
		row += Swatch(p) + " "
	}
	lines = append(lines, Field("swatches", 17)+row)
	lines = append(lines, Field("preview", 17)+AccentLabel(accentName)+"  "+Accent("■")+" "+Ink("done")+"  "+Accent("›")+" "+Body("ink on paper")+"  "+Weak("· weak")+"  "+hair("────"))
	paper := "dark"
	if isLight {
		paper = "light"
	}
	if highCon {
		paper += " · high contrast"
	}
	lines = append(lines, Field("paper", 17)+Sub(paper)+Weak("  · BLAZE_THEME, BLAZE_CONTRAST=high"))
	Box(AccentLabel("theme"), lines)
}

func pad2(n int) string { return fmt.Sprintf("%02d", n) }
