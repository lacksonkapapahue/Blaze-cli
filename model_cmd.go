package main

// /model: pick the model Blaze uses. The choice is saved to config.json the way
// Blaze Desktop's model picker saves it (default + model_for, and first in the
// chain), so it sticks across restarts and both apps agree on it.

import (
	"fmt"
	"strings"

	"github.com/lacksonkapapahue/blaze-cli/internal/agent"
	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/ui"
)

// menuCfg feeds the "/model " sub-menu (set when the REPL starts).
var menuCfg *config.Config

// usable is true when Blaze can call the provider right now (a key, or keyless).
func usable(cfg *config.Config, provider string) bool {
	return config.Keyless(provider) || cfg.Key(provider) != ""
}

// modelName is the banner's "Provider · model": the first route entry that can
// answer, else the first one at all.
func modelName(a *agent.Agent) string {
	first := ""
	for _, e := range a.Cfg.RouteChain() {
		p, ok := config.ProviderByID(e.Provider)
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
	if first != "" {
		return first
	}
	return "no model yet"
}

func entryModel(e config.Entry) string {
	if e.Model != "" {
		return e.Model
	}
	if p, ok := config.ProviderByID(e.Provider); ok {
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

// parseModel turns "provider/model" (or a bare model id) into a chain entry.
// OpenRouter ids contain a slash themselves ("nvidia/nemotron-…:free"), so:
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
	return config.Entry{}, fmt.Errorf("unknown provider %q (use provider/model, e.g. gemini/gemini-3.8-flash or openrouter/vendor/model)", prov)
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
		ui.Step("Auto: Blaze goes down your model chain in order. Saved for next time.")
		return
	}
	e, err := parseModel(a.Cfg, arg)
	if err != nil {
		ui.Step(err.Error())
		return
	}
	if err := a.Cfg.SelectModel(e.Provider, e.Model); err != nil {
		ui.Step("Couldn't save: " + err.Error())
		return
	}
	a.Cfg.Override = nil // a saved pick replaces a --model for this run
	msg := "Using " + entryLabel(e) + " from now on (saved; the rest of your chain stays as fallback)."
	if !usable(a.Cfg, e.Provider) {
		msg += " " + e.Provider + " has no key yet: run /setup to add one."
	}
	ui.Step(msg)
}

func showModels(cfg *config.Config) {
	route := cfg.RouteChain()
	var rows []string
	for i, e := range route {
		row := ui.Ink(e.Provider) + ui.Weak(" "+entryModel(e))
		if i == 0 {
			row += ui.Accent("  ■ in use")
		}
		if !usable(cfg, e.Provider) {
			row += ui.Weak("  (no key)")
		}
		rows = append(rows, row)
	}
	ui.Numbered(rows)
	ui.Println("  " + ui.Weak("Type ") + ui.Accent("/model ") + ui.Weak("to pick one from the list, /model provider/model for another, /model auto for chain order"))
}

// modelMenu is the "/model " sub-menu: your chain (and Auto), filtered as you type.
func modelMenu(rest string, cfg *config.Config) *ui.MenuSet {
	if strings.Contains(rest, " ") {
		return nil
	}
	var all []ui.MenuItem
	route := cfg.RouteChain()
	for i, e := range route {
		if e.Provider == "pollinations" && i == len(route)-1 && i > 0 {
			continue // the keyless last resort, not something to pick
		}
		desc := ""
		if p, ok := config.ProviderByID(e.Provider); ok {
			desc = p.Name
		}
		if i == 0 {
			desc += " · in use"
		}
		if !usable(cfg, e.Provider) {
			desc += " · no key"
		}
		l := entryLabel(e)
		all = append(all, ui.MenuItem{Name: l, Desc: desc, Insert: "/model " + l, Submit: true})
	}
	all = append(all, ui.MenuItem{Name: "auto", Desc: "No fixed model: go down the chain in order", Insert: "/model auto", Submit: true})
	return &ui.MenuSet{Title: "models", Items: ui.Rank(all, rest, nil), Full: len(all)}
}
