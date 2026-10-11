package connect

import (
	"fmt"
	"regexp"
	"strings"
)

// ---------------- GitHub ----------------

var githubAPI = "https://api.github.com"

var ghHdr = []string{"Accept", "application/vnd.github+json", "X-GitHub-Api-Version", "2022-11-28"}

func githubWho(c *call) (string, error) {
	var r struct{ Login string }
	if err := c.get(githubAPI+"/user", &r, ghHdr...); err != nil {
		return "", err
	}
	return "@" + r.Login, nil
}

var repoRe = regexp.MustCompile(`^[\w.-]+/[\w.-]+$`)

func repoArg(a args) (string, error) {
	r := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(a.s("repo"), "https://github.com/"), "github.com/"), ".git")
	if !repoRe.MatchString(r) {
		return "", fmt.Errorf("repo must be owner/name, got %q", a.s("repo"))
	}
	return r, nil
}

type ghIssue struct {
	Number      int
	Title       string
	State       string
	Body        string
	HtmlURL     string `json:"html_url"`
	User        struct{ Login string }
	Labels      []struct{ Name string }
	Comments    int
	UpdatedAt   string    `json:"updated_at"`
	PullRequest *struct{} `json:"pull_request"`
	RepoURL     string    `json:"repository_url"`
}

