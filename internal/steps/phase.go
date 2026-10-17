package steps

import "strings"

// Phases of the working line (v0.12.0): what kind of work a plain phrase is, so the
// terminal can give thinking and doing a different, subtle motion.
const (
	PhaseThinking  = "thinking"
	PhaseReading   = "reading"
	PhaseSearching = "searching"
	PhaseEditing   = "editing"
	PhaseRunning   = "running"
)

// thinkingPhrases are the exact lines Thinking, Writing and Learning produce.
var thinkingPhrases = map[string]bool{
	"Reading your request":             true,
	"Checking the test results":        true,
	"Going through the search results": true,
	"Checking the build":               true,
	"Wrapping up the handoff":          true,
	"Checking the change":              true,
	"Working out the next step":        true,
	Writing:                            true,
	Learning:                           true,
}

var verbPhase = map[string]string{
	"Reading": PhaseReading, "Opening": PhaseReading, "Checking": PhaseReading,
	"Getting": PhaseReading, "Counting": PhaseReading, "Querying": PhaseReading,
	"Following": PhaseReading, "Listing": PhaseReading, "Showing": PhaseReading,

	"Searching": PhaseSearching, "Looking": PhaseSearching, "Finding": PhaseSearching,
	"Fetching": PhaseSearching, "Browsing": PhaseSearching,

	"Editing": PhaseEditing, "Writing": PhaseEditing, "Creating": PhaseEditing,
	"Deleting": PhaseEditing, "Updating": PhaseEditing, "Renaming": PhaseEditing,
	"Moving": PhaseEditing, "Copying": PhaseEditing, "Applying": PhaseEditing,
	"Adding": PhaseEditing, "Removing": PhaseEditing, "Changing": PhaseEditing,
	"Formatting": PhaseEditing, "Restoring": PhaseEditing, "Drafting": PhaseEditing,
	"Saving": PhaseEditing, "Tidying": PhaseEditing,
}

// PhaseOf says which phase a working-line phrase belongs to: thinking for the
// model's own lines (Thinking, Writing the reply), else by its leading verb, with
// running for everything else (commands, installs, builds, git).
func PhaseOf(plain string) string {
	plain = strings.TrimSpace(plain)
	if plain == "" || thinkingPhrases[plain] || strings.HasPrefix(plain, "Going over ") {
		return PhaseThinking
	}
	verb, _, _ := strings.Cut(plain, " ")
	if p, ok := verbPhase[verb]; ok {
		return p
	}
	return PhaseRunning
}
