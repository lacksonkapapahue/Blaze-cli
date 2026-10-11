package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ProtocolVersion is the MCP version Blaze asks for first; older servers answer
// with their own and Blaze carries on with it.
const ProtocolVersion = "2025-06-18"

// fallbacks are tried when a server refuses the version outright.
var fallbacks = []string{"2025-03-26", "2024-11-05"}

// Timeouts (vars so tests can shorten them).
var (
	InitTimeout = 60 * time.Second // npx may download the server the first time
	ListTimeout = 30 * time.Second
	CallTimeout = 5 * time.Minute
)

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC error from the server.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("%s (code %d)", e.Message, e.Code) }

// transport moves JSON-RPC messages; send returns the reply for a request.
type transport interface {
	send(ctx context.Context, m message, wantReply bool) (*message, error)
	setProtocol(v string)
	close()
	logs() string
}

// Tool is one tool a server lists.
type Tool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations struct {
		Title       string `json:"title,omitempty"`
		ReadOnly    *bool  `json:"readOnlyHint,omitempty"`
		Destructive *bool  `json:"destructiveHint,omitempty"`
	} `json:"annotations"`
}

// ReadOnly is the tool's readOnlyHint.
func (t Tool) ReadOnly() bool { return t.Annotations.ReadOnly != nil && *t.Annotations.ReadOnly }

// Client is one live connection to a server.
type Client struct {
	tr       transport
	next     atomic.Int64
	Protocol string // the version the server agreed to
	Info     string // "name version" from initialize
}

// Connect starts the server (or opens the URL) and does the initialize handshake.
func Connect(ctx context.Context, s Server, dir, clientVersion string) (*Client, error) {
	var tr transport
	if s.Remote() {
		tr = newHTTP(expand(s.URL), expandMap(s.Headers))
	} else {
		st, err := startStdio(s, dir)
		if err != nil {
			return nil, err
		}
		tr = st
	}
	c := &Client{tr: tr}
	if err := c.initialize(ctx, clientVersion); err != nil {
		tr.close()
		if l := strings.TrimSpace(tr.logs()); l != "" && !strings.Contains(err.Error(), l) {
			err = fmt.Errorf("%w: %s", err, clip(lastLines(l, 3), 300))
		}
		return nil, err
	}
	return c, nil
}

func (c *Client) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	var p json.RawMessage
	if params != nil {
		p, _ = json.Marshal(params)
	}
	id := c.next.Add(1)
	r, err := c.tr.send(ctx, message{JSONRPC: "2.0", ID: json.RawMessage(fmt.Sprint(id)), Method: method, Params: p}, true)
	if err != nil {
		return nil, err
	}
	if r.Error != nil {
		return nil, r.Error
	}
	return r.Result, nil
}

func (c *Client) notify(ctx context.Context, method string) error {
	_, err := c.tr.send(ctx, message{JSONRPC: "2.0", Method: method}, false)
	return err
}

func (c *Client) initialize(ctx context.Context, clientVersion string) error {
	ctx, cancel := context.WithTimeout(ctx, InitTimeout)
	defer cancel()
	versions := append([]string{ProtocolVersion}, fallbacks...)
	var res json.RawMessage
	var err error
	for i, v := range versions {
		res, err = c.request(ctx, "initialize", map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "Blaze CLI", "version": clientVersion},
		})
		var re *RPCError
		// Only a refused version is worth another try with an older one.
		if err != nil && errors.As(err, &re) && i < len(versions)-1 && strings.Contains(strings.ToLower(re.Message), "version") {
			continue
		}
		break
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("the server didn't answer the handshake within %s", InitTimeout)
		}
		return err
	}
	var r struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return fmt.Errorf("the server's handshake reply wasn't understood: %v", err)
	}
	c.Protocol = r.ProtocolVersion
	if c.Protocol == "" {
		c.Protocol = ProtocolVersion
	}
	c.Info = strings.TrimSpace(r.ServerInfo.Name + " " + r.ServerInfo.Version)
	c.tr.setProtocol(c.Protocol)
	return c.notify(ctx, "notifications/initialized")
}

