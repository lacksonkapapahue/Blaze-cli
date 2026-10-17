package ui

// v0.12.0 menu polish (W3 #14): the matched characters of each menu row are drawn
// in the accent, recently used slash commands come first while the query is still
// short, and a thin section label (R E C E N T / C O M M A N D S) splits the two.

import (
	"os"
	"regexp"
	"strings"
	"sync"
	"unicode"
)

// RecentFile is where recently used slash commands persist, one per line
// ("" = memory only). Set by the host (config dir).
var RecentFile string

const (
	recentKeep = 8 // commands remembered
	recentShow = 4 // shown in the RECENT section
)

var (
	recentMu     sync.Mutex
	recentList   []string // newest first
	recentLoaded string   // the RecentFile the list was loaded from
	recentInit   bool
)

var recentCmd = regexp.MustCompile(`^/[a-z][a-z0-9_-]*$`)

func recentLoad() {
	if recentInit && recentLoaded == RecentFile {
		return
	}
	recentInit, recentLoaded, recentList = true, RecentFile, nil
	if RecentFile == "" {
		return
	}
	b, err := os.ReadFile(RecentFile)
	if err != nil {
		return
	}
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); recentCmd.MatchString(l) && len(recentList) < recentKeep {
			recentList = append(recentList, l)
		}
	}
}

// RecordRecent notes that a line was sent: a slash command moves to the front of
// the recent list. Anything else is ignored.
func RecordRecent(line string) {
	f := strings.Fields(line)
	if len(f) == 0 || !recentCmd.MatchString(f[0]) {
		return
	}
	recentMu.Lock()
	defer recentMu.Unlock()
	recentLoad()
	recentList = pushRecent(recentList, f[0], recentKeep)
	if RecentFile != "" {
		_ = os.WriteFile(RecentFile, []byte(strings.Join(recentList, "\n")+"\n"), 0o600)
	}
}

// Recent returns the recently used commands, newest first.
func Recent() []string {
	recentMu.Lock()
	defer recentMu.Unlock()
	recentLoad()
	return append([]string(nil), recentList...)
}

// pushRecent puts cmd first in list (once), keeping at most keep entries.
func pushRecent(list []string, cmd string, keep int) []string {
	out := []string{cmd}
	for _, c := range list {
		if c != cmd && len(out) < keep {
			out = append(out, c)
		}
	}
	return out
}

// menuQuery is the text the menu for line is filtered by: the last word, without
// its "/" or "@", and for paths only the part after the last separator.
func menuQuery(line string) string {
	q := line[strings.LastIndexAny(line, " \t\n")+1:]
	q = strings.TrimLeft(q, "/@")
	if i := strings.LastIndexAny(q, `/\`); i >= 0 {
		q = q[i+1:]
	}
	return q
}

// withRecent returns m ready to draw for line: its Query set and, for the top-level
// slash command menu with a query of at most one letter, the recently used
// commands moved first with sections. m itself is never changed.
func withRecent(line string, m *MenuSet, recent []string) *MenuSet {
	if m == nil {
		return nil
	}
	c := *m
	if c.Query == "" {
		c.Query = menuQuery(line)
	}
	if !strings.HasPrefix(line, "/") || strings.ContainsAny(line, " \t\n") || len([]rune(line)) > 2 || len(recent) == 0 {
		return &c
	}
	rank := map[string]int{}
	for i, r := range recent {
		if _, ok := rank[r]; !ok {
			rank[r] = i
		}
	}
	var top []MenuItem
	var rest []MenuItem
	for _, it := range c.Items {
		if _, ok := rank[strings.TrimSpace(it.Insert)]; ok && len(top) < recentShow {
			it.Section = "recent"
			top = append(top, it)
			continue
		}
		rest = append(rest, it)
	}
	if len(top) == 0 {
		return &c
	}
	// newest first among the recent ones
	for i := 1; i < len(top); i++ {
		for j := i; j > 0 && rank[strings.TrimSpace(top[j].Insert)] < rank[strings.TrimSpace(top[j-1].Insert)]; j-- {
			top[j], top[j-1] = top[j-1], top[j]
		}
	}
	sec := c.Title
	if sec == "" {
		sec = "commands"
	}
	items := make([]MenuItem, 0, len(c.Items))
	items = append(items, top...)
	for _, it := range rest {
		it.Section = sec
		items = append(items, it)
	}
	c.Items = items
	return &c
}

// matchSpots marks which runes of name match q (case-insensitive): a contiguous
// run if q appears in name, else the first subsequence; nil when q doesn't match.
func matchSpots(name, q string) []bool {
	if q == "" {
		return nil
	}
	nr := []rune(name)
	qr := []rune(strings.ToLower(q))
	lr := make([]rune, len(nr))
	for i, r := range nr {
		lr[i] = unicode.ToLower(r)
	}
	spots := make([]bool, len(nr))
	if i := indexRunes(lr, qr); i >= 0 {
		for k := range qr {
			spots[i+k] = true
		}
		return spots
	}
	j := 0
	for i := 0; i < len(lr) && j < len(qr); i++ {
		if lr[i] == qr[j] {
			spots[i] = true
			j++
		}
	}
	if j < len(qr) {
		return nil
	}
	return spots
}

func indexRunes(s, sub []rune) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		ok := true
		for k := range sub {
			if s[i+k] != sub[k] {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

// highlight draws name with the runes matching q in the accent (bold when the row
// is selected) and the rest with base.
func highlight(name, q string, base func(string) string, selected bool) string {
	spots := matchSpots(name, q)
	if spots == nil {
		return base(name)
	}
	hi := Accent
	if selected {
		hi = func(s string) string { return bold() + Accent(s) }
	}
	var b strings.Builder
	nr := []rune(name)
	for i := 0; i < len(nr); {
		j := i
		for j < len(nr) && spots[j] == spots[i] {
			j++
		}
		if spots[i] {
			b.WriteString(hi(string(nr[i:j])))
		} else {
			b.WriteString(base(string(nr[i:j])))
		}
		i = j
	}
	return b.String()
}
