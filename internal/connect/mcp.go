package connect

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MCP: JSON-RPC 2.0 over Streamable HTTP, the older HTTP+SSE transport, or stdio.

const mcpProtocol = "2025-06-18"

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("MCP error %d: %s", e.Code, e.Message) }

// transport sends one JSON-RPC message; for requests it returns the response.
type transport interface {
	roundTrip(ctx context.Context, msg rpcMsg, wantReply bool) (*rpcMsg, error)
	close()
}

// mcpClient is one live connection to a server.
type mcpClient struct {
	mu     sync.Mutex
	tr     transport
	nextID atomic.Int64
	ready  bool
	info   string // server name/version from initialize
}

func newMCPClient(tr transport) *mcpClient { return &mcpClient{tr: tr} }

func (c *mcpClient) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	var p json.RawMessage
	if params != nil {
		p, _ = json.Marshal(params)
	}
	id := c.nextID.Add(1)
	msg := rpcMsg{JSONRPC: "2.0", ID: json.RawMessage(fmt.Sprint(id)), Method: method, Params: p}
	r, err := c.tr.roundTrip(ctx, msg, true)
	if err != nil {
		return nil, err
	}
	if r.Error != nil {
		return nil, r.Error
	}
	return r.Result, nil
}

func (c *mcpClient) notify(ctx context.Context, method string) {
	_, _ = c.tr.roundTrip(ctx, rpcMsg{JSONRPC: "2.0", Method: method}, false)
}

func (c *mcpClient) initialize(ctx context.Context, version string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ready {
		return nil
	}
	res, err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion": mcpProtocol,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "Blaze Desktop", "version": version},
	})
	if err != nil {
		return err
	}
	var r struct {
		ServerInfo struct{ Name, Version string } `json:"serverInfo"`
	}
	_ = json.Unmarshal(res, &r)
	c.info = strings.TrimSpace(r.ServerInfo.Name + " " + r.ServerInfo.Version)
	c.notify(ctx, "notifications/initialized")
	c.ready = true
	return nil
}

