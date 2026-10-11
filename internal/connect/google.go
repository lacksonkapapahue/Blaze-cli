package connect

import (
	"encoding/base64"
	"fmt"
	"mime"
	"regexp"
	"strings"
	"time"
)

var (
	gmailAPI  = "https://gmail.googleapis.com/gmail/v1/users/me"
	calAPI    = "https://www.googleapis.com/calendar/v3/calendars/primary"
	driveAPI  = "https://www.googleapis.com/drive/v3"
	peopleAPI = "https://people.googleapis.com/v1"
	googleUI  = "https://openidconnect.googleapis.com/v1/userinfo"
)

func googleWho(c *call) (string, error) {
	var r struct {
		Email string `json:"email"`
	}
	if err := c.get(googleUI, &r); err != nil {
		return "", err
	}
	return r.Email, nil
}

type gHeader struct{ Name, Value string }
type gPart struct {
	MimeType string    `json:"mimeType"`
	Headers  []gHeader `json:"headers"`
	Body     struct {
		Data string `json:"data"`
	} `json:"body"`
	Parts []gPart `json:"parts"`
}
type gMessage struct {
	ID       string `json:"id"`
	ThreadID string `json:"threadId"`
	Snippet  string `json:"snippet"`
	Payload  gPart  `json:"payload"`
}

func (m gMessage) header(n string) string {
	for _, h := range m.Payload.Headers {
		if strings.EqualFold(h.Name, n) {
			return h.Value
		}
	}
	return ""
}

func b64url(s string) string {
	s = strings.TrimRight(s, "=")
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		b, _ = base64.RawStdEncoding.DecodeString(s)
	}
	return string(b)
}

var tagRe = regexp.MustCompile(`(?s)<style.*?</style>|<script.*?</script>|<[^>]+>`)
var blankRe = regexp.MustCompile(`\n\s*\n\s*\n+`)

func htmlText(s string) string {
	s = strings.NewReplacer("<br>", "\n", "<br/>", "\n", "<br />", "\n", "</p>", "\n\n", "</div>", "\n", "</tr>", "\n").Replace(s)
	s = tagRe.ReplaceAllString(s, "")
	s = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'").Replace(s)
	return strings.TrimSpace(blankRe.ReplaceAllString(s, "\n\n"))
}

func partText(p gPart, mt string) string {
	if p.MimeType == mt && p.Body.Data != "" {
		return b64url(p.Body.Data)
	}
	for _, c := range p.Parts {
		if t := partText(c, mt); t != "" {
			return t
		}
	}
	return ""
}

// rfc822 builds a plain-text message the Gmail API accepts as raw.
func rfc822(to, cc, subject, body string) string {
	var b strings.Builder
	b.WriteString("To: " + oneLine(to) + "\r\n")
	if cc != "" {
		b.WriteString("Cc: " + oneLine(cc) + "\r\n")
	}
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", oneLine(subject)) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	return base64.RawURLEncoding.EncodeToString([]byte(b.String()))
}

func mailAsk(verb string) func(a args) (string, string) {
	return func(a args) (string, string) {
		d := "To: " + a.s("to")
		if a.s("cc") != "" {
			d += "\nCc: " + a.s("cc")
		}
		return verb, d + "\nSubject: " + a.s("subject") + "\n\n" + clip(a.s("body"), 900)
	}
}

// parseWhen reads "2026-10-12 14:30", "2026-10-12T14:30", RFC 3339 or a date (all day).
func parseWhen(s string) (t time.Time, allDay bool, err error) {
	s = strings.TrimSpace(s)
	if t, err = time.Parse(time.RFC3339, s); err == nil {
		return t, false, nil
	}
	for _, f := range []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err = time.ParseInLocation(f, s, time.Local); err == nil {
			return t, false, nil
		}
	}
	if t, err = time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		return t, true, nil
	}
	return time.Time{}, false, fmt.Errorf("couldn't read the time %q: use YYYY-MM-DD HH:MM (local) or YYYY-MM-DD for all day", s)
}

