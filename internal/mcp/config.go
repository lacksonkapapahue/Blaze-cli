// Package mcp is Blaze's Model Context Protocol client: it reads mcp.json (the
// same file Blaze Desktop uses), starts each server over stdio or Streamable
// HTTP, and offers the servers' tools to the model as mcp__<server>__<tool>.
package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Server is one entry under "mcpServers": a local command (stdio) or a URL
// (Streamable HTTP).
type Server struct {
	Name     string            `json:"-"`
	Command  string            `json:"command,omitempty"`
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Cwd      string            `json:"cwd,omitempty"`
	URL      string            `json:"url,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Disabled bool              `json:"disabled,omitempty"`
	Source   string            `json:"-"` // the file it came from
	// Project is true for a server from the working folder's .mcp.json or
	// .blaze/mcp.json; Trusted is true when that folder's files are trusted
	// (always true for the global file). An untrusted project server never
	// starts and its ${VAR}s are never expanded.
	Project bool `json:"-"`
	Trusted bool `json:"-"`
}

// MayStart is false for a project server whose folder isn't trusted.
func (s Server) MayStart() bool { return !s.Project || s.Trusted }

// ex expands ${VAR}s, except in an untrusted project server (whose file could
// otherwise send the user's secrets to any URL or command).
func (s Server) ex(v string) string {
	if !s.MayStart() {
		return v
	}
	return expand(v)
}

func (s Server) exMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = s.ex(v)
	}
	return out
}

// Remote is true for a URL server.
func (s Server) Remote() bool { return s.URL != "" }

// Kind is "stdio" or "http".
func (s Server) Kind() string {
	if s.Remote() {
		return "http"
	}
	return "stdio"
}

// Target is the command line or the URL, for listings.
func (s Server) Target() string {
	if s.Remote() {
		return s.URL
	}
	return strings.TrimSpace(s.Command + " " + strings.Join(s.Args, " "))
}

// GlobalFile is ~/.blaze/mcp.json (home is config.Dir(), which respects BLAZE_HOME).
func GlobalFile(home string) string { return filepath.Join(home, "mcp.json") }

// ProjectFiles are the working folder's files, lowest precedence first:
// .mcp.json (the common project file), then .blaze/mcp.json.
func ProjectFiles(dir string) []string {
	if dir == "" {
		return nil
	}
	return []string{filepath.Join(dir, ".mcp.json"), filepath.Join(dir, ".blaze", "mcp.json")}
}

// Files are every file Load reads, lowest precedence first.
func Files(home, dir string) []string {
	out := []string{GlobalFile(home)}
	for _, f := range ProjectFiles(dir) {
		if !samePath(f, out[0]) {
			out = append(out, f)
		}
	}
	return out
}

func samePath(a, b string) bool {
	x, _ := filepath.Abs(a)
	y, _ := filepath.Abs(b)
	return x == y
}

// Load reads the global file and the project files. Project servers are marked
// Project (and Trusted when the folder's trust matches the files' hash); one
// with the same name as a global server is ignored with a warning, so a repo
// can never stand in for (and inherit the approvals of) the user's own server.
// Unreadable files come back as warnings.
func Load(home, dir string) ([]Server, []string) {
	byName := map[string]Server{}
	var warns []string
	g := GlobalFile(home)
	ss, err := readFile(g)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		warns = append(warns, fmt.Sprintf("%s: %v", g, err))
	}
	for _, s := range ss {
		s.Trusted = true
		byName[s.Name] = s
	}
	p := LoadProject(home, dir)
	warns = append(warns, p.Warnings...)
	for _, s := range p.Servers {
		if gs, ok := byName[s.Name]; ok && !gs.Project {
			warns = append(warns, fmt.Sprintf("%s: server %q ignored: %s already has a server with that name", s.Source, s.Name, g))
			continue
		}
		byName[s.Name] = s
	}
	out := make([]Server, 0, len(byName))
	for _, s := range byName {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, warns
}

func readFile(path string) ([]Server, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseServers(path, b)
}

func parseServers(path string, b []byte) ([]Server, error) {
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil, nil
	}
	var doc struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("not valid JSON (%v)", err)
	}
	var out []Server
	for name, raw := range doc.Servers {
		var s Server
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("server %q: %v", name, err)
		}
		// Some files say "type": "http"/"sse" with "serverUrl"; accept the alias.
		if s.URL == "" {
			var alt struct {
				ServerURL string `json:"serverUrl"`
			}
			_ = json.Unmarshal(raw, &alt)
			s.URL = alt.ServerURL
		}
		s.Name, s.Source = name, path
		if s.Command == "" && s.URL == "" {
			continue // nothing to start
		}
		out = append(out, s)
	}
	return out, nil
}

// ---------------- editing (keeps keys Blaze doesn't know, for Desktop) ----------------

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,39}$`)

