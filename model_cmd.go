package main

// /model: pick the model Blaze uses. The choice is saved to config.json the way
// Blaze Desktop's model picker saves it (default + model_for, and first in the
// chain), so it sticks across restarts and both apps agree on it.
//
// Blaze Cloud: signed in, the Blaze model (blaze-auto, or blaze-<task>) answers
// first. Picking a Blaze model is saved in ~/.blaze/blaze-cloud.json (not the
// shared config.json, which Desktop reads); picking a model of your own puts it
// first with Blaze Cloud right behind it.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/agent"
	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
	"github.com/lacksonkapapahue/blaze-cli/internal/ui"
)

// menuCfg feeds the "/model " sub-menu (set when the REPL starts).
var menuCfg *config.Config

// usable is true when Blaze can call the provider right now (a key, keyless, or
// Blaze Cloud while signed in).
func usable(cfg *config.Config, provider string) bool { return llm.Usable(cfg, provider) }

func providerName(id string) string {
	if p, ok := llm.ProviderFor(id); ok {
		return p.Name
	}
	return id
}

// modelName is the banner's "Provider · model": the first route entry that can
// answer, else the first one at all.
func modelName(a *agent.Agent) string {
	first := ""
	for _, e := range llm.RouteChain(a.Cfg) {
		p, ok := llm.ProviderFor(e.Provider)
		if !ok {
			continue
		}
		name := p.Name
		if m := entryModel(e); m != "" {
			name += " · " + m
		}
		if usable(a.Cfg, e.Provider) {
			return name
		}
		if first == "" {
			first = name
		}
	}
	if !llm.SignedIn() {
		return "not signed in · blaze login"
	}
	if first != "" {
		return first
	}
	return "no model yet"
}

func entryModel(e config.Entry) string {
	if e.Model != "" {
		return e.Model
	}
	if p, ok := llm.ProviderFor(e.Provider); ok {
		return p.DefaultModel
	}
	return ""
}

func entryLabel(e config.Entry) string {
	if m := entryModel(e); m != "" {
		return e.Provider + "/" + m
	}
	return e.Provider
}

// parseBlaze reads a Blaze Cloud model name: "blaze", "blaze/blaze-code", "blaze-code".
func parseBlaze(s string) (config.Entry, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == llm.BlazeID {
		return config.Entry{Provider: llm.BlazeID, Model: llm.BlazeModel}, true
	}
	m := strings.TrimPrefix(s, llm.BlazeID+"/")
	if llm.IsBlazeModel(m) {
		return config.Entry{Provider: llm.BlazeID, Model: m}, true
	}
	return config.Entry{}, false
}

// parseModel turns "provider/model" (or a bare model id) into a chain entry.
// OpenRouter ids contain a slash themselves ("nvidia/nemotron-…:free"), so:
//  0. a Blaze Cloud model ("blaze-code", "blaze/blaze-auto") is Blaze Cloud;
//  1. an entry already in the chain that the text names wins;
//  2. a known provider prefix that is set up is that provider;
//  3. otherwise, with OpenRouter set up, the whole text is an OpenRouter model
//     (always for ":free" ids, which only OpenRouter uses);
//  4. a known provider prefix even if it isn't set up yet.
func parseModel(cfg *config.Config, s string) (config.Entry, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return config.Entry{}, fmt.Errorf("no model given")
	}
	if b, ok := parseBlaze(s); ok {
		return b, nil
	}
	for _, e := range cfg.Chain {
		if s == entryLabel(e) || s == e.Provider+"/"+e.Model || (e.Model != "" && s == e.Model) {
			return config.Entry{Provider: e.Provider, Model: entryModel(e)}, nil
		}
	}
	prov, m, slashed := strings.Cut(s, "/")
	_, known := config.ProviderByID(prov)
	if !slashed && known {
		return config.Entry{Provider: prov, Model: cfg.Model(prov)}, nil
	}
	orReady := usable(cfg, "openrouter")
	if known && prov == "openrouter" {
		return config.Entry{Provider: prov, Model: m}, nil
	}
	if orReady && strings.HasSuffix(s, ":free") {
		return config.Entry{Provider: "openrouter", Model: s}, nil
	}
	if known && usable(cfg, prov) {
		return config.Entry{Provider: prov, Model: m}, nil
	}
	if orReady && slashed {
		return config.Entry{Provider: "openrouter", Model: s}, nil
	}
	if known {
		return config.Entry{Provider: prov, Model: m}, nil
	}
	return config.Entry{}, fmt.Errorf("unknown provider %q (use blaze-auto, blaze-code…, or provider/model, e.g. gemini/gemini-3.8-flash or openrouter/vendor/model)", prov)
}

