package setup

import (
	"testing"

	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
)

func TestSignInStep(t *testing.T) {
	old, oldSign, oldUser := llm.SessionToken, SignIn, AccountUser
	defer func() { llm.SessionToken, SignIn, AccountUser = old, oldSign, oldUser }()
	tok := ""
	llm.SessionToken = func() string { return tok }
	AccountUser = func() string { return "Ada <ada@example.com>" }
	calls := 0
	SignIn = func() bool { calls++; tok = "bz_new"; return true }

	// Signed out: the card, then the login flow inline (no question when ask is off).
	if !SignInStep(false) || calls != 1 {
		t.Fatalf("signed out: calls=%d", calls)
	}
	// Signed in: no login flow again.
	if !SignInStep(false) || calls != 1 {
		t.Fatalf("signed in: calls=%d", calls)
	}
	// A login that doesn't finish stays signed out.
	tok = ""
	SignIn = func() bool { return false }
	if SignInStep(false) {
		t.Fatal("cancelled login reported signed in")
	}
}
