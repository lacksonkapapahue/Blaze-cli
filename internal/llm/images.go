package llm

// Images on a message (v0.13: Ctrl+V and @image:). A Message with Images is sent
// in the OpenAI content-parts form:
//
//	{"role":"user","content":[{"type":"text","text":"…"},
//	  {"type":"image_url","image_url":{"url":"data:image/png;base64,…"}}]}
//
// and one without keeps the plain string form, byte for byte as before. The
// agent puts Images only on the copy of the history it sends, never on the
// history it keeps, so saved sessions stay small; reading a saved message whose
// content is a parts array (from anywhere) keeps its text and leaves "[image]"
// where each image was.

import (
	"encoding/json"
	"strings"
)

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

// plainMessage is Message without its JSON methods.
type plainMessage Message

// MarshalJSON writes Content as content parts when the message has Images.
func (m Message) MarshalJSON() ([]byte, error) {
	if len(m.Images) == 0 {
		return json.Marshal(plainMessage(m))
	}
	parts := make([]contentPart, 0, len(m.Images)+1)
	if m.Content != "" {
		parts = append(parts, contentPart{Type: "text", Text: m.Content})
	}
	for _, u := range m.Images {
		parts = append(parts, contentPart{Type: "image_url", ImageURL: &imageURL{URL: u}})
	}
	return json.Marshal(struct {
		plainMessage
		Content []contentPart `json:"content"`
	}{plainMessage(m), parts})
}

// UnmarshalJSON reads Content as a string, or as content parts (text joined,
// each image as "[image]"; image data is not kept).
func (m *Message) UnmarshalJSON(b []byte) error {
	var aux struct {
		plainMessage
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	*m = Message(aux.plainMessage)
	m.Content = ""
	raw := strings.TrimSpace(string(aux.Content))
	switch {
	case raw == "" || raw == "null":
	case raw[0] == '[':
		var parts []contentPart
		if err := json.Unmarshal(aux.Content, &parts); err != nil {
			return err
		}
		var texts []string
		for _, p := range parts {
			switch {
			case p.Type == "text" || (p.Type == "" && p.Text != ""):
				texts = append(texts, p.Text)
			case p.ImageURL != nil || strings.HasPrefix(p.Type, "image"):
				texts = append(texts, "[image]")
			}
		}
		m.Content = strings.Join(texts, "\n")
	default:
		return json.Unmarshal(aux.Content, &m.Content)
	}
	return nil
}