func modelCmd(a *agent.Agent, args []string) {
	if len(args) == 0 {
		showModels(a.Cfg)
		return
	}
	arg := strings.Join(args, " ")
	if arg == config.Auto {
		if err := a.Cfg.SelectModel(config.Auto, ""); err != nil {
			ui.Step("Couldn't save: " + err.Error())
			return
		}
		_ = llm.SaveCloudPrefs(llm.CloudPrefs{Model: llm.BlazeModel})
		a.Cfg.Override = nil
		if llm.SignedIn() {
			ui.Step("Auto: Blaze Cloud (blaze-auto) picks the model for each task; your own keys stay behind it. Saved for next time.")
		} else {
			ui.Step("Auto: Blaze goes down your model chain in order. Saved for next time. Sign in (/login) for Blaze Cloud.")
		}
		return
	}
	e, err := parseModel(a.Cfg, arg)
	if err != nil {
		ui.Step(err.Error())
		return
	}
	a.Cfg.Override = nil // a saved pick replaces a --model for this run
	if e.Provider == llm.BlazeID {
		if err := llm.SaveCloudPrefs(llm.CloudPrefs{Model: e.Model}); err != nil {
			ui.Step("Couldn't save: " + err.Error())
			return
		}
		msg := "Using Blaze Cloud · " + e.Model + " from now on (saved; your own keys stay behind it as fallback)."
		if !llm.SignedIn() {
			msg += " Sign in first: /login."
		}
		ui.Step(msg)
		return
	}
	if err := a.Cfg.SelectModel(e.Provider, e.Model); err != nil {
		ui.Step("Couldn't save: " + err.Error())
		return
	}
	prefs := llm.LoadCloudPrefs()
	prefs.OwnFirst = true
	_ = llm.SaveCloudPrefs(prefs)
	msg := "Using " + entryLabel(e) + " from now on (saved; the rest of your chain stays as fallback)."
	if llm.SignedIn() {
		msg = "Using " + entryLabel(e) + " first from now on (saved); Blaze Cloud stays right behind it. /model auto puts Blaze Cloud first again."
	}
	if !usable(a.Cfg, e.Provider) {
		msg += " " + e.Provider + " has no key yet: run /setup to add one."
	}
	ui.Step(msg)
}

func showModels(cfg *config.Config) {
	route := llm.RouteChain(cfg)
	var rows []string
	for i, e := range route {
		row := ui.Ink(e.Provider) + ui.Weak(" "+entryModel(e))
		if e.Provider == llm.BlazeID {
			row = ui.Ink("Blaze Cloud") + ui.Weak(" "+entryModel(e)+" · picks the best model per task")
		}
		if i == 0 {
			row += ui.Accent("  ■ in use")
		}
		if !usable(cfg, e.Provider) {
			row += ui.Weak("  (no key)")
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		ui.Println("  " + ui.Sub("No model yet. ") + ui.Accent("/login") + ui.Sub(" to use Blaze Cloud (free, no API keys)."))
	} else {
		ui.Numbered(rows)
	}
	if llm.SignedIn() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		ids, live := llm.BlazeModels(ctx)
		cancel()
		cur := llm.LoadCloudPrefs().Model
		var bl []string
		for _, id := range ids {
			r := ui.Ink(id) + ui.Weak("  "+llm.BlazeModelDesc(id))
			if id == cur {
				r += ui.Accent("  ■")
			}
			bl = append(bl, r)
		}
		label := "blaze cloud models"
		if !live {
			label += " (offline list)"
		}
		ui.Box(ui.AccentLabel(label), bl)
	} else {
		ui.Println("  " + ui.Sub("Not signed in: ") + ui.Accent("/login") + ui.Sub(" adds Blaze Cloud (blaze-auto and a model per task), no API keys needed."))
	}
	ui.Println("  " + ui.Weak("Type ") + ui.Accent("/model ") + ui.Weak("to pick from the list · /model blaze-code for a Blaze task model · /model provider/model for your own key · /model auto for Blaze Cloud first"))
}

// modelMenu is the "/model " sub-menu: Blaze Cloud's models, your chain (and Auto),
// filtered as you type. No network here: the Blaze list is the cached one.
func modelMenu(rest string, cfg *config.Config) *ui.MenuSet {
	if strings.Contains(rest, " ") {
		return nil
	}
	var all []ui.MenuItem
	route := llm.RouteChain(cfg)
	inUse := config.Entry{}
	if len(route) > 0 {
		inUse = route[0]
	}
	signedIn := llm.SignedIn()
	for _, id := range llm.CachedBlazeModels() {
		desc := "Blaze Cloud · " + llm.BlazeModelDesc(id)
		if inUse.Provider == llm.BlazeID && inUse.Model == id {
			desc += " · in use"
		}
		if !signedIn {
			desc += " · sign in"
		}
		all = append(all, ui.MenuItem{Name: id, Desc: desc, Insert: "/model " + id, Submit: true})
	}
	for i, e := range route {
		if e.Provider == llm.BlazeID {
			continue
		}
		if e.Provider == "pollinations" && i == len(route)-1 && i > 0 {
			continue // the keyless last resort, not something to pick
		}
		desc := providerName(e.Provider)
		if i == 0 {
			desc += " · in use"
		}
		if !usable(cfg, e.Provider) {
			desc += " · no key"
		}
		l := entryLabel(e)
		all = append(all, ui.MenuItem{Name: l, Desc: desc, Insert: "/model " + l, Submit: true})
	}
	all = append(all, ui.MenuItem{Name: "auto", Desc: "Blaze Cloud first, then your chain in order", Insert: "/model auto", Submit: true})
	return &ui.MenuSet{Title: "models", Items: ui.Rank(all, rest, nil), Full: len(all)}
}