func githubTools() []*Tool {
	issueLine := func(i ghIssue) string {
		var labels []string
		for _, l := range i.Labels {
			labels = append(labels, l.Name)
		}
		s := fmt.Sprintf("#%d %s · @%s · updated %s", i.Number, i.Title, i.User.Login, clip(i.UpdatedAt, 10))
		if len(labels) > 0 {
			s += " · " + strings.Join(labels, ", ")
		}
		return s
	}
	return []*Tool{
		{Name: "github_my_repos", Group: "github", Risk: Read,
			Desc:  "List the user's own GitHub repositories (private ones included), most recently pushed first. Use this, not search, for 'my repos'.",
			Props: map[string]any{"limit": num("How many (default 30, up to 100)")},
			Run: func(c *call) (string, error) {
				var l []struct {
					FullName    string `json:"full_name"`
					Private     bool
					Description string
					PushedAt    string `json:"pushed_at"`
				}
				if err := c.get(githubAPI+"/user/repos?sort=pushed&affiliation=owner,collaborator,organization_member&per_page="+fmt.Sprint(c.a.i("limit", 30, 1, 100)), &l, ghHdr...); err != nil {
					return "", err
				}
				if len(l) == 0 {
					return "No repositories on this account.", nil
				}
				var b strings.Builder
				for _, r := range l {
					fmt.Fprintf(&b, "%s%s · pushed %s", r.FullName, map[bool]string{true: " · private"}[r.Private], clip(r.PushedAt, 10))
					if r.Description != "" {
						b.WriteString(" · " + clip(r.Description, 70))
					}
					b.WriteString("\n")
				}
				return b.String(), nil
			}},
		{Name: "github_search_repos", Group: "github", Risk: Read,
			Desc:  "Search public GitHub repositories.",
			Props: map[string]any{"query": str("GitHub search query, e.g. 'wails language:go stars:>100'")}, Required: []string{"query"},
			Run: func(c *call) (string, error) {
				var r struct {
					Items []struct {
						FullName    string `json:"full_name"`
						Stars       int    `json:"stargazers_count"`
						Description string
					}
				}
				if err := c.get(githubAPI+"/search/repositories?per_page=10&q="+q(c.a.s("query")), &r, ghHdr...); err != nil {
					return "", err
				}
				if len(r.Items) == 0 {
					return "Nothing found.", nil
				}
				var b strings.Builder
				for _, x := range r.Items {
					fmt.Fprintf(&b, "%s ★%d · %s\n", x.FullName, x.Stars, clip(x.Description, 90))
				}
				return b.String(), nil
			}},
		{Name: "github_issues", Group: "github", Risk: Read,
			Desc:  "List issues in a repository (pull requests excluded).",
			Props: map[string]any{"repo": str("owner/name"), "state": enum("open (default), closed or all", "open", "closed", "all"), "labels": str("Optional comma-separated labels")}, Required: []string{"repo"},
			Run: func(c *call) (string, error) {
				repo, err := repoArg(c.a)
				if err != nil {
					return "", err
				}
				st := c.a.s("state")
				if st == "" {
					st = "open"
				}
				u := githubAPI + "/repos/" + repo + "/issues?per_page=30&state=" + q(st)
				if l := c.a.s("labels"); l != "" {
					u += "&labels=" + q(l)
				}
				var l []ghIssue
				if err := c.get(u, &l, ghHdr...); err != nil {
					return "", err
				}
				var b strings.Builder
				for _, i := range l {
					if i.PullRequest == nil {
						b.WriteString(issueLine(i) + "\n")
					}
				}
				if b.Len() == 0 {
					return "No " + st + " issues in " + repo + ".", nil
				}
				return b.String(), nil
			}},
		{Name: "github_read", Group: "github", Risk: Read,
			Desc:  "Read an issue or pull request with its latest comments (and, for a PR, its state and changed files).",
			Props: map[string]any{"repo": str("owner/name"), "number": num("Issue or PR number")}, Required: []string{"repo", "number"},
			Run: func(c *call) (string, error) {
				repo, err := repoArg(c.a)
				if err != nil {
					return "", err
				}
				n := fmt.Sprint(c.a.i("number", 0, 0, 1<<30))
				var i ghIssue
				if err := c.get(githubAPI+"/repos/"+repo+"/issues/"+n, &i, ghHdr...); err != nil {
					return "", err
				}
				var b strings.Builder
				kind := "Issue"
				if i.PullRequest != nil {
					kind = "Pull request"
				}
				fmt.Fprintf(&b, "%s #%d: %s (%s) by @%s\n%s\n\n%s\n", kind, i.Number, i.Title, i.State, i.User.Login, i.HtmlURL, clip(i.Body, 6000))
				if i.PullRequest != nil {
					var p struct {
						Merged, Draft        bool
						MergeableState       string `json:"mergeable_state"`
						Head, Base           struct{ Ref string }
						Additions, Deletions int
						ChangedFiles         int `json:"changed_files"`
					}
					if c.get(githubAPI+"/repos/"+repo+"/pulls/"+n, &p, ghHdr...) == nil {
						fmt.Fprintf(&b, "\n%s → %s · merged: %v · draft: %v · %s · +%d −%d in %d files\n", p.Head.Ref, p.Base.Ref, p.Merged, p.Draft, p.MergeableState, p.Additions, p.Deletions, p.ChangedFiles)
					}
					var files []struct {
						Filename             string
						Status               string
						Additions, Deletions int
					}
					if c.get(githubAPI+"/repos/"+repo+"/pulls/"+n+"/files?per_page=40", &files, ghHdr...) == nil {
						for _, f := range files {
							fmt.Fprintf(&b, "  %s %s +%d −%d\n", f.Status, f.Filename, f.Additions, f.Deletions)
						}
					}
				}
				if i.Comments > 0 {
					var cm []struct {
						User      struct{ Login string }
						Body      string
						CreatedAt string `json:"created_at"`
					}
					since := i.Comments - 10
					if since < 0 {
						since = 0
					}
					page := since/30 + 1
					_ = c.get(fmt.Sprintf("%s/repos/%s/issues/%s/comments?per_page=30&page=%d", githubAPI, repo, n, page), &cm, ghHdr...)
					if len(cm) > 10 {
						cm = cm[len(cm)-10:]
					}
					fmt.Fprintf(&b, "\n%d comments, latest:\n", i.Comments)
					for _, x := range cm {
						fmt.Fprintf(&b, "— @%s %s: %s\n", x.User.Login, clip(x.CreatedAt, 10), clip(oneLine(x.Body), 600))
					}
				}
				return b.String(), nil
			}},
		{Name: "github_my_prs", Group: "github", Risk: Read,
			Desc: "List the user's open pull requests and the ones waiting for their review.",
			Run: func(c *call) (string, error) {
				var me struct{ Login string }
				if err := c.get(githubAPI+"/user", &me, ghHdr...); err != nil {
					return "", err
				}
				list := func(qs string) (string, error) {
					var r struct{ Items []ghIssue }
					if err := c.get(githubAPI+"/search/issues?per_page=20&q="+q(qs), &r, ghHdr...); err != nil {
						return "", err
					}
					var b strings.Builder
					for _, i := range r.Items {
						fmt.Fprintf(&b, "  %s #%d %s\n", strings.TrimPrefix(i.RepoURL, githubAPI+"/repos/"), i.Number, i.Title)
					}
					if b.Len() == 0 {
						return "  none\n", nil
					}
					return b.String(), nil
				}
				mine, err := list("is:pr is:open author:" + me.Login)
				if err != nil {
					return "", err
				}
				rev, err := list("is:pr is:open review-requested:" + me.Login)
				if err != nil {
					return "", err
				}
				return "Your open pull requests:\n" + mine + "Waiting for your review:\n" + rev, nil
			}},
		{Name: "github_notifications", Group: "github", Risk: Read,
			Desc: "List the user's unread GitHub notifications.",
			Run: func(c *call) (string, error) {
				var l []struct {
					Reason     string
					Repository struct {
						FullName string `json:"full_name"`
					}
					Subject struct{ Title, Type string }
				}
				if err := c.get(githubAPI+"/notifications?per_page=30", &l, ghHdr...); err != nil {
					return "", err
				}
				if len(l) == 0 {
					return "No unread notifications.", nil
				}
				var b strings.Builder
				for _, n := range l {
					fmt.Fprintf(&b, "%s · %s · %s: %s\n", n.Repository.FullName, n.Reason, n.Subject.Type, n.Subject.Title)
				}
				return b.String(), nil
			}},
		{Name: "github_create_issue", Group: "github", Risk: Write,
			Desc:  "Open an issue in a repository. The user approves it first.",
			Props: map[string]any{"repo": str("owner/name"), "title": str("Title"), "body": str("Markdown body")}, Required: []string{"repo", "title"},
			Ask: func(a args) (string, string) {
				return "Open an issue in " + a.s("repo") + "?", a.s("title") + "\n\n" + clip(a.s("body"), 900)
			},
			Run: func(c *call) (string, error) {
				repo, err := repoArg(c.a)
				if err != nil {
					return "", err
				}
				var r ghIssue
				if err := c.json("POST", githubAPI+"/repos/"+repo+"/issues", map[string]any{"title": c.a.s("title"), "body": c.a.s("body")}, &r, ghHdr...); err != nil {
					return "", err
				}
				return fmt.Sprintf("Opened issue #%d: %s", r.Number, r.HtmlURL), nil
			}},
		{Name: "github_comment", Group: "github", Risk: Write,
			Desc:  "Comment on an issue or pull request. The user approves it first.",
			Props: map[string]any{"repo": str("owner/name"), "number": num("Issue or PR number"), "body": str("Markdown comment")}, Required: []string{"repo", "number", "body"},
			Ask: func(a args) (string, string) {
				return fmt.Sprintf("Comment on %s #%s?", a.s("repo"), a.s("number")), clip(a.s("body"), 900)
			},
			Run: func(c *call) (string, error) {
				repo, err := repoArg(c.a)
				if err != nil {
					return "", err
				}
				var r struct {
					HtmlURL string `json:"html_url"`
				}
				if err := c.json("POST", fmt.Sprintf("%s/repos/%s/issues/%d/comments", githubAPI, repo, c.a.i("number", 0, 0, 1<<30)), map[string]any{"body": c.a.s("body")}, &r, ghHdr...); err != nil {
					return "", err
				}
				return "Commented: " + r.HtmlURL, nil
			}},
	}
}

