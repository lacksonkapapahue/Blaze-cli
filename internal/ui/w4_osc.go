package ui

// Parsing for the terminal background query (OSC 11). The query itself is in
// w4_osc_unix.go; this part is pure and tested.

import (
	"bytes"
	"strconv"
	"strings"
)

// parseOSC11 finds an OSC 11 reply ("ESC ] 11 ; rgb:RRRR/GGGG/BBBB" ended by BEL
// or ESC \) in b and returns the colour as 0..1 channels, and the reply's span.
func parseOSC11(b []byte) (r, g, bl float64, start, end int, ok bool) {
	start = bytes.Index(b, []byte("\x1b]11;"))
	if start < 0 {
		return 0, 0, 0, -1, -1, false
	}
	body := b[start+5:]
	stop, termLen := -1, 0
	for i := 0; i < len(body); i++ {
		if body[i] == 0x07 {
			stop, termLen = i, 1
			break
		}
		if body[i] == 0x1b && i+1 < len(body) && body[i+1] == '\\' {
			stop, termLen = i, 2
			break
		}
	}
	if stop < 0 {
		return 0, 0, 0, start, -1, false
	}
	end = start + 5 + stop + termLen
	r, g, bl, ok = parseColorSpec(string(body[:stop]))
	return r, g, bl, start, end, ok
}

// parseColorSpec reads X11 colour specs: rgb:R/G/B (1-4 hex digits per channel),
// rgba:R/G/B/A and #RRGGBB.
func parseColorSpec(s string) (r, g, b float64, ok bool) {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(s, "rgb:"), strings.HasPrefix(s, "rgba:"):
		_, v, _ := strings.Cut(s, ":")
		p := strings.Split(v, "/")
		if len(p) < 3 {
			return 0, 0, 0, false
		}
		ch := [3]float64{}
		for i := 0; i < 3; i++ {
			h := p[i]
			if len(h) < 1 || len(h) > 4 {
				return 0, 0, 0, false
			}
			n, err := strconv.ParseUint(h, 16, 16)
			if err != nil {
				return 0, 0, 0, false
			}
			ch[i] = float64(n) / float64(uint64(1)<<(4*len(h))-1)
		}
		return ch[0], ch[1], ch[2], true
	case strings.HasPrefix(s, "#") && len(s) == 7:
		n, err := strconv.ParseUint(s[1:], 16, 32)
		if err != nil {
			return 0, 0, 0, false
		}
		return float64(n>>16&0xff) / 255, float64(n>>8&0xff) / 255, float64(n&0xff) / 255, true
	}
	return 0, 0, 0, false
}

// bgIsLight: a background brighter than mid-grey (luma > 0.5) is a light paper.
func bgIsLight(r, g, b float64) bool { return 0.2126*r+0.7152*g+0.0722*b > 0.5 }

// da1End finds the end of a primary device attributes reply (ESC [ ? … c),
// which every terminal sends and so marks the end of the query's answers.
func da1End(b []byte) (start, end int) {
	i := bytes.Index(b, []byte("\x1b[?"))
	for i >= 0 {
		for j := i + 3; j < len(b); j++ {
			c := b[j]
			if c == 'c' {
				return i, j + 1
			}
			if !(c >= '0' && c <= '9' || c == ';') {
				break
			}
		}
		k := bytes.Index(b[i+3:], []byte("\x1b[?"))
		if k < 0 {
			break
		}
		i += 3 + k
	}
	return -1, -1
}

func osc11ReplyComplete(b []byte) bool {
	_, _, _, _, e, _ := parseOSC11(b)
	return e > 0
}

// splitReplies takes what was read after the query and returns the background
// verdict and the bytes that were not part of either reply (keys typed meanwhile).
func splitReplies(b []byte) (light, found bool, rest []byte) {
	rest = append([]byte(nil), b...)
	if r, g, bl, s, e, ok := parseOSC11(rest); s >= 0 && e > 0 {
		light, found = bgIsLight(r, g, bl), ok
		rest = append(rest[:s:s], rest[e:]...)
	}
	if s, e := da1End(rest); s >= 0 {
		rest = append(rest[:s:s], rest[e:]...)
	}
	return light, found, rest
}
