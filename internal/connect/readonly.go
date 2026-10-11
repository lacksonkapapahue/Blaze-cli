package connect

// ReadOnly says whether a connector tool only reads (the desktop's Plan mode keeps
// these and drops the rest): the two meta tools, Read-risk tools and MCP tools that
// declare readOnlyHint.
func (m *Manager) ReadOnly(name string) bool {
	if m == nil {
		return false
	}
	if name == metaLoad || name == metaSuggest {
		return true
	}
	if t := m.tools[name]; t != nil {
		return t.Risk == Read
	}
	for _, s := range m.store.MCPList() {
		for _, t := range s.Tools {
			if mcpToolName(s.ID, t.Name) == name {
				return mcpRisk(t) == Read
			}
		}
	}
	return false
}
