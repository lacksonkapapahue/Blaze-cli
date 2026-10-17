// Package llm talks to any OpenAI-compatible chat endpoint with streaming and
// tool calls, and routes each turn across the user's free providers.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
)

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
	Images     []string   `json:"-"` // data URLs sent as image parts with Content (images.go); never saved
}

type Tool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type Result struct {
	Text      string
	ToolCalls []ToolCall
	Route     string // "Groq · model", or "Blaze · provider/model" through Blaze Cloud
	Served    Served // Blaze Cloud only: what the gateway says answered
}

type HTTPError struct {
	Code int
	Body string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Code, e.Body) }

var client = &http.Client{Timeout: 0}

// Chat does one streamed completion against one provider/model. For Blaze Cloud
// (provider "blaze") it adds the task hint, reads which model answered from the
// gateway's headers into Result.Route/Served, and returns gateway refusals as
// *GatewayError.
func Chat(ctx context.Context, p config.Provider, key, model string, msgs []Message, tools []Tool, onText func(string)) (*Result, error) {
	var extra map[string]any
	if p.ID == BlazeID {
		extra = blazeExtra(model, msgs, tools)
	}
	return chat(ctx, p, key, model, msgs, tools, extra, onText)
}

func chat(ctx context.Context, p config.Provider, key, model string, msgs []Message, tools []Tool, extra map[string]any, onText func(string)) (*Result, error) {
	body := map[string]any{"model": model, "messages": msgs, "stream": true}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	for k, v := range extra {
		body[k] = v
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(p.BaseURL, "/")+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if p.ID == "openrouter" {
		req.Header.Set("HTTP-Referer", "https://github.com/lacksonkapapahue/Blaze-cli")
		req.Header.Set("X-Title", "Blaze CLI")
	}
	if p.ID == BlazeID {
		req.Header.Set("User-Agent", "Blaze-CLI")
		req.Header.Set("X-Blaze-Client", "cli")
	}
	resp, err := client.Do(req)
	if err != nil {
		if p.ID == BlazeID && ctx.Err() == nil {
			return nil, &GatewayError{Err: err}
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4000))
		if p.ID == BlazeID {
			return nil, gatewayError(resp.StatusCode, resp.Header, raw)
		}
		return nil, &HTTPError{resp.StatusCode, cleanErr(string(raw))}
	}
	res := &Result{}
	if p.ID == BlazeID {
		res.Served = servedFrom(resp.Header)
		res.Route = res.Served.RouteLabel(model)
	}
	calls := map[int]*ToolCall{}
	var text strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		if chunk.Error != nil {
			if p.ID == BlazeID {
				return nil, &GatewayError{Status: 500, Message: chunk.Error.Message}
			}
			return nil, &HTTPError{500, chunk.Error.Message}
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != "" {
				text.WriteString(ch.Delta.Content)
				if onText != nil {
					onText(ch.Delta.Content)
				}
			}
			for _, tc := range ch.Delta.ToolCalls {
				c := calls[tc.Index]
				if c == nil {
					c = &ToolCall{Type: "function"}
					calls[tc.Index] = c
				}
				if tc.ID != "" {
					c.ID = tc.ID
				}
				if tc.Function.Name != "" {
					c.Function.Name += tc.Function.Name
				}
				c.Function.Arguments += tc.Function.Arguments
			}
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, context.Canceled) {
		if text.Len() == 0 && len(calls) == 0 {
			return nil, err
		}
	}
	idx := make([]int, 0, len(calls))
	for i := range calls {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for n, i := range idx {
		c := calls[i]
		if c.ID == "" {
			c.ID = fmt.Sprintf("call_%d", n)
		}
		if strings.TrimSpace(c.Function.Arguments) == "" {
			c.Function.Arguments = "{}"
		}
		res.ToolCalls = append(res.ToolCalls, *c)
	}
	res.Text = text.String()
	return res, nil
}

func cleanErr(s string) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(s), &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	var arr []struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(s), &arr) == nil && len(arr) > 0 && arr[0].Error.Message != "" {
		return arr[0].Error.Message
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return strings.TrimSpace(s)
}

