package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
)

func TestCleanErr(t *testing.T) {
	cases := map[string]string{
		`{"error":{"message":"rate limited"}}`: "rate limited",
		`[{"error":{"message":"bad key"}}]`:    "bad key",
		"  plain text  ":                       "plain text",
		strings.Repeat("x", 400):               strings.Repeat("x", 300),
	}
	for in, want := range cases {
		if got := cleanErr(in); got != want {
			t.Errorf("cleanErr(%.30q) = %.30q, want %.30q", in, got, want)
		}
	}
}

func TestPickModel(t *testing.T) {
	cases := []struct {
		ids  []string
		want string
	}{
		{[]string{"text-embedding-3", "llama-3.1-8b-instant", "llama-3.3-70b-versatile"}, "llama-3.3-70b-versatile"},
		{[]string{"gpt-4o-mini", "mistral-large-latest"}, "mistral-large-latest"},
		// "gemini" contains "mini"; it must not be treated as a small model.
		{[]string{"google/gemini-2.5-pro:free", "some-model"}, "google/gemini-2.5-pro:free"},
		{[]string{"gemini-2.5-flash-lite", "gemini-2.5-flash"}, "gemini-2.5-flash"},
		{[]string{"tiny-embed"}, "tiny-embed"}, // nothing strong: fall back to the first
		{nil, ""},
	}
	for _, c := range cases {
		if got := PickModel(c.ids); got != c.want {
			t.Errorf("PickModel(%v) = %q, want %q", c.ids, got, c.want)
		}
	}
}

func sse(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, c := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", c)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func TestChatStreamsTextAndToolCalls(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		sse(w,
			`{"choices":[{"delta":{"content":"Hel"}}]}`,
			`{"choices":[{"delta":{"content":"lo"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"name":"run_","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"read_file","arguments":"{\"path\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"name":"shell"}}]}}]}`,
			`not json, skipped`,
		)
	}))
	defer srv.Close()

	var streamed strings.Builder
	p := config.Provider{ID: "test", BaseURL: srv.URL + "/"}
	res, err := Chat(context.Background(), p, "k", "m", []Message{{Role: "user", Content: "hi"}}, []Tool{{Type: "function"}}, func(s string) { streamed.WriteString(s) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Hello" || streamed.String() != "Hello" {
		t.Errorf("text = %q, streamed %q", res.Text, streamed.String())
	}
	if got["stream"] != true || got["model"] != "m" || got["tools"] == nil {
		t.Errorf("request body = %v", got)
	}
	if len(res.ToolCalls) != 2 {
		t.Fatalf("tool calls = %+v", res.ToolCalls)
	}
	a, b := res.ToolCalls[0], res.ToolCalls[1]
	if a.ID != "a" || a.Function.Name != "read_file" || a.Function.Arguments != `{"path":"x"}` {
		t.Errorf("first call = %+v", a)
	}
	if b.ID != "call_1" || b.Function.Name != "run_shell" || b.Function.Arguments != "{}" {
		t.Errorf("second call = %+v", b)
	}
}

func TestChatErrors(t *testing.T) {
	status := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"slow down"}}`)
	}))
	defer status.Close()
	_, err := Chat(context.Background(), config.Provider{ID: "test", BaseURL: status.URL}, "", "m", nil, nil, nil)
	var he *HTTPError
	if !errors.As(err, &he) || he.Code != http.StatusTooManyRequests || he.Body != "slow down" {
		t.Fatalf("status error = %v", err)
	}

	mid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"error":{"message":"overloaded"}}`)
	}))
	defer mid.Close()
	_, err = Chat(context.Background(), config.Provider{ID: "test", BaseURL: mid.URL}, "", "m", nil, nil, nil)
	if !errors.As(err, &he) || he.Body != "overloaded" {
		t.Fatalf("mid-stream error = %v", err)
	}
}