func googleTools() []*Tool {
	gm := gScope + "gmail.readonly"
	return []*Tool{
		{Name: "gmail_search", Group: "gmail", Risk: Read, Scopes: []string{gm},
			Desc:  "Search the user's Gmail with Gmail search syntax (e.g. 'from:bank newer_than:7d is:unread'). Returns ids, dates, senders, subjects and snippets.",
			Props: map[string]any{"query": str("Gmail search query"), "max": num("Max results (default 10, up to 25)")}, Required: []string{"query"},
			Run: func(c *call) (string, error) {
				var l struct {
					Messages []struct{ ID string } `json:"messages"`
				}
				if err := c.get(gmailAPI+"/messages?maxResults="+fmt.Sprint(c.a.i("max", 10, 1, 25))+"&q="+q(c.a.s("query")), &l); err != nil {
					return "", err
				}
				if len(l.Messages) == 0 {
					return "No emails matched.", nil
				}
				var b strings.Builder
				for _, x := range l.Messages {
					var m gMessage
					if err := c.get(gmailAPI+"/messages/"+x.ID+"?format=metadata&metadataHeaders=From&metadataHeaders=Subject&metadataHeaders=Date", &m); err != nil {
						return "", err
					}
					fmt.Fprintf(&b, "[%s] %s · %s · %s\n   %s\n", m.ID, clip(m.header("Date"), 31), clip(m.header("From"), 60), clip(m.header("Subject"), 100), clip(htmlText(m.Snippet), 180))
				}
				return b.String(), nil
			}},
		{Name: "gmail_read", Group: "gmail", Risk: Read, Scopes: []string{gm},
			Desc:  "Read one Gmail message by id (from gmail_search): headers and the text body.",
			Props: map[string]any{"id": str("Message id")}, Required: []string{"id"},
			Run: func(c *call) (string, error) {
				var m gMessage
				if err := c.get(gmailAPI+"/messages/"+q(c.a.s("id"))+"?format=full", &m); err != nil {
					return "", err
				}
				body := partText(m.Payload, "text/plain")
				if strings.TrimSpace(body) == "" {
					body = htmlText(partText(m.Payload, "text/html"))
				}
				if strings.TrimSpace(body) == "" {
					body = m.Snippet
				}
				return fmt.Sprintf("From: %s\nTo: %s\nDate: %s\nSubject: %s\nThread: %s\n\n%s", m.header("From"), m.header("To"), m.header("Date"), m.header("Subject"), m.ThreadID, clip(body, 12000)), nil
			}},
		{Name: "gmail_draft", Group: "gmail", Risk: Action, Scopes: []string{gScope + "gmail.compose"},
			Desc:  "Save an email as a draft in the user's Gmail (not sent). Prefer this when the user wants to review before sending.",
			Props: map[string]any{"to": str("Recipient address(es), comma separated"), "cc": str("Optional Cc"), "subject": str("Subject"), "body": str("Plain-text body")}, Required: []string{"to", "subject", "body"},
			Ask: mailAsk("Save this Gmail draft?"),
			Run: func(c *call) (string, error) {
				var r struct {
					ID string `json:"id"`
				}
				if err := c.json("POST", gmailAPI+"/drafts", map[string]any{"message": map[string]any{"raw": rfc822(c.a.s("to"), c.a.s("cc"), c.a.s("subject"), c.a.s("body"))}}, &r); err != nil {
					return "", err
				}
				return "Saved a draft to " + c.a.s("to") + " in Gmail (draft " + r.ID + "). It was not sent.", nil
			}},
		{Name: "gmail_send", Group: "gmail", Risk: Write, Scopes: []string{gScope + "gmail.send"},
			Desc:  "Send an email from the user's Gmail. The user approves it first.",
			Props: map[string]any{"to": str("Recipient address(es), comma separated"), "cc": str("Optional Cc"), "subject": str("Subject"), "body": str("Plain-text body")}, Required: []string{"to", "subject", "body"},
			Ask: mailAsk("Send this email from Gmail?"),
			Run: func(c *call) (string, error) {
				var r struct {
					ID string `json:"id"`
				}
				if err := c.json("POST", gmailAPI+"/messages/send", map[string]any{"raw": rfc822(c.a.s("to"), c.a.s("cc"), c.a.s("subject"), c.a.s("body"))}, &r); err != nil {
					return "", err
				}
				return "Email sent to " + c.a.s("to") + " (message " + r.ID + ").", nil
			}},
		{Name: "gcal_events", Group: "calendar", Risk: Read, Scopes: []string{gScope + "calendar.events.readonly"},
			Desc:  "List events on the user's primary Google Calendar from today for the next N days, optionally matching words.",
			Props: map[string]any{"days": num("Days ahead (default 7, up to 60)"), "query": str("Optional words to match")},
			Run: func(c *call) (string, error) {
				now := time.Now()
				start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
				end := start.AddDate(0, 0, c.a.i("days", 7, 1, 60))
				u := calAPI + "/events?singleEvents=true&orderBy=startTime&maxResults=60&timeMin=" + q(start.Format(time.RFC3339)) + "&timeMax=" + q(end.Format(time.RFC3339))
				if s := c.a.s("query"); s != "" {
					u += "&q=" + q(s)
				}
				var r struct {
					Items []struct {
						ID, Summary, Location, HtmlLink string
						Start                           struct{ DateTime, Date string }
						End                             struct{ DateTime, Date string }
					} `json:"items"`
				}
				if err := c.get(u, &r); err != nil {
					return "", err
				}
				if len(r.Items) == 0 {
					return "Nothing on the calendar in that range.", nil
				}
				var b strings.Builder
				for _, e := range r.Items {
					when := e.Start.Date + " (all day)"
					if e.Start.DateTime != "" {
						if t, err := time.Parse(time.RFC3339, e.Start.DateTime); err == nil {
							when = t.Local().Format("Mon 2 Jan 15:04")
							if t2, err := time.Parse(time.RFC3339, e.End.DateTime); err == nil {
								when += "–" + t2.Local().Format("15:04")
							}
						}
					}
					fmt.Fprintf(&b, "%s · %s", when, e.Summary)
					if e.Location != "" {
						b.WriteString(" @ " + e.Location)
					}
					b.WriteString(" [" + e.ID + "]\n")
				}
				return b.String(), nil
			}},
		{Name: "gcal_create", Group: "calendar", Risk: Write, Scopes: []string{gScope + "calendar.events"},
			Desc:     "Create an event on the user's primary Google Calendar. Times are local. Attendees get an invitation email.",
			Props:    map[string]any{"title": str("Title"), "start": str("YYYY-MM-DD HH:MM local, or YYYY-MM-DD for all day"), "end": str("Optional end, same format (default 1 hour later)"), "location": str("Optional"), "description": str("Optional"), "attendees": str("Optional comma-separated emails to invite")},
			Required: []string{"title", "start"},
			Ask: func(a args) (string, string) {
				d := a.s("title") + "\n" + a.s("start")
				if a.s("end") != "" {
					d += " → " + a.s("end")
				}
				if a.s("location") != "" {
					d += "\n@ " + a.s("location")
				}
				if a.s("attendees") != "" {
					d += "\nInvites: " + a.s("attendees")
				}
				return "Add this to Google Calendar?", d
			},
			Run: func(c *call) (string, error) {
				st, allDay, err := parseWhen(c.a.s("start"))
				if err != nil {
					return "", err
				}
				en := st.Add(time.Hour)
				if allDay {
					en = st.AddDate(0, 0, 1)
				}
				if c.a.s("end") != "" {
					if en, _, err = parseWhen(c.a.s("end")); err != nil {
						return "", err
					}
				}
				ev := map[string]any{"summary": c.a.s("title"), "location": c.a.s("location"), "description": c.a.s("description")}
				if allDay {
					ev["start"] = map[string]any{"date": st.Format("2006-01-02")}
					ev["end"] = map[string]any{"date": en.Format("2006-01-02")}
				} else {
					ev["start"] = map[string]any{"dateTime": st.Format(time.RFC3339)}
					ev["end"] = map[string]any{"dateTime": en.Format(time.RFC3339)}
				}
				u := calAPI + "/events"
				if at := c.a.s("attendees"); at != "" {
					var l []map[string]string
					for _, e := range strings.Split(at, ",") {
						if e = strings.TrimSpace(e); e != "" {
							l = append(l, map[string]string{"email": e})
						}
					}
					ev["attendees"] = l
					u += "?sendUpdates=all"
				}
				var r struct{ ID, HtmlLink string }
				if err := c.json("POST", u, ev, &r); err != nil {
					return "", err
				}
				return fmt.Sprintf("Added \"%s\" to Google Calendar (%s). %s", c.a.s("title"), r.ID, r.HtmlLink), nil
			}},
		{Name: "drive_search", Group: "drive", Risk: Read, Scopes: []string{gScope + "drive.readonly"},
			Desc:  "Search the user's Google Drive by file name or contents. Returns ids for drive_read.",
			Props: map[string]any{"query": str("Words to look for")}, Required: []string{"query"},
			Run: func(c *call) (string, error) {
				w := strings.ReplaceAll(strings.ReplaceAll(c.a.s("query"), `\`, `\\`), "'", `\'`)
				var r struct {
					Files []struct{ ID, Name, MimeType, WebViewLink, ModifiedTime string } `json:"files"`
				}
				if err := c.get(driveAPI+"/files?pageSize=15&orderBy=modifiedTime%20desc&fields=files(id,name,mimeType,webViewLink,modifiedTime)&q="+q("(name contains '"+w+"' or fullText contains '"+w+"') and trashed=false"), &r); err != nil {
					// orderBy isn't allowed with fullText; retry without it.
					if err2 := c.get(driveAPI+"/files?pageSize=15&fields=files(id,name,mimeType,webViewLink,modifiedTime)&q="+q("(name contains '"+w+"' or fullText contains '"+w+"') and trashed=false"), &r); err2 != nil {
						return "", err2
					}
				}
				if len(r.Files) == 0 {
					return "No Drive files matched.", nil
				}
				var b strings.Builder
				for _, f := range r.Files {
					fmt.Fprintf(&b, "%s · %s · %s · id %s\n   %s\n", f.Name, strings.TrimPrefix(f.MimeType, "application/vnd.google-apps."), clip(f.ModifiedTime, 10), f.ID, f.WebViewLink)
				}
				return b.String(), nil
			}},
		{Name: "drive_read", Group: "drive", Risk: Read, Scopes: []string{gScope + "drive.readonly"},
			Desc:  "Read a Google Drive file's text by id: Google Docs and Slides as text, Sheets as CSV, and plain text files.",
			Props: map[string]any{"id": str("File id from drive_search")}, Required: []string{"id"},
			Run: func(c *call) (string, error) {
				id := q(c.a.s("id"))
				var f struct{ Name, MimeType, WebViewLink, Size string }
				if err := c.get(driveAPI+"/files/"+id+"?fields=name,mimeType,webViewLink,size", &f); err != nil {
					return "", err
				}
				u := ""
				switch f.MimeType {
				case "application/vnd.google-apps.document", "application/vnd.google-apps.presentation":
					u = driveAPI + "/files/" + id + "/export?mimeType=text/plain"
				case "application/vnd.google-apps.spreadsheet":
					u = driveAPI + "/files/" + id + "/export?mimeType=text/csv"
				default:
					if strings.HasPrefix(f.MimeType, "text/") || strings.Contains(f.MimeType, "json") || strings.Contains(f.MimeType, "xml") || strings.Contains(f.MimeType, "javascript") {
						u = driveAPI + "/files/" + id + "?alt=media"
					}
				}
				if u == "" {
					return fmt.Sprintf("%s is a %s file, which Blaze can't read as text. Link: %s", f.Name, f.MimeType, f.WebViewLink), nil
				}
				st, data, err := c.do("GET", u, nil, map[string]string{"Accept": "*/*"})
				if err != nil {
					return "", err
				}
				if st > 299 {
					return "", &APIError{st, string(data)}
				}
				return fmt.Sprintf("%s (%s)\n%s\n\n%s", f.Name, f.MimeType, f.WebViewLink, clip(string(data), 20000)), nil
			}},
		{Name: "gcontacts_search", Group: "contacts", Risk: Read, Scopes: []string{gScope + "contacts.readonly"},
			Desc:  "Search the user's Google Contacts by name, email or phone number.",
			Props: map[string]any{"query": str("Name, email or number")}, Required: []string{"query"},
			Run: func(c *call) (string, error) {
				mask := "&readMask=names,emailAddresses,phoneNumbers,organizations"
				_ = c.get(peopleAPI+"/people:searchContacts?query="+mask, nil) // warm-up, as Google asks
				var r struct {
					Results []struct {
						Person struct {
							Names          []struct{ DisplayName string } `json:"names"`
							EmailAddresses []struct{ Value string }       `json:"emailAddresses"`
							PhoneNumbers   []struct{ Value string }       `json:"phoneNumbers"`
							Organizations  []struct{ Name string }        `json:"organizations"`
						} `json:"person"`
					} `json:"results"`
				}
				if err := c.get(peopleAPI+"/people:searchContacts?pageSize=15&query="+q(c.a.s("query"))+mask, &r); err != nil {
					return "", err
				}
				if len(r.Results) == 0 {
					return "No contact matched.", nil
				}
				var b strings.Builder
				for _, x := range r.Results {
					p := x.Person
					var parts []string
					if len(p.Names) > 0 {
						parts = append(parts, p.Names[0].DisplayName)
					}
					for _, e := range p.EmailAddresses {
						parts = append(parts, e.Value)
					}
					for _, n := range p.PhoneNumbers {
						parts = append(parts, n.Value)
					}
					if len(p.Organizations) > 0 && p.Organizations[0].Name != "" {
						parts = append(parts, p.Organizations[0].Name)
					}
					b.WriteString(strings.Join(parts, " · ") + "\n")
				}
				return b.String(), nil
			}},
	}
}
