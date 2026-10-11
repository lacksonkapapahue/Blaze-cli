package llm

// Attachments on a user message: an image pasted with Ctrl+V (or attached as
// @picture.png) goes to models that can see pictures as an image_url part; other
// models get a one-line note with the file's path instead. Text files and folders
// are inlined into the message text by the CLI itself and never reach this.

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Attach is one attachment on a user message (the desktop's llm.Attach, trimmed).
type Attach struct {
	Kind string `json:"kind"` // image (file and folder are inlined as text)
	Path string `json:"path"` // where it is on disk
	Name string `json:"name,omitempty"`
	Mime string `json:"mime,omitempty"`
}

// maxImage is the largest picture sent inline (bigger ones go as a path note).
const maxImage = 8 << 20

// VisionProvider reports whether a provider's chat endpoint takes image parts
// for its usual models (Gemini, OpenAI, Anthropic, xAI, OpenRouter's default).
func VisionProvider(id string) bool {
	switch id {
	case "gemini", "openai", "anthropic", "xai", "openrouter":
		return true
	}
	return false
}

// ImageMime is the MIME type for a picture path ("" when it isn't one).
func ImageMime(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	}
	return ""
}

// wire turns messages into what one provider receives: attachments become
// image_url parts (vision) or a note (no vision); every other message is as is.
func wire(msgs []Message, vision bool) []any {
	out := make([]any, len(msgs))
	for i, m := range msgs {
		if len(m.Attach) == 0 {
			out[i] = m
			continue
		}
		var notes []string
		var parts []map[string]any
		for _, a := range m.Attach {
			b, err := os.ReadFile(a.Path)
			mime := a.Mime
			if mime == "" {
				mime = ImageMime(a.Path)
			}
			if !vision || err != nil || len(b) > maxImage || mime == "" {
				notes = append(notes, "[Image attached: "+a.Path+" — this model can't see it; say so if the picture matters, and suggest a vision model such as Gemini]")
				continue
			}
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b)}})
		}
		text := m.Content
		if len(notes) > 0 {
			text += "\n\n" + strings.Join(notes, "\n")
		}
		if len(parts) == 0 {
			out[i] = map[string]any{"role": m.Role, "content": text}
			continue
		}
		parts = append([]map[string]any{{"type": "text", "text": text}}, parts...)
		out[i] = map[string]any{"role": m.Role, "content": parts}
	}
	return out
}

// wireJSON is wire as JSON (tests).
func wireJSON(msgs []Message, vision bool) string {
	b, _ := json.Marshal(wire(msgs, vision))
	return string(b)
}