// ---------------- model discovery ----------------

var weak = regexp.MustCompile(`(?i)(embed|guard|vision|-vl|ocr|reward|whisper|tts|audio|image|moderation|rerank|\b[0-9]b\b|\b(0\.5|1|1\.5|2|3|4|7|8)b\b|\b(mini|nano|tiny|lite)\b)`)
var strong = []string{"gpt-oss-120b", "kimi", "qwen3", "deepseek", "llama-4", "llama-3.3-70b", "glm", "gemini", "mistral-large", "mistral-medium", "nemotron", "grok", "claude", "gpt-5", "gpt-4.1"}

// Models lists the provider's model ids.
func Models(ctx context.Context, p config.Provider, key string) ([]string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(p.BaseURL, "/")+"/models", nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		return nil, &HTTPError{resp.StatusCode, cleanErr(string(raw))}
	}
	var r struct {
		Data []struct {
			ID                  string   `json:"id"`
			SupportedParameters []string `json:"supported_parameters"`
		} `json:"data"`
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	_ = json.Unmarshal(raw, &r)
	var ids []string
	for _, d := range r.Data {
		if p.ID == "openrouter" {
			if !strings.HasSuffix(d.ID, ":free") {
				continue
			}
			ok := false
			for _, s := range d.SupportedParameters {
				if s == "tools" {
					ok = true
				}
			}
			if !ok {
				continue
			}
		}
		ids = append(ids, strings.TrimPrefix(d.ID, "models/"))
	}
	return ids, nil
}

// PickModel chooses the strongest tool-capable model from a list.
func PickModel(ids []string) string {
	best, bestScore := "", -1
	for _, id := range ids {
		if weak.MatchString(id) {
			continue
		}
		score := 0
		for i, s := range strong {
			if strings.Contains(strings.ToLower(id), s) {
				score = len(strong) - i
				break
			}
		}
		if score > bestScore {
			best, bestScore = id, score
		}
	}
	if best == "" && len(ids) > 0 {
		best = ids[0]
	}
	return best
}

// ---------------- router ----------------

type health struct {
	coolUntil time.Time
	lastErr   string
	gw        *GatewayError // Blaze Cloud's last refusal, said again while it cools
}

var (
	hmu     sync.Mutex
	health_ = map[string]*health{}
)

// blazeRetryWait is the pause before a quiet retry of a Blaze Cloud call (tests shorten it).
var blazeRetryWait = func(attempt, retryAfter int) time.Duration {
	if retryAfter > 0 && retryAfter <= 5 {
		return time.Duration(retryAfter) * time.Second
	}
	return time.Duration(1+2*attempt) * time.Second
}

// blazeRetryable: worth one quiet retry (no answer, the gateway's side, a short busy).
// Never 401/403, never the daily allowance.
func blazeRetryable(ge *GatewayError) bool {
	switch {
	case ge.Status == 0:
		return true
	case ge.Status == http.StatusServiceUnavailable || ge.Status == http.StatusTooManyRequests:
		return !ge.Quota() && ge.RetryAfter <= 5
	case ge.Status >= 500:
		return true
	}
	return false
}

// ChainError is "no model could answer". It unwraps to Blaze Cloud's own failure
// (a *GatewayError) when Blaze Cloud was tried, so callers can tell an expired
// sign-in or a used-up allowance from the rest.
type ChainError struct {
	Failures []string
	Blaze    *GatewayError
}

func (e *ChainError) Error() string {
	return "no model could answer:\n  • " + strings.Join(e.Failures, "\n  • ") + "\nRun `blaze doctor` to check your models."
}

func (e *ChainError) Unwrap() error {
	if e.Blaze == nil {
		return nil
	}
	return e.Blaze
}

