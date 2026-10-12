package tools

// about_blaze: Blaze's live report about itself (internal/selfknow). The host (main) sets
// AboutFacts; the agent puts selfknow.Brief in the system prompt as ABOUT YOU.

import "github.com/lacksonkapapahue/blaze-cli/internal/selfknow"

// AboutFacts reads Blaze CLI's live state. full adds the slower parts (connectors, MCP,
// skills, memory) only the tool needs. Set by main.
var AboutFacts func(full bool) selfknow.Facts

// AboutBrief is the ABOUT YOU block ("" before main sets AboutFacts).
func AboutBrief() string {
	if AboutFacts == nil {
		return ""
	}
	return selfknow.Brief(AboutFacts(false))
}

func init() {
	register(selfknow.ToolName, selfknow.ToolDesc,
		[]prop{p("section", "string", "Optional: one section (empty = everything)")}, nil,
		func(e *Env, a args) string {
			if AboutFacts == nil {
				return "Error: Blaze is still starting."
			}
			return selfknow.Report(AboutFacts(true), a.s("section"))
		})
}
