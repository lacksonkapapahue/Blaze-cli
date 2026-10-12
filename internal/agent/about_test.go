package agent

import (
	"strings"
	"testing"

	"github.com/lacksonkapapahue/blaze-cli/internal/selfknow"
	"github.com/lacksonkapapahue/blaze-cli/internal/tools"
)

// The system prompt carries ABOUT YOU and the rule against asking "which apps are you building?".
func TestSystemPromptHasAboutYou(t *testing.T) {
	old := tools.AboutFacts
	tools.AboutFacts = func(bool) selfknow.Facts {
		return selfknow.Facts{App: "Blaze CLI", Version: "v0.10.0", DataDir: "/home/luke/.blaze", System: selfknow.System{OSName: "Arch Linux", OS: "linux", Arch: "amd64"}}
	}
	defer func() { tools.AboutFacts = old }()
	sys := newTestAgent(t).SystemPrompt()
	for _, want := range []string{"ABOUT YOU", "running here as Blaze CLI v0.10.0", "Blaze Android", "Phone ↔ PC (Blaze Link)", selfknow.ToolName, "Never ask the user which apps they are building"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt misses %q", want)
		}
	}
}