// ValidName checks a server name: letters, digits, - and _, up to 40 characters.
func ValidName(n string) error {
	if !nameRe.MatchString(n) || strings.Contains(n, "__") {
		return fmt.Errorf("%q isn't a good server name: use letters, digits, - or _ (up to 40, no double underscore)", n)
	}
	return nil
}

type rawDoc struct {
	top     map[string]json.RawMessage
	servers map[string]map[string]any
}

func readRaw(path string) (*rawDoc, error) {
	d := &rawDoc{top: map[string]json.RawMessage{}, servers: map[string]map[string]any{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && strings.TrimSpace(string(b)) == "") {
		return d, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &d.top); err != nil {
		return nil, fmt.Errorf("%s isn't valid JSON (%v); fix it by hand first", path, err)
	}
	if raw, ok := d.top["mcpServers"]; ok {
		if err := json.Unmarshal(raw, &d.servers); err != nil {
			return nil, fmt.Errorf("%s: mcpServers isn't an object (%v)", path, err)
		}
	}
	if d.servers == nil {
		d.servers = map[string]map[string]any{}
	}
	return d, nil
}

func (d *rawDoc) write(path string) error {
	sb, err := json.Marshal(d.servers)
	if err != nil {
		return err
	}
	d.top["mcpServers"] = sb
	b, err := json.MarshalIndent(d.top, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'), 0o600)
}

// Add writes a server into file (replacing one with the same name, keeping its
// other keys).
func Add(file string, s Server) error {
	if err := ValidName(s.Name); err != nil {
		return err
	}
	if s.Command == "" && s.URL == "" {
		return errors.New("give a command (after --) or --url")
	}
	d, err := readRaw(file)
	if err != nil {
		return err
	}
	e := map[string]any{}
	if s.Remote() {
		e["url"] = s.URL
		if len(s.Headers) > 0 {
			e["headers"] = s.Headers
		}
		e["type"] = "http"
	} else {
		e["command"] = s.Command
		e["args"] = append([]string{}, s.Args...)
		if len(s.Env) > 0 {
			e["env"] = s.Env
		}
	}
	d.servers[s.Name] = e
	return d.write(file)
}

// Remove deletes a server from file; false when it wasn't there.
func Remove(file, name string) (bool, error) {
	d, err := readRaw(file)
	if err != nil {
		return false, err
	}
	if _, ok := d.servers[name]; !ok {
		return false, nil
	}
	delete(d.servers, name)
	return true, d.write(file)
}

// SetDisabled turns a server off or on in file; false when it wasn't there.
func SetDisabled(file, name string, off bool) (bool, error) {
	d, err := readRaw(file)
	if err != nil {
		return false, err
	}
	e, ok := d.servers[name]
	if !ok {
		return false, nil
	}
	if off {
		e["disabled"] = true
	} else {
		delete(e, "disabled")
	}
	return true, d.write(file)
}

// FileDefining is the highest-precedence file that has the server ("" if none).
func FileDefining(home, dir, name string) string {
	fs := Files(home, dir)
	for i := len(fs) - 1; i >= 0; i-- {
		ss, err := readFile(fs[i])
		if err != nil {
			continue
		}
		for _, s := range ss {
			if s.Name == name {
				return fs[i]
			}
		}
		// A server without command/url is still "there" for remove.
		if d, err := readRaw(fs[i]); err == nil {
			if _, ok := d.servers[name]; ok {
				return fs[i]
			}
		}
	}
	return ""
}

// expand replaces ${VAR} and ${VAR:-default} with environment values.
func expand(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return varRe.ReplaceAllStringFunc(s, func(m string) string {
		k := m[2 : len(m)-1]
		if name, def, ok := strings.Cut(k, ":-"); ok {
			if v, ok := os.LookupEnv(name); ok && v != "" {
				return v
			}
			return def
		}
		return os.Getenv(k)
	})
}

var varRe = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*(:-[^}]*)?\}`)
