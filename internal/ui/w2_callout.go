package ui

// Callouts: GitHub-style "> [!NOTE]" blocks and paragraphs that open with
// "Note:" / "Warning:" / "Tip:" render as an accent-ruled block under a spaced
// label, so they stand apart from the answer around them.

import (
	"regexp"
	"strings"
)

var (
	reCalloutQ = regexp.MustCompile(`(?i)^>\s*\[!(note|tip|warning|important|caution)\]\s*(.*)$`)
	reCalloutP = regexp.MustCompile(`(?i)^(?:\*\*|__)?(note|tip|warning|important|caution)(?::\*\*|\*\*:|:__|__:|:)\s+(.*)$`)
)

// calloutStart detects a callout opening line (trimmed). quote is true for
// the "> [!KIND]" form, which continues over following "> " lines; the
// paragraph form continues until a blank line or another block.
func calloutStart(t string) (kind, rest string, quote, ok bool) {
	if g := reCalloutQ.FindStringSubmatch(t); g != nil {
		return strings.ToLower(g[1]), g[2], true, true
	}
	if g := reCalloutP.FindStringSubmatch(t); g != nil {
		return strings.ToLower(g[1]), g[2], false, true
	}
	return "", "", false, false
}

func calloutRule() string { return "  " + Accent("│") + " " }

// calloutHead is the label line: ■ for warnings, · for the rest.
func calloutHead(kind string) string {
	if kind == "caution" {
		kind = "warning"
	}
	if kind == "warning" {
		return calloutRule() + Accent("■") + " " + AccentLabel(kind)
	}
	return calloutRule() + Weak("·") + " " + Label(kind)
}

// blockStart reports whether a trimmed line opens a new markdown block, which
// ends a paragraph callout.
func blockStart(t string) bool {
	return strings.HasPrefix(t, "```") || reHeading.MatchString(t) || strings.HasPrefix(t, ">") ||
		strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") || strings.HasPrefix(t, "• ") ||
		reNum.MatchString(t) || isTableRow(t) || t == "---" || t == "***"
}

var reHeading = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)

// heading renders # / ## / ### levels.
func mdHeading(level int, h string) []string {
	h = strings.TrimSpace(h)
	switch {
	case level == 1:
		p := stripMD(h)
		maxW := termWidth() - 4
		var lab string
		if strWidth(spacedCaps(p)) <= maxW {
			lab = AccentLabel(p)
		} else {
			lab = Accent(clipCols(strings.ToUpper(p), maxW))
		}
		return []string{"  " + lab, "  " + hair(strings.Repeat("─", min(escWidth(lab), maxW)))}
	case level == 2:
		return []string{"  " + Accent("■ ") + Bold(stripMD(h))}
	}
	return []string{"  " + Bold(stripMD(h))}
}
