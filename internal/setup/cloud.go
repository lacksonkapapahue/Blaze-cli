package setup

// Blaze Cloud in the wizard and the doctor: sign in to the Blaze account and every
// model call goes through the Blaze Gateway (internal/llm/blaze.go). Own API keys
// stay an optional, advanced extra tried after it.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
	"github.com/lacksonkapapahue/blaze-cli/internal/ui"
)

// Account hooks, set by the host (package main, blaze_cloud.go).
var (
	// AccountUser is the signed-in account as "Name <email>" ("" when unknown).
	AccountUser func() string
	// SignIn runs the `blaze login` flow inline (email / Google / GitHub / create
	// account) and reports whether Blaze is signed in afterwards.
	SignIn func() bool
)

func accountUser() string {
	if AccountUser == nil {
		return ""
	}
	return AccountUser()
}

// SignInCard is the "Sign in to Blaze to start" card.
func SignInCard() {
	ui.Println("")
	ui.Box(ui.AccentLabel("blaze"), []string{
		ui.Bold("Sign in to Blaze to start") + ui.Weak("  ·  free, no API keys"),
		"",
		ui.Sub("One Blaze account for the CLI, Blaze Desktop and your phone."),
		ui.Sub("Blaze Cloud picks a strong model for every task; nothing to set up."),
	})
}

// SignInStep makes sure Blaze is signed in: already signed in says so; otherwise
// it shows the card and runs the sign-in (after a Yes when ask is set).
func SignInStep(ask bool) bool {
	if llm.SignedIn() {
		who := accountUser()
		if who == "" {
			who = "your Blaze account"
		}
		ui.StepDone(true, "Signed in as "+who, "Blaze Cloud answers · no API keys needed", 0)
		return true
	}
	SignInCard()
	if SignIn == nil {
		ui.Println("  " + ui.Sub("Run ") + ui.Accent("blaze login") + ui.Sub(" to sign in."))
		return false
	}
	if ask && !yes("Sign in now?", true) {
		ui.Println("  " + ui.Sub("Later: ") + ui.Accent("blaze login"))
		return false
	}
	return SignIn() && llm.SignedIn()
}

// cloudDoctor checks Blaze Cloud first: signed in, and the gateway answers.
func cloudDoctor(add func(string, bool, string)) {
	if !llm.SignedIn() {
		add("Blaze Cloud", false, "not signed in: run blaze login (free, no API keys)")
		return
	}
	p := llm.BlazeProvider()
	model := llm.LoadCloudPrefs().Model
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	t0 := time.Now()
	r, err := llm.Chat(ctx, p, llm.Token(), model, []llm.Message{{Role: "user", Content: "Reply with OK."}}, nil, nil)
	if err != nil {
		add("Blaze Cloud", false, trunc(llm.Plain(err), 110))
		return
	}
	d := fmt.Sprintf("%s answered in %.1fs", strings.TrimPrefix(r.Route, "Blaze · "), time.Since(t0).Seconds())
	if r.Served.Remaining != "" {
		d += " · " + r.Served.Remaining + " requests left today"
	}
	who := accountUser()
	if who != "" {
		d += " · " + who
	}
	add("Blaze Cloud", true, trunc(d, 110))
}

// ownModelsDoctor checks the user's own keys (optional; tried after Blaze Cloud).
func ownModelsDoctor(cfg *config.Config, add func(string, bool, string)) {
	signedIn := llm.SignedIn()
	for _, e := range llm.RouteChain(cfg) {
		if e.Provider == llm.BlazeID {
			continue
		}
		p, ok := config.ProviderByID(e.Provider)
		if !ok {
			continue
		}
		name := p.Name
		if signedIn {
			name += " (own)"
		}
		key := cfg.Key(p.ID)
		if key == "" && !config.Keyless(p.ID) {
			// Optional when signed in: a missing key is a note, not a failure.
			add(name, signedIn, "no key saved (optional): blaze setup › your own keys")
			continue
		}
		model := e.Model
		if model == "" {
			model = p.DefaultModel
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		t0 := time.Now()
		_, err := llm.Chat(ctx, p, key, model, []llm.Message{{Role: "user", Content: "Reply with OK."}}, nil, nil)
		cancel()
		if err != nil {
			add(name, false, model+": "+trunc(err.Error(), 90))
		} else {
			add(name, true, fmt.Sprintf("%s answered in %.1fs", model, time.Since(t0).Seconds()))
		}
	}
}
