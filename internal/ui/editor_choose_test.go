package ui

import (
	"strings"
	"testing"
)

func TestChosenLabelOutOfRangeIsCancelled(t *testing.T) {
	opts := []string{"Yes", "No"}
	for _, sel := range []int{-1, -5, 2, 9} {
		got := ansi.ReplaceAllString(chosenLabel(opts, sel), "")
		if got != "cancelled" {
			t.Fatalf("sel %d: got %q, want cancelled", sel, got)
		}
	}
	if got := ansi.ReplaceAllString(chosenLabel(opts, 1), ""); !strings.Contains(got, "No") {
		t.Fatalf("sel 1: got %q", got)
	}
	if got := ansi.ReplaceAllString(chosenLabel(nil, 0), ""); got != "cancelled" {
		t.Fatalf("empty options: got %q", got)
	}
}

func TestChooseNoOptionsReturnsCancel(t *testing.T) {
	if got := Choose("? ", nil, 0, -1); got != -1 {
		t.Fatalf("got %d, want -1", got)
	}
}