// ---------------- Spotify ----------------

var spotifyAPI = "https://api.spotify.com/v1"

func spotifyWho(c *call) (string, error) {
	var r struct {
		DisplayName string `json:"display_name"`
		ID          string
	}
	if err := c.get(spotifyAPI+"/me", &r); err != nil {
		return "", err
	}
	if r.DisplayName != "" {
		return r.DisplayName, nil
	}
	return r.ID, nil
}

func spotifyErr(st int, body []byte) error {
	if st == 404 {
		return fmt.Errorf("Spotify has no active device. Open Spotify on a computer or phone, play anything for a second, then try again")
	}
	if st == 403 {
		return fmt.Errorf("Spotify refused playback control (it needs Spotify Premium): %s", clip(string(body), 160))
	}
	return &APIError{st, string(body)}
}

func spotifyTools() []*Tool {
	type item struct {
		Name    string
		URI     string
		Artists []struct{ Name string }
		Owner   struct {
			DisplayName string `json:"display_name"`
		}
	}
	line := func(it item) string {
		s := it.Name
		if len(it.Artists) > 0 {
			s += " — " + it.Artists[0].Name
		} else if it.Owner.DisplayName != "" {
			s += " — by " + it.Owner.DisplayName
		}
		return s
	}
	search := func(c *call, query, typ string, n int) ([]item, error) {
		if typ == "" {
			typ = "track"
		}
		var r map[string]struct{ Items []item }
		if err := c.get(fmt.Sprintf("%s/search?limit=%d&type=%s&q=%s", spotifyAPI, n, q(typ), q(query)), &r); err != nil {
			return nil, err
		}
		return r[typ+"s"].Items, nil
	}
	kinds := enum("track (default), album, playlist or artist", "track", "album", "playlist", "artist")
	return []*Tool{
		{Name: "spotify_now_playing", Group: "spotify", Risk: Read,
			Desc: "What's playing on the user's Spotify right now, and on which device.",
			Run: func(c *call) (string, error) {
				st, data, err := c.do("GET", spotifyAPI+"/me/player", nil, nil)
				if err != nil {
					return "", err
				}
				if st == 204 || len(strings.TrimSpace(string(data))) == 0 {
					return "Nothing is playing on Spotify.", nil
				}
				if st > 299 {
					return "", &APIError{st, string(data)}
				}
				var r struct {
					IsPlaying bool `json:"is_playing"`
					Item      item
					Device    struct{ Name string }
				}
				if err := jsonUnmarshal(data, &r); err != nil {
					return "", err
				}
				if r.Item.Name == "" {
					return "Nothing is playing on Spotify.", nil
				}
				s := line(r.Item) + " on " + r.Device.Name
				if !r.IsPlaying {
					s += " (paused)"
				}
				return s, nil
			}},
		{Name: "spotify_search", Group: "spotify", Risk: Read,
			Desc:  "Search Spotify for tracks, albums, playlists or artists.",
			Props: map[string]any{"query": str("What to look for"), "type": kinds}, Required: []string{"query"},
			Run: func(c *call) (string, error) {
				l, err := search(c, c.a.s("query"), c.a.s("type"), 10)
				if err != nil {
					return "", err
				}
				if len(l) == 0 {
					return "Nothing found on Spotify.", nil
				}
				var b strings.Builder
				for _, it := range l {
					b.WriteString(line(it) + " · " + it.URI + "\n")
				}
				return b.String(), nil
			}},
		{Name: "spotify_play", Group: "spotify", Risk: Action,
			Desc:  "Play on the user's active Spotify device: the top search result for a query, a spotify: URI, or resume when empty.",
			Props: map[string]any{"query": str("What to play, or a spotify: URI; empty resumes"), "type": kinds},
			Ask: func(a args) (string, string) {
				if a.s("query") == "" {
					return "Resume Spotify?", "Resume playback on your active device."
				}
				return "Play on Spotify?", a.s("query")
			},
			Run: func(c *call) (string, error) {
				qy, typ := c.a.s("query"), c.a.s("type")
				body := map[string]any{}
				what := "Resumed."
				if strings.HasPrefix(qy, "spotify:") {
					if strings.HasPrefix(qy, "spotify:track:") {
						body["uris"] = []string{qy}
					} else {
						body["context_uri"] = qy
					}
					what = "Playing " + qy + "."
				} else if qy != "" {
					l, err := search(c, qy, typ, 1)
					if err != nil {
						return "", err
					}
					if len(l) == 0 {
						return "Couldn't find \"" + qy + "\" on Spotify.", nil
					}
					if typ == "" || typ == "track" {
						body["uris"] = []string{l[0].URI}
					} else {
						body["context_uri"] = l[0].URI
					}
					what = "Playing " + line(l[0]) + "."
				}
				st, data, err := c.do("PUT", spotifyAPI+"/me/player/play", body, nil)
				if err != nil {
					return "", err
				}
				if st > 299 {
					return "", spotifyErr(st, data)
				}
				return what, nil
			}},
		{Name: "spotify_control", Group: "spotify", Risk: Action,
			Desc:  "Pause, skip to next, or go back to previous on Spotify.",
			Props: map[string]any{"action": enum("What to do", "pause", "next", "previous")}, Required: []string{"action"},
			Ask: func(a args) (string, string) { return "Spotify: " + a.s("action") + "?", "On your active device." },
			Run: func(c *call) (string, error) {
				m, p := "POST", "next"
				switch c.a.s("action") {
				case "pause":
					m, p = "PUT", "pause"
				case "previous":
					p = "previous"
				case "next":
				default:
					return "", fmt.Errorf("action must be pause, next or previous")
				}
				st, data, err := c.do(m, spotifyAPI+"/me/player/"+p, "", nil)
				if err != nil {
					return "", err
				}
				if st > 299 {
					return "", spotifyErr(st, data)
				}
				return "Done: " + c.a.s("action") + ".", nil
			}},
	}
}

