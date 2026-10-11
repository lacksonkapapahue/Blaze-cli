package notify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
)

// Entry is one notification Blaze sent (Settings › Notifications › History).
type Entry struct {
	Note
	At      time.Time `json:"at"`
	Method  string    `json:"method"`
	Err     string    `json:"error,omitempty"`
	Clicked bool      `json:"clicked,omitempty"`
	Dismiss bool      `json:"dismissed,omitempty"`
}

const historyMax = 100

func historyPath() string { return filepath.Join(config.Dir(), "desktop", "notify-history.json") }

// History is newest first.
func History() []Entry {
	mu.Lock()
	defer mu.Unlock()
	return loadHistory()
}

func loadHistory() []Entry {
	b, err := os.ReadFile(historyPath())
	if err != nil {
		return nil
	}
	var h []Entry
	_ = json.Unmarshal(b, &h)
	return h
}

func saveHistory(h []Entry) {
	if len(h) > historyMax {
		h = h[:historyMax]
	}
	_ = os.MkdirAll(filepath.Dir(historyPath()), 0o700)
	b, _ := json.Marshal(h)
	_ = os.WriteFile(historyPath(), b, 0o600)
}

// Record adds an entry at the top.
func Record(e Entry) {
	mu.Lock()
	defer mu.Unlock()
	h := loadHistory()
	saveHistory(append([]Entry{e}, h...))
}

// Mark sets clicked or dismissed on the entry with id; it returns the entry.
func Mark(id string, clicked bool) (Entry, bool) {
	mu.Lock()
	defer mu.Unlock()
	h := loadHistory()
	for i := range h {
		if h[i].ID == id {
			if clicked {
				h[i].Clicked = true
			} else {
				h[i].Dismiss = true
			}
			saveHistory(h)
			return h[i], true
		}
	}
	return Entry{}, false
}

// ClearHistory forgets every entry.
func ClearHistory() {
	mu.Lock()
	defer mu.Unlock()
	_ = os.Remove(historyPath())
}

// SentSince counts notifications of kind sent after t ("" = any kind).
func SentSince(h []Entry, kind string, t time.Time) int {
	n := 0
	for _, e := range h {
		if e.At.Before(t) {
			break
		}
		if (kind == "" || e.Kind == kind) && e.Err == "" {
			n++
		}
	}
	return n
}

// Ignored counts the latest suggestions in a row that were dismissed or never clicked.
func Ignored(h []Entry) int { return IgnoredAt(h, time.Now()) }

// IgnoredAt is Ignored at a given time (a suggestion counts as ignored after 15 minutes).
func IgnoredAt(h []Entry, now time.Time) int {
	n := 0
	for _, e := range h {
		if e.Kind != "suggest" {
			continue
		}
		if e.Clicked {
			break
		}
		if e.Dismiss || now.Sub(e.At) > 15*time.Minute {
			n++
		}
	}
	return n
}

// Similar is true when a recent suggestion (last 24 h) says much the same thing.
func Similar(h []Entry, title, body string) bool {
	a := words(title + " " + body)
	for _, e := range h {
		if time.Since(e.At) > 24*time.Hour {
			break
		}
		if e.Kind != "suggest" {
			continue
		}
		if jaccard(a, words(e.Title+" "+e.Body)) >= 0.5 {
			return true
		}
	}
	return false
}

func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if len(w) > 2 {
			out[w] = true
		}
	}
	return out
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	in := 0
	for w := range a {
		if b[w] {
			in++
		}
	}
	return float64(in) / float64(len(a)+len(b)-in)
}