// ListTools asks for every tool, following nextCursor.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	ctx, cancel := context.WithTimeout(ctx, ListTimeout)
	defer cancel()
	var out []Tool
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 50; page++ {
		var params any
		if cursor != "" {
			params = map[string]any{"cursor": cursor}
		}
		res, err := c.request(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var r struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(res, &r); err != nil {
			return nil, fmt.Errorf("the tool list wasn't understood: %v", err)
		}
		out = append(out, r.Tools...)
		if r.NextCursor == "" || seen[r.NextCursor] {
			break
		}
		seen[r.NextCursor] = true
		cursor = r.NextCursor
	}
	return out, nil
}

// CallResult is a tool's answer flattened for the model.
type CallResult struct {
	Text    string
	IsError bool
}

// CallTool runs a tool and returns its text content; other content is noted.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (CallResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	res, err := c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return CallResult{}, fmt.Errorf("no answer within %s", CallTimeout)
		}
		return CallResult{}, err
	}
	return flatten(res)
}

func flatten(res json.RawMessage) (CallResult, error) {
	var r struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			MimeType string `json:"mimeType"`
			URI      string `json:"uri"`
			Name     string `json:"name"`
			Resource *struct {
				URI      string `json:"uri"`
				MimeType string `json:"mimeType"`
				Text     string `json:"text"`
				Blob     string `json:"blob"`
			} `json:"resource"`
		} `json:"content"`
		Structured json.RawMessage `json:"structuredContent"`
		IsError    bool            `json:"isError"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return CallResult{}, fmt.Errorf("the tool's reply wasn't understood: %v", err)
	}
	var parts []string
	for _, x := range r.Content {
		switch x.Type {
		case "text":
			parts = append(parts, x.Text)
		case "resource":
			if x.Resource == nil {
				continue
			}
			if x.Resource.Text != "" {
				parts = append(parts, "[resource "+x.Resource.URI+"]\n"+x.Resource.Text)
			} else {
				parts = append(parts, "[resource "+x.Resource.URI+" ("+orStr(x.Resource.MimeType, "binary")+"), not shown]")
			}
		case "resource_link":
			parts = append(parts, "[link "+strings.TrimSpace(x.Name+" "+x.URI)+"]")
		case "image", "audio":
			parts = append(parts, "["+x.Type+" content ("+orStr(x.MimeType, "unknown type")+") returned, not shown]")
		default:
			parts = append(parts, "["+x.Type+" content, not shown]")
		}
	}
	out := strings.TrimSpace(strings.Join(parts, "\n"))
	if out == "" && len(r.Structured) > 0 && string(r.Structured) != "null" {
		out = string(r.Structured)
	}
	if out == "" {
		out = "Done (the tool returned no content)."
	}
	return CallResult{Text: out, IsError: r.IsError}, nil
}

// Close ends the session and stops a local server.
func (c *Client) Close() { c.tr.close() }

// Logs is the tail of a local server's stderr.
func (c *Client) Logs() string { return c.tr.logs() }

// ---------------- stream peer (stdio) ----------------

type peer struct {
	mu      sync.Mutex
	waiting map[string]chan *message
	dead    error
	write   func(b []byte) error
}

func newPeer() *peer { return &peer{waiting: map[string]chan *message{}} }

func (p *peer) deliver(data []byte) {
	var m message
	if json.Unmarshal(data, &m) != nil {
		return
	}
	if m.Method != "" {
		// A request or notification from the server: answer pings, refuse other requests.
		if len(m.ID) > 0 {
			reply := message{JSONRPC: "2.0", ID: m.ID}
			if m.Method == "ping" {
				reply.Result = json.RawMessage("{}")
			} else {
				reply.Error = &RPCError{Code: -32601, Message: "Blaze CLI doesn't support " + m.Method}
			}
			b, _ := json.Marshal(reply)
			go func() { _ = p.write(b) }()
		}
		return
	}
	p.mu.Lock()
	ch := p.waiting[string(m.ID)]
	delete(p.waiting, string(m.ID))
	p.mu.Unlock()
	if ch != nil {
		ch <- &m
	}
}

func (p *peer) fail(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead == nil {
		p.dead = err
	}
	for id, ch := range p.waiting {
		close(ch)
		delete(p.waiting, id)
	}
}

func (p *peer) err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dead
}

func (p *peer) roundTrip(ctx context.Context, m message, wantReply bool) (*message, error) {
	b, _ := json.Marshal(m)
	var ch chan *message
	p.mu.Lock()
	if p.dead != nil {
		err := p.dead
		p.mu.Unlock()
		return nil, err
	}
	if wantReply {
		ch = make(chan *message, 1)
		p.waiting[string(m.ID)] = ch
	}
	p.mu.Unlock()
	if err := p.write(b); err != nil {
		p.mu.Lock()
		delete(p.waiting, string(m.ID))
		p.mu.Unlock()
		if d := p.err(); d != nil {
			return nil, d
		}
		return nil, err
	}
	if !wantReply {
		return nil, nil
	}
	select {
	case r, ok := <-ch:
		if !ok {
			if d := p.err(); d != nil {
				return nil, d
			}
			return nil, errors.New("the server went away")
		}
		return r, nil
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.waiting, string(m.ID))
		p.mu.Unlock()
		return nil, ctx.Err()
	}
}

// ---------------- stdio ----------------

type stdio struct {
	*peer
	cmd   *exec.Cmd
	stdin io.WriteCloser
	errs  *ringBuf
	done  chan struct{}
	once  sync.Once
}

func startStdio(s Server, dir string) (*stdio, error) {
	command := expand(s.Command)
	path, err := lookPath(command)
	if err != nil {
		return nil, fmt.Errorf("couldn't find %q on this PC: install it or give the full path", command)
	}
	args := make([]string, len(s.Args))
	for i, a := range s.Args {
		args[i] = expand(a)
	}
	cmd := exec.Command(path, args...)
	cmd.Dir = dir
	if s.Cwd != "" {
		cmd.Dir = expand(s.Cwd)
	}
	cmd.Env = os.Environ()
	for k, v := range s.Env {
		cmd.Env = append(cmd.Env, k+"="+expand(v))
	}
	detach(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	errs := &ringBuf{max: 8000}
	cmd.Stderr = errs
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("couldn't start %s: %v", command, err)
	}
	t := &stdio{peer: newPeer(), cmd: cmd, stdin: stdin, errs: errs, done: make(chan struct{})}
	var wmu sync.Mutex
	t.write = func(b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		_, err := stdin.Write(append(b, '\n'))
		return err
	}
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 64<<20)
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) > 0 && line[0] == '{' { // servers sometimes print banners; skip non-JSON
				t.deliver(append([]byte(nil), line...))
			}
		}
		_ = cmd.Wait()
		close(t.done)
		msg := "the server stopped"
		if l := strings.TrimSpace(errs.String()); l != "" {
			msg += ": " + clip(lastLines(l, 3), 300)
		}
		t.fail(errors.New(msg))
	}()
	return t, nil
}

func (t *stdio) send(ctx context.Context, m message, wantReply bool) (*message, error) {
	return t.roundTrip(ctx, m, wantReply)
}

func (t *stdio) setProtocol(string) {}

func (t *stdio) logs() string { return t.errs.String() }

// close shuts stdin (the polite way to stop a stdio server), then kills the
// server and anything it started if it hasn't gone within two seconds.
func (t *stdio) close() {
	t.once.Do(func() {
		t.fail(errors.New("closed"))
		_ = t.stdin.Close()
		select {
		case <-t.done:
		case <-time.After(2 * time.Second):
			killTree(t.cmd)
			select {
			case <-t.done:
			case <-time.After(2 * time.Second):
			}
		}
	})
}

// lookPath finds the command. On Windows exec.LookPath tries PATHEXT, so "npx"
// finds npx.cmd.
func lookPath(c string) (string, error) {
	if strings.ContainsAny(c, `/\`) {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return exec.LookPath(c)
}

// ---------------- Streamable HTTP ----------------

type httpT struct {
	url     string
	hdr     map[string]string
	hc      *http.Client
	mu      sync.Mutex
	session string
	proto   string
}

func newHTTP(u string, hdr map[string]string) *httpT {
	return &httpT{url: u, hdr: hdr, hc: &http.Client{}}
}

func (t *httpT) setProtocol(v string) {
	t.mu.Lock()
	t.proto = v
	t.mu.Unlock()
}

func (t *httpT) logs() string { return "" }

func (t *httpT) send(ctx context.Context, m message, wantReply bool) (*message, error) {
	body, _ := json.Marshal(m)
	req, err := http.NewRequestWithContext(ctx, "POST", t.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range t.hdr {
		req.Header.Set(k, v)
	}
	t.mu.Lock()
	if t.session != "" {
		req.Header.Set("Mcp-Session-Id", t.session)
	}
	if t.proto != "" && m.Method != "initialize" {
		req.Header.Set("MCP-Protocol-Version", t.proto)
	}
	t.mu.Unlock()
	resp, err := t.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		t.mu.Lock()
		t.session = s
		t.mu.Unlock()
	}
	switch {
	case resp.StatusCode == 404 && req.Header.Get("Mcp-Session-Id") != "":
		t.mu.Lock()
		t.session = ""
		t.mu.Unlock()
		return nil, ErrSessionGone
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("the server refused the request (HTTP %d): check the token or headers. %s", resp.StatusCode, strings.TrimSpace(string(b)))
	case resp.StatusCode >= 300:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if !wantReply {
		return nil, nil
	}
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/event-stream") {
		var found *message
		err := readSSE(resp.Body, func(data string) bool {
			var r message
			if json.Unmarshal([]byte(data), &r) == nil && r.Method == "" && string(r.ID) == string(m.ID) {
				found = &r
				return false
			}
			return true // notifications and server requests on the stream are skipped
		})
		if found != nil {
			return found, nil
		}
		if err == nil {
			err = errors.New("the server closed the stream without answering")
		}
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	var r message
	if err := json.Unmarshal(data, &r); err != nil {
		var batch []message
		if json.Unmarshal(data, &batch) == nil {
			for _, x := range batch {
				if string(x.ID) == string(m.ID) {
					return &x, nil
				}
			}
		}
		return nil, fmt.Errorf("the server's reply wasn't JSON-RPC: %s", clip(string(data), 200))
	}
	return &r, nil
}

// close ends the HTTP session (DELETE with the session id), best effort.
func (t *httpT) close() {
	t.mu.Lock()
	s := t.session
	t.mu.Unlock()
	if s == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "DELETE", t.url, nil)
	if err != nil {
		return
	}
	req.Header.Set("Mcp-Session-Id", s)
	for k, v := range t.hdr {
		req.Header.Set(k, v)
	}
	if resp, err := t.hc.Do(req); err == nil {
		resp.Body.Close()
	}
}

// ErrSessionGone means the HTTP server forgot the session; reconnect.
var ErrSessionGone = errors.New("the server ended the session")

func readSSE(r io.Reader, fn func(data string) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 64<<20)
	var data []string
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if line == "" {
			if len(data) > 0 && !fn(strings.Join(data, "\n")) {
				return nil
			}
			data = nil
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		k, v, _ := strings.Cut(line, ":")
		if k == "data" {
			data = append(data, strings.TrimPrefix(v, " "))
		}
	}
	if len(data) > 0 {
		fn(strings.Join(data, "\n"))
	}
	return sc.Err()
}

// ---------------- helpers ----------------

type ringBuf struct {
	mu  sync.Mutex
	b   []byte
	max int
}

func (r *ringBuf) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.b = append(r.b, p...)
	if len(r.b) > r.max {
		r.b = r.b[len(r.b)-r.max:]
	}
	return len(p), nil
}

func (r *ringBuf) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.b)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func lastLines(s string, n int) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, " / ")
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func expandMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = expand(v)
	}
	return out
}