func (c *mcpClient) listTools(ctx context.Context) ([]MCPTool, error) {
	var out []MCPTool
	cursor := ""
	for i := 0; i < 20; i++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, err := c.request(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var r struct {
			Tools []struct {
				Name        string         `json:"name"`
				Description string         `json:"description"`
				InputSchema map[string]any `json:"inputSchema"`
				Annotations struct {
					ReadOnly    *bool `json:"readOnlyHint"`
					Destructive *bool `json:"destructiveHint"`
				} `json:"annotations"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(res, &r); err != nil {
			return nil, err
		}
		for _, t := range r.Tools {
			mt := MCPTool{Name: t.Name, Description: t.Description, Schema: t.InputSchema}
			if t.Annotations.ReadOnly != nil && *t.Annotations.ReadOnly {
				mt.ReadOnly = true
			}
			if t.Annotations.Destructive != nil && *t.Annotations.Destructive {
				mt.Destructive = true
			}
			out = append(out, mt)
		}
		if cursor = r.NextCursor; cursor == "" {
			break
		}
	}
	return out, nil
}

// callTool runs a tool and flattens its content to text.
func (c *mcpClient) callTool(ctx context.Context, name string, arguments map[string]any) (string, error) {
	if arguments == nil {
		arguments = map[string]any{}
	}
	res, err := c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments})
	if err != nil {
		return "", err
	}
	var r struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			MimeType string `json:"mimeType"`
			Resource *struct {
				URI  string `json:"uri"`
				Text string `json:"text"`
			} `json:"resource"`
		} `json:"content"`
		Structured json.RawMessage `json:"structuredContent"`
		IsError    bool            `json:"isError"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, x := range r.Content {
		switch x.Type {
		case "text":
			b.WriteString(x.Text)
		case "resource":
			if x.Resource != nil {
				b.WriteString("[resource " + x.Resource.URI + "]\n" + x.Resource.Text)
			}
		default:
			b.WriteString("[" + x.Type + " " + x.MimeType + " content]")
		}
		b.WriteString("\n")
	}
	out := strings.TrimSpace(b.String())
	if out == "" && len(r.Structured) > 0 {
		out = string(r.Structured)
	}
	if out == "" {
		out = "Done."
	}
	if r.IsError {
		return "Tool error: " + out, nil
	}
	return out, nil
}

func (c *mcpClient) close() {
	if c.tr != nil {
		c.tr.close()
	}
}

// ---------------- Streamable HTTP ----------------

type httpTransport struct {
	url     string
	hdr     map[string]string
	hc      *http.Client
	mu      sync.Mutex
	session string
	proto   string
}

func (t *httpTransport) roundTrip(ctx context.Context, msg rpcMsg, wantReply bool) (*rpcMsg, error) {
	body, _ := json.Marshal(msg)
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
	if t.proto != "" {
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
	if resp.StatusCode == 404 && req.Header.Get("Mcp-Session-Id") != "" {
		t.mu.Lock()
		t.session, t.proto = "", ""
		t.mu.Unlock()
		return nil, errSessionGone
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
		return nil, fmt.Errorf("the server refused the request (HTTP %d). Check the token or header. %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if msg.Method == "initialize" {
		t.mu.Lock()
		t.proto = mcpProtocol
		t.mu.Unlock()
	}
	if !wantReply {
		return nil, nil
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		var found *rpcMsg
		err := readSSE(resp.Body, func(event, data string) bool {
			var m rpcMsg
			if json.Unmarshal([]byte(data), &m) == nil && string(m.ID) == string(msg.ID) && m.Method == "" {
				found = &m
				return false
			}
			return true
		})
		if found != nil {
			return found, nil
		}
		if err == nil {
			err = errors.New("the server closed the stream without answering")
		}
		return nil, err
	}
	var m rpcMsg
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err := json.Unmarshal(data, &m); err != nil {
		// A batch reply: pick ours.
		var batch []rpcMsg
		if json.Unmarshal(data, &batch) == nil {
			for _, x := range batch {
				if string(x.ID) == string(msg.ID) {
					return &x, nil
				}
			}
		}
		return nil, fmt.Errorf("the server's reply wasn't JSON-RPC: %s", clip(string(data), 200))
	}
	return &m, nil
}

func (t *httpTransport) close() {
	t.mu.Lock()
	s := t.session
	t.mu.Unlock()
	if s == "" {
		return
	}
	req, err := http.NewRequest("DELETE", t.url, nil)
	if err != nil {
		return
	}
	req.Header.Set("Mcp-Session-Id", s)
	for k, v := range t.hdr {
		req.Header.Set(k, v)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if resp, err := t.hc.Do(req.WithContext(ctx)); err == nil {
		resp.Body.Close()
	}
}

var errSessionGone = errors.New("session expired")

// readSSE calls fn for each event until fn returns false or the stream ends.
func readSSE(r io.Reader, fn func(event, data string) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	event, data := "", []string{}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if len(data) > 0 {
				if !fn(event, strings.Join(data, "\n")) {
					return nil
				}
			}
			event, data = "", nil
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		k, v, _ := strings.Cut(line, ":")
		v = strings.TrimPrefix(v, " ")
		switch k {
		case "event":
			event = v
		case "data":
			data = append(data, v)
		}
	}
	if len(data) > 0 {
		fn(event, strings.Join(data, "\n"))
	}
	return sc.Err()
}

// ---------------- stream transports (SSE and stdio) ----------------

// streamPeer matches replies to requests on a long-lived stream.
type streamPeer struct {
	mu      sync.Mutex
	waiting map[string]chan *rpcMsg
	dead    error
	send    func(ctx context.Context, b []byte) error // writes one message
	closeFn func()
}

func newPeer() *streamPeer { return &streamPeer{waiting: map[string]chan *rpcMsg{}} }

func (p *streamPeer) deliver(data []byte) {
	var m rpcMsg
	if json.Unmarshal(data, &m) != nil {
		return
	}
	if m.Method != "" {
		// A request or notification from the server. Answer pings; refuse the rest politely.
		if len(m.ID) > 0 {
			reply := rpcMsg{JSONRPC: "2.0", ID: m.ID}
			if m.Method == "ping" {
				reply.Result = json.RawMessage("{}")
			} else {
				reply.Error = &rpcError{Code: -32601, Message: "Blaze doesn't support " + m.Method}
			}
			b, _ := json.Marshal(reply)
			go func() { _ = p.send(context.Background(), b) }()
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

func (p *streamPeer) fail(err error) {
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

func (p *streamPeer) roundTrip(ctx context.Context, msg rpcMsg, wantReply bool) (*rpcMsg, error) {
	b, _ := json.Marshal(msg)
	var ch chan *rpcMsg
	p.mu.Lock()
	if p.dead != nil {
		err := p.dead
		p.mu.Unlock()
		return nil, err
	}
	if wantReply {
		ch = make(chan *rpcMsg, 1)
		p.waiting[string(msg.ID)] = ch
	}
	p.mu.Unlock()
	if err := p.send(ctx, b); err != nil {
		p.mu.Lock()
		delete(p.waiting, string(msg.ID))
		p.mu.Unlock()
		return nil, err
	}
	if !wantReply {
		return nil, nil
	}
	select {
	case m, ok := <-ch:
		if !ok {
			p.mu.Lock()
			err := p.dead
			p.mu.Unlock()
			if err == nil {
				err = errors.New("the server went away")
			}
			return nil, err
		}
		return m, nil
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.waiting, string(msg.ID))
		p.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (p *streamPeer) close() {
	p.fail(errors.New("closed"))
	if p.closeFn != nil {
		p.closeFn()
	}
}

// dialSSE opens the older HTTP+SSE transport: GET the stream, wait for the
// "endpoint" event, then POST messages there; replies arrive on the stream.
func dialSSE(ctx context.Context, hc *http.Client, rawURL string, hdr map[string]string) (*streamPeer, error) {
	sctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(sctx, "GET", rawURL, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	stream := &http.Client{Transport: hc.Transport} // no overall timeout on the stream
	resp, err := stream.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	p := newPeer()
	endpoint := make(chan string, 1)
	go func() {
		err := readSSE(resp.Body, func(event, data string) bool {
			if event == "endpoint" {
				select {
				case endpoint <- data:
				default:
				}
				return true
			}
			p.deliver([]byte(data))
			return true
		})
		resp.Body.Close()
		if err == nil {
			err = errors.New("the server closed the event stream")
		}
		p.fail(err)
	}()
	base, _ := url.Parse(rawURL)
	var post string
	select {
	case e := <-endpoint:
		u, err := base.Parse(e)
		if err != nil {
			cancel()
			return nil, err
		}
		post = u.String()
	case <-ctx.Done():
		cancel()
		return nil, errors.New("the SSE server never sent its endpoint event")
	}
	p.send = func(ctx context.Context, b []byte) error {
		req, err := http.NewRequestWithContext(ctx, "POST", post, bytes.NewReader(b))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		r, err := hc.Do(req)
		if err != nil {
			return err
		}
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		if r.StatusCode >= 300 {
			return fmt.Errorf("HTTP %d from %s", r.StatusCode, post)
		}
		return nil
	}
	p.closeFn = cancel
	return p, nil
}

// dialStdio starts a local server: newline-delimited JSON on stdin/stdout.
func dialStdio(command string, argv []string, dir string) (*streamPeer, *ringBuf, error) {
	cmd := exec.Command(command, argv...)
	cmd.Dir = dir
	hideWindow(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	logs := &ringBuf{max: 4000}
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("couldn't start %s: %v", command, err)
	}
	p := newPeer()
	var wmu sync.Mutex
	p.send = func(ctx context.Context, b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		_, err := stdin.Write(append(b, '\n'))
		return err
	}
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 32<<20)
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) > 0 && line[0] == '{' {
				p.deliver(append([]byte(nil), line...))
			}
		}
		_ = cmd.Wait()
		msg := "the local server exited"
		if s := strings.TrimSpace(logs.String()); s != "" {
			msg += ": " + clip(s, 300)
		}
		p.fail(errors.New(msg))
	}()
	p.closeFn = func() {
		_ = stdin.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		}
	}
	return p, logs, nil
}

// ringBuf keeps the tail of a server's stderr for error messages.
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

// mcpToolName is how a server's tool appears to the model: mcp_<server>_<tool>.
func mcpToolName(server, tool string) string {
	var b strings.Builder
	for _, r := range tool {
		if r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	n := "mcp_" + server + "_" + b.String()
	if len(n) > 64 {
		n = n[:64]
	}
	return n
}
