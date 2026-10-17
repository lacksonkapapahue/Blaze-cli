package steps

import "testing"

func TestPhaseOf(t *testing.T) {
	cases := map[string]string{
		"":                               PhaseThinking,
		"Reading your request":           PhaseThinking,
		Writing:                          PhaseThinking,
		Thinking(2, "Reading main.go"):   PhaseThinking,
		Thinking(2, "Running the tests"): PhaseThinking,
		"Reading main.go":                PhaseReading,
		"Searching the web for \"x\"":    PhaseSearching,
		"Editing main.go":                PhaseEditing,
		"Writing notes.md":               PhaseEditing,
		"Running the tests":              PhaseRunning,
		"Installing ripgrep":             PhaseRunning,
	}
	for in, want := range cases {
		if got := PhaseOf(in); got != want {
			t.Errorf("PhaseOf(%q) = %q, want %q", in, got, want)
		}
	}
	// every thinking line is a thinking phase
	for _, last := range []string{"", "Running the tests", "Searching the web for x", "Building the app", "Briefing an agent", "Reading a.go", "Editing a.go", "Installing x"} {
		if p := PhaseOf(Thinking(1, last)); p != PhaseThinking {
			t.Errorf("Thinking(1,%q) = %q is %s", last, Thinking(1, last), p)
		}
	}
}