// ---------------- Notion ----------------

var notionAPI = "https://api.notion.com/v1"

var notionHdr = []string{"Notion-Version", "2022-06-28"}

func notionWho(c *call) (string, error) {
	var r struct {
		Name string
		Bot  struct {
			WorkspaceName string `json:"workspace_name"`
		}
	}
	if err := c.get(notionAPI+"/users/me", &r, notionHdr...); err != nil {
		return "", err
	}
	if r.Bot.WorkspaceName != "" {
		return r.Name + " · " + r.Bot.WorkspaceName, nil
	}
	return r.Name, nil
}

var notionIDRe = regexp.MustCompile(`([0-9a-f]{32}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)

type nRich []struct {
	PlainText string `json:"plain_text"`
}

func (r nRich) text() string {
	var b strings.Builder
	for _, x := range r {
		b.WriteString(x.PlainText)
	}
	return b.String()
}

type nPage struct {
	ID         string
	URL        string
	Object     string
	Properties map[string]struct {
		Type  string
		Title nRich
	}
	Title nRich // databases
}

func (p nPage) title() string {
	if t := p.Title.text(); t != "" {
		return t
	}
	for _, v := range p.Properties {
		if v.Type == "title" {
			if t := v.Title.text(); t != "" {
				return t
			}
		}
	}
	return "Untitled"
}

// notionPage finds a page by id, URL or title.
func notionPage(c *call, ref string) (string, error) {
	if m := notionIDRe.FindString(strings.ToLower(ref)); m != "" {
		return m, nil
	}
	var r struct{ Results []nPage }
	if err := c.json("POST", notionAPI+"/search", map[string]any{"query": ref, "page_size": 5, "filter": map[string]any{"property": "object", "value": "page"}}, &r, notionHdr...); err != nil {
		return "", err
	}
	for _, p := range r.Results {
		if strings.EqualFold(p.title(), ref) {
			return p.ID, nil
		}
	}
	if len(r.Results) > 0 {
		return r.Results[0].ID, nil
	}
	return "", fmt.Errorf("no Notion page called %q is shared with your integration (Notion › ••• › Connections)", ref)
}

func notionParas(text string) []map[string]any {
	var out []map[string]any
	for _, p := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		p = strings.TrimSpace(p)
		for p != "" && len(out) < 90 {
			chunk := p
			if r := []rune(p); len(r) > 1900 {
				chunk, p = string(r[:1900]), string(r[1900:])
			} else {
				p = ""
			}
			typ, body := "paragraph", chunk
			switch {
			case strings.HasPrefix(chunk, "# "):
				typ, body = "heading_2", chunk[2:]
			case strings.HasPrefix(chunk, "- ") && !strings.Contains(chunk, "\n"):
				typ, body = "bulleted_list_item", chunk[2:]
			}
			out = append(out, map[string]any{"object": "block", "type": typ, typ: map[string]any{"rich_text": []any{map[string]any{"type": "text", "text": map[string]any{"content": body}}}}})
		}
	}
	return out
}

func notionTools() []*Tool {
	return []*Tool{
		{Name: "notion_search", Group: "notion", Risk: Read,
			Desc:  "Search the Notion pages and databases shared with the user's integration.",
			Props: map[string]any{"query": str("Words to look for")}, Required: []string{"query"},
			Run: func(c *call) (string, error) {
				var r struct{ Results []nPage }
				if err := c.json("POST", notionAPI+"/search", map[string]any{"query": c.a.s("query"), "page_size": 15}, &r, notionHdr...); err != nil {
					return "", err
				}
				if len(r.Results) == 0 {
					return "Nothing in Notion matched. Pages must be shared with the integration (••• › Connections).", nil
				}
				var b strings.Builder
				for _, p := range r.Results {
					fmt.Fprintf(&b, "%s · %s · %s\n", p.title(), p.Object, p.URL)
				}
				return b.String(), nil
			}},
		{Name: "notion_read", Group: "notion", Risk: Read,
			Desc:  "Read a Notion page's text by id, URL or title.",
			Props: map[string]any{"page": str("Page id, URL or exact title")}, Required: []string{"page"},
			Run: func(c *call) (string, error) {
				id, err := notionPage(c, c.a.s("page"))
				if err != nil {
					return "", err
				}
				var p nPage
				if err := c.get(notionAPI+"/pages/"+id, &p, notionHdr...); err != nil {
					return "", err
				}
				var b strings.Builder
				b.WriteString(p.title() + "\n" + p.URL + "\n\n")
				cursor := ""
				for i := 0; i < 5; i++ {
					u := notionAPI + "/blocks/" + id + "/children?page_size=100"
					if cursor != "" {
						u += "&start_cursor=" + q(cursor)
					}
					var r struct {
						Results    []map[string]any
						HasMore    bool   `json:"has_more"`
						NextCursor string `json:"next_cursor"`
					}
					if err := c.get(u, &r, notionHdr...); err != nil {
						return "", err
					}
					for _, blk := range r.Results {
						typ, _ := blk["type"].(string)
						body, _ := blk[typ].(map[string]any)
						rt, _ := body["rich_text"].([]any)
						var line strings.Builder
						for _, x := range rt {
							if m, ok := x.(map[string]any); ok {
								s, _ := m["plain_text"].(string)
								line.WriteString(s)
							}
						}
						t := line.String()
						switch typ {
						case "heading_1", "heading_2", "heading_3":
							t = "## " + t
						case "bulleted_list_item", "numbered_list_item":
							t = "- " + t
						case "to_do":
							chk, _ := body["checked"].(bool)
							t = map[bool]string{true: "[x] ", false: "[ ] "}[chk] + t
						case "child_page":
							tt, _ := body["title"].(string)
							t = "[page] " + tt
						}
						if strings.TrimSpace(t) != "" {
							b.WriteString(t + "\n")
						}
					}
					if !r.HasMore {
						break
					}
					cursor = r.NextCursor
				}
				return clip(b.String(), 20000), nil
			}},
		{Name: "notion_append", Group: "notion", Risk: Write,
			Desc:  "Add text to the end of a Notion page (blank lines start new paragraphs; '# ' heading, '- ' bullet).",
			Props: map[string]any{"page": str("Page id, URL or exact title"), "text": str("Text to add")}, Required: []string{"page", "text"},
			Ask: func(a args) (string, string) {
				return "Add to the Notion page " + a.s("page") + "?", clip(a.s("text"), 900)
			},
			Run: func(c *call) (string, error) {
				id, err := notionPage(c, c.a.s("page"))
				if err != nil {
					return "", err
				}
				if err := c.json("PATCH", notionAPI+"/blocks/"+id+"/children", map[string]any{"children": notionParas(c.a.s("text"))}, nil, notionHdr...); err != nil {
					return "", err
				}
				return "Added the text to the Notion page.", nil
			}},
		{Name: "notion_create_page", Group: "notion", Risk: Write,
			Desc:  "Create a Notion page under a parent page.",
			Props: map[string]any{"parent": str("Parent page id, URL or exact title"), "title": str("New page title"), "content": str("Optional text")}, Required: []string{"parent", "title"},
			Ask: func(a args) (string, string) {
				return "Create a Notion page?", a.s("title") + " (in " + a.s("parent") + ")\n\n" + clip(a.s("content"), 700)
			},
			Run: func(c *call) (string, error) {
				pid, err := notionPage(c, c.a.s("parent"))
				if err != nil {
					return "", err
				}
				body := map[string]any{
					"parent":     map[string]any{"page_id": pid},
					"properties": map[string]any{"title": map[string]any{"title": []any{map[string]any{"text": map[string]any{"content": c.a.s("title")}}}}},
				}
				if t := c.a.s("content"); t != "" {
					body["children"] = notionParas(t)
				}
				var r struct{ URL string }
				if err := c.json("POST", notionAPI+"/pages", body, &r, notionHdr...); err != nil {
					return "", err
				}
				return "Created \"" + c.a.s("title") + "\": " + r.URL, nil
			}},
	}
}

// ---------------- Slack ----------------

var slackAPI = "https://slack.com/api"

type slackResp struct {
	OK    bool
	Error string
}

func slackWho(c *call) (string, error) {
	var r struct {
		slackResp
		User, Team string
	}
	if err := c.get(slackAPI+"/auth.test", &r); err != nil {
		return "", err
	}
	if !r.OK {
		return "", fmt.Errorf("Slack: %s", r.Error)
	}
	return r.User + " · " + r.Team, nil
}

type slackChan struct {
	ID, Name string
	IsMember bool `json:"is_member"`
}

func slackChannels(c *call) ([]slackChan, error) {
	var all []slackChan
	cursor := ""
	for i := 0; i < 5; i++ {
		var r struct {
			slackResp
			Channels []slackChan
			Meta     struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		u := slackAPI + "/conversations.list?limit=200&exclude_archived=true&types=public_channel,private_channel"
		if cursor != "" {
			u += "&cursor=" + q(cursor)
		}
		if err := c.get(u, &r); err != nil {
			return nil, err
		}
		if !r.OK {
			return nil, fmt.Errorf("Slack: %s", r.Error)
		}
		all = append(all, r.Channels...)
		if cursor = r.Meta.NextCursor; cursor == "" {
			break
		}
	}
	return all, nil
}

var slackIDRe = regexp.MustCompile(`^[CGD][A-Z0-9]{6,}$`)

func slackChanID(c *call, ch string) (string, error) {
	ch = strings.TrimPrefix(strings.TrimSpace(ch), "#")
	if slackIDRe.MatchString(ch) {
		return ch, nil
	}
	l, err := slackChannels(c)
	if err != nil {
		return "", err
	}
	for _, x := range l {
		if strings.EqualFold(x.Name, ch) {
			return x.ID, nil
		}
	}
	return "", fmt.Errorf("no Slack channel called #%s that you can see", ch)
}

func slackTools() []*Tool {
	return []*Tool{
		{Name: "slack_channels", Group: "slack", Risk: Read,
			Desc: "List the Slack channels the user is in.",
			Run: func(c *call) (string, error) {
				l, err := slackChannels(c)
				if err != nil {
					return "", err
				}
				var names []string
				for _, x := range l {
					if x.IsMember {
						names = append(names, "#"+x.Name)
					}
				}
				if len(names) == 0 {
					return "You're not in any channels Blaze can see.", nil
				}
				return strings.Join(names, ", "), nil
			}},
		{Name: "slack_read", Group: "slack", Risk: Read,
			Desc:  "Read the latest messages in a Slack channel, oldest first.",
			Props: map[string]any{"channel": str("Channel name or id"), "limit": num("How many (default 20, up to 100)")}, Required: []string{"channel"},
			Run: func(c *call) (string, error) {
				id, err := slackChanID(c, c.a.s("channel"))
				if err != nil {
					return "", err
				}
				var r struct {
					slackResp
					Messages []struct{ User, Text, Ts string }
				}
				if err := c.get(fmt.Sprintf("%s/conversations.history?limit=%d&channel=%s", slackAPI, c.a.i("limit", 20, 1, 100), id), &r); err != nil {
					return "", err
				}
				if !r.OK {
					return "", fmt.Errorf("Slack: %s", r.Error)
				}
				names := map[string]string{}
				name := func(u string) string {
					if u == "" {
						return "bot"
					}
					if n, ok := names[u]; ok {
						return n
					}
					var ur struct {
						slackResp
						User struct {
							Name    string
							Profile struct {
								DisplayName string `json:"display_name"`
								RealName    string `json:"real_name"`
							}
						}
					}
					n := u
					if len(names) < 25 && c.get(slackAPI+"/users.info?user="+q(u), &ur) == nil && ur.OK {
						for _, v := range []string{ur.User.Profile.DisplayName, ur.User.Profile.RealName, ur.User.Name} {
							if v != "" {
								n = v
								break
							}
						}
					}
					names[u] = n
					return n
				}
				var b strings.Builder
				for i := len(r.Messages) - 1; i >= 0; i-- {
					m := r.Messages[i]
					fmt.Fprintf(&b, "%s: %s\n", name(m.User), clip(m.Text, 500))
				}
				if b.Len() == 0 {
					return "No messages.", nil
				}
				return b.String(), nil
			}},
		{Name: "slack_post", Group: "slack", Risk: Write,
			Desc:  "Post a message to a Slack channel as the user. The user approves it first.",
			Props: map[string]any{"channel": str("Channel name or id"), "text": str("Message (Slack markdown)"), "thread_ts": str("Optional: reply in this thread")}, Required: []string{"channel", "text"},
			Ask: func(a args) (string, string) {
				return "Post in Slack #" + strings.TrimPrefix(a.s("channel"), "#") + "?", clip(a.s("text"), 900)
			},
			Run: func(c *call) (string, error) {
				id, err := slackChanID(c, c.a.s("channel"))
				if err != nil {
					return "", err
				}
				body := map[string]any{"channel": id, "text": c.a.s("text")}
				if t := c.a.s("thread_ts"); t != "" {
					body["thread_ts"] = t
				}
				var r slackResp
				if err := c.json("POST", slackAPI+"/chat.postMessage", body, &r, "Content-Type", "application/json; charset=utf-8"); err != nil {
					return "", err
				}
				if !r.OK {
					return "", fmt.Errorf("Slack: %s", r.Error)
				}
				return "Posted in #" + strings.TrimPrefix(c.a.s("channel"), "#") + ".", nil
			}},
	}
}
