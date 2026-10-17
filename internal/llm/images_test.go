package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessagePlainJSONUnchanged(t *testing.T) {
	m := Message{Role: "assistant", Content: "hi", ToolCallID: "x"}
	b, err := json.Marshal(m)
	if err != nil || string(b) != `{"role":"assistant","content":"hi","tool_call_id":"x"}` {
		t.Fatalf("%s %v", b, err)
	}
	var back Message
	if err := json.Unmarshal(b, &back); err != nil || back.Content != "hi" || back.ToolCallID != "x" || back.Role != "assistant" {
		t.Fatalf("%+v %v", back, err)
	}
	// slices of messages (what sessions save) round trip too
	h := []Message{{Role: "user", Content: "a\n\"q\""}, {Role: "tool", Name: "n", Content: ""}}
	b, _ = json.Marshal(h)
	var hb []Message
	if err := json.Unmarshal(b, &hb); err != nil || hb[0].Content != h[0].Content || hb[1].Name != "n" {
		t.Fatalf("%+v %v", hb, err)
	}
}

func TestMessageImagesAsParts(t *testing.T) {
	m := Message{Role: "user", Content: "what is [image 1]?", Images: []string{"data:image/png;base64,AAAA"}}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"role":"user","content":[{"type":"text","text":"what is [image 1]?"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
	// inside a request body
	body, _ := json.Marshal(map[string]any{"messages": []Message{m}})
	if !strings.Contains(string(body), `"image_url":{"url":"data:image/png;base64,AAAA"}`) {
		t.Fatal(string(body))
	}
	// read back: text kept, image data dropped for a placeholder
	var back Message
	if err := json.Unmarshal(b, &back); err != nil || back.Content != "what is [image 1]?\n[image]" || len(back.Images) != 0 {
		t.Fatalf("%+v %v", back, err)
	}
}

func TestMessageNullContent(t *testing.T) {
	var m Message
	if err := json.Unmarshal([]byte(`{"role":"assistant","content":null,"tool_calls":[{"id":"1","type":"function","function":{"name":"f","arguments":"{}"}}]}`), &m); err != nil || m.Content != "" || len(m.ToolCalls) != 1 {
		t.Fatalf("%+v %v", m, err)
	}
}
