package refs

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

const maxURLRead = 2 * 1024 * 1024 // bytes read from a page

// fetchURL gets raw (http or https only) and returns its host for the receipt,
// the page title and its readable text.
func fetchURL(ctx context.Context, raw string, opt Options) (host, title, text string, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw, "", "", errors.New("not a web address")
	}
	host = strings.TrimPrefix(u.Hostname(), "www.")
	if u.Scheme != "http" && u.Scheme != "https" {
		if host == "" {
			host = raw
		}
		return host, "", "", errors.New("only http and https addresses")
	}
	if u.Host == "" {
		return raw, "", "", errors.New("not a web address")
	}
	cl := opt.Client
	if cl == nil {
		cl = &http.Client{}
	}
	c := *cl
	c.Timeout = opt.Timeout
	c.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if r.URL.Scheme != "http" && r.URL.Scheme != "https" {
			return errors.New("redirect to a non-web address")
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, opt.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return host, "", "", err
	}
	req.Header.Set("User-Agent", "Blaze-CLI (+https://github.com/lacksonkapapahue/Blaze-cli)")
	req.Header.Set("Accept", "text/html,text/plain,application/json;q=0.9,*/*;q=0.5")
	resp, err := c.Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded) {
			return host, "", "", errors.New("timed out")
		}
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return host, "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return host, "", "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxURLRead))
	if err != nil {
		return host, "", "", errors.New("couldn't read the page")
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	switch {
	case strings.Contains(ct, "html") || (ct == "" && looksHTML(b)):
		title, text = Readable(string(b))
	case strings.HasPrefix(ct, "text/") || strings.Contains(ct, "json") || strings.Contains(ct, "xml") || ct == "":
		if binary(b) || !utf8.Valid(b) {
			return host, "", "", errors.New("not a text page")
		}
		text = string(b)
	default:
		return host, "", "", fmt.Errorf("not a text page (%s)", strings.SplitN(ct, ";", 2)[0])
	}
	if strings.TrimSpace(text) == "" {
		return host, title, "", errors.New("no readable text")
	}
	return host, title, text, nil
}

func looksHTML(b []byte) bool {
	s := strings.ToLower(string(b[:min(len(b), 512)]))
	return strings.Contains(s, "<html") || strings.Contains(s, "<!doctype html")
}

var (
	reTitle   = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reDrop    = regexp.MustCompile(`(?is)<(script|style|noscript|svg|template|iframe|head|nav|footer|form)\b[^>]*>.*?</(script|style|noscript|svg|template|iframe|head|nav|footer|form)\s*>`)
	reComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	reBlock   = regexp.MustCompile(`(?i)<\s*(br|/p|/div|/h[1-6]|/li|/tr|/section|/article|/header|/blockquote|/pre|hr)\b[^>]*>`)
	reItem    = regexp.MustCompile(`(?i)<\s*li\b[^>]*>`)
	reHead    = regexp.MustCompile(`(?i)<\s*h([1-6])\b[^>]*>`)
	reTag     = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpaces  = regexp.MustCompile(`[ \t\f\v\x{00a0}]+`)
	reBlank   = regexp.MustCompile(`\n{3,}`)
)

// Readable turns an HTML page into its title and plain readable text: scripts,
// styles, navigation and forms dropped, block ends as line breaks, headings and
// list items marked, entities decoded, whitespace collapsed.
func Readable(page string) (title, text string) {
	if m := reTitle.FindStringSubmatch(page); m != nil {
		title = strings.TrimSpace(reSpaces.ReplaceAllString(html.UnescapeString(reTag.ReplaceAllString(m[1], "")), " "))
	}
	s := reComment.ReplaceAllString(page, "")
	// a few passes so nested drops (a <nav> inside a <footer>) go too
	for i := 0; i < 3; i++ {
		n := reDrop.ReplaceAllString(s, " ")
		if n == s {
			break
		}
		s = n
	}
	s = reHead.ReplaceAllStringFunc(s, func(h string) string {
		lvl := reHead.FindStringSubmatch(h)[1]
		return "\n\n" + strings.Repeat("#", int(lvl[0]-'0')) + " "
	})
	s = reItem.ReplaceAllString(s, "\n- ")
	s = reBlock.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(reSpaces.ReplaceAllString(l, " "))
		if l == "-" {
			continue
		}
		out = append(out, l)
	}
	text = strings.TrimSpace(reBlank.ReplaceAllString(strings.Join(out, "\n"), "\n\n"))
	return title, text
}
