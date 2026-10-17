package main

// Blaze Cloud in the CLI: sign in to the Blaze account and every model call goes
// through the Blaze Gateway with the session token (internal/llm/blaze.go). Nobody
// needs API keys; their own keys stay an optional extra after Blaze Cloud.
//
// This file wires the account (account.go) into llm and setup, and holds the
// sign-in gate main() runs before chat or a one-shot task.

import (
	"os"
	"strings"
	"testing"

	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
	"github.com/lacksonkapapahue/blaze-cli/internal/setup"
	"github.com/lacksonkapapahue/blaze-cli/internal/ui"
)

func init() {
	llm.SessionToken = sessionToken
	setup.AccountUser = func() string { return userLine(getAccount().cachedUser()) }
	setup.SignIn = func() bool {
		if err := cmdLogin(nil); err != nil {
			fail(err.Error())
		}
		return llm.SignedIn()
	}
}

// sessionToken is the Blaze session token, read fresh on every model call so a
// login or logout (here, or in Blaze Desktop) applies at once. BLAZE_SESSION_TOKEN
// overrides it (headless servers, CI).
func sessionToken() string {
	if v := strings.TrimSpace(os.Getenv("BLAZE_SESSION_TOKEN")); v != "" {
		return v
	}
	if testing.Testing() {
		return "" // tests never use the developer's real sign-in
	}
	tok, _ := getAccount().sec.Get(sessionKey)
	return strings.TrimSpace(tok)
}

// interactive: a person can answer prompts (stdout and stdin are terminals).
func interactive() bool { return ui.IsTTY() && ui.StdinTTY() }

// blazeGate makes sure Blaze is signed in before chat or a one-shot task. Signed
// out and interactive, it shows the sign-in card and runs the login flow inline;
// non-interactive, it says to run `blaze login`. False means: don't start.
func blazeGate(oneShot bool) bool {
	if llm.SignedIn() {
		return true
	}
	if !interactive() {
		os.Stderr.WriteString("blaze: you're not signed in. Run `blaze login` first (free, no API keys needed).\n")
		return false
	}
	setup.SignInCard()
	if err := cmdLogin(nil); err != nil {
		fail(err.Error())
	}
	if !llm.SignedIn() {
		ui.Println("  " + ui.Sub("Not signed in. Run ") + ui.Accent("blaze login") + ui.Sub(" when you're ready."))
		return false
	}
	if oneShot {
		ui.Println("")
	}
	return true
}

// blazeAfterError follows up a failed turn: an expired Blaze sign-in (gateway 401)
// offers to sign in again right here. Busy, allowance, waiting list and network
// errors only print their line (the session stays).
func blazeAfterError(err error) {
	ge, ok := llm.AsGateway(err)
	if !ok || !ge.Expired() || !interactive() {
		return
	}
	if ui.Choose("  "+ui.Ink("Sign in to Blaze again now?")+"  ", []string{"Sign in", "Later"}, 0, 1) != 0 {
		return
	}
	relogin()
}

// relogin drops the refused session and runs the sign-in again.
func relogin() {
	_ = getAccount().setSession(nil)
	if err := cmdLogin(nil); err != nil {
		fail(err.Error())
		return
	}
	if llm.SignedIn() {
		ui.Println("  " + ui.Sub("Signed in again. Send your message once more."))
	}
}

// cloudRouteRow is whoami's "model" row: what answers chats right now.
func cloudRouteRow() string {
	m := llm.LoadCloudPrefs().Model
	return "model: Blaze Cloud · " + m + " (" + strings.TrimSuffix(llm.GatewayURL(), "/") + ")"
}