// Complete tries each chain entry in order, skipping ones cooling down after errors.
// Signed in, Blaze Cloud is first (see RouteChain); the user's own keys follow.
func Complete(ctx context.Context, cfg *config.Config, msgs []Message, tools []Tool, onText func(string)) (*Result, error) {
	var failures []string
	var blazeErr *GatewayError
	tried := 0
	chain := RouteChain(cfg)
	for _, e := range chain {
		p, ok := ProviderFor(e.Provider)
		if !ok {
			continue
		}
		key := KeyFor(cfg, p.ID)
		if key == "" && !config.Keyless(p.ID) {
			continue
		}
		k := p.ID + "|" + e.Model
		if p.ID == BlazeID {
			k += "|" + tokenTag(key) // a new sign-in starts with a clean slate
		}
		hmu.Lock()
		h := health_[k]
		if h == nil {
			h = &health{}
			health_[k] = h
		}
		cooling := time.Now().Before(h.coolUntil)
		var last *GatewayError
		if h.gw != nil {
			c := *h.gw
			if c.Quota() || c.Busy() {
				c.RetryAfter = int(time.Until(h.coolUntil).Seconds() + 0.5)
			}
			last = &c
		}
		hmu.Unlock()
		if cooling && tried < len(chain)-1 {
			if last != nil && blazeErr == nil {
				blazeErr = last
				failures = append(failures, p.Name+": "+last.Error())
			}
			continue
		}
		tried++
		model := e.Model
		if model == "" {
			model = p.DefaultModel
		}
		started := false
		wrapped := func(s string) {
			started = true
			if onText != nil {
				onText(s)
			}
		}
		var r *Result
		var err error
		for attempt := 0; ; attempt++ {
			r, err = Chat(ctx, p, key, model, msgs, tools, wrapped)
			ge, isGW := AsGateway(err)
			if err == nil || !isGW || started || ctx.Err() != nil || attempt >= 1 || !blazeRetryable(ge) {
				break
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(blazeRetryWait(attempt, ge.RetryAfter)):
			}
		}
		if err == nil {
			if r.Route == "" {
				r.Route = p.Name + " · " + model
			}
			hmu.Lock()
			h.coolUntil = time.Time{}
			h.lastErr = ""
			h.gw = nil
			hmu.Unlock()
			return r, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if started {
			// Half an answer already printed: don't silently restart on another model.
			if ge, ok := AsGateway(err); ok {
				return nil, fmt.Errorf("Blaze stopped mid-answer: %w", ge)
			}
			return nil, fmt.Errorf("%s stopped mid-answer: %v", p.Name, err)
		}
		cool := 60 * time.Second
		var he *HTTPError
		if ge, ok := AsGateway(err); ok {
			blazeErr = ge
			switch {
			case ge.Expired() || ge.Status == http.StatusForbidden:
				cool = 30 * time.Minute // keyed by token: signing in again clears it
			case ge.Quota():
				cool = time.Duration(max(ge.RetryAfter, 600)) * time.Second
				cool = min(cool, 24*time.Hour)
			case ge.Busy():
				cool = time.Duration(max(ge.RetryAfter, 5)) * time.Second
				cool = min(cool, 10*time.Minute)
			case ge.Status == 0 || ge.Status >= 500:
				cool = 15 * time.Second
			}
		} else if errors.As(err, &he) {
			switch {
			case he.Code == 401 || he.Code == 403:
				cool = 30 * time.Minute
			case he.Code == 429:
				cool = 90 * time.Second
			case he.Code == 400 && strings.Contains(strings.ToLower(he.Body), "tool"):
				cool = 10 * time.Minute
			}
		}
		hmu.Lock()
		h.coolUntil = time.Now().Add(cool)
		h.lastErr = err.Error()
		h.gw, _ = AsGateway(err)
		hmu.Unlock()
		msg := err.Error()
		if len(msg) > 140 && blazeErr != err {
			msg = msg[:140]
		}
		failures = append(failures, p.Name+": "+msg)
	}
	if len(failures) == 0 {
		return nil, errors.New(MsgSignedOut)
	}
	if blazeErr != nil && len(failures) == 1 {
		// Only Blaze Cloud was tried (no own keys): its own line, typed.
		return nil, blazeErr
	}
	return nil, &ChainError{Failures: failures, Blaze: blazeErr}
}
