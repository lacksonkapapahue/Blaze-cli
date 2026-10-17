package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lacksonkapapahue/blaze-cli/internal/agents"
	"github.com/lacksonkapapahue/blaze-cli/internal/connect"
	"github.com/lacksonkapapahue/blaze-cli/internal/llm"
	"github.com/lacksonkapapahue/blaze-cli/internal/tools"
)

func TestAllToolSchemasAreProviderSafe(t *testing.T) {
	all := append([]llm.Tool(nil), tools.All()...)
	all = append(all, agents.MainTools()...)
	all = append(all, agents.SpecTools()...)
	all = append(all, laneTools()...)
	all = append(all, updateBlazeTool(), notifyTool())

	connectorManager := connect.New(t.TempDir(), &connect.MemSecrets{}, connect.Host{})
	for _, connectorTool := range connect.BuiltinTools() {
		_ = connectorManager.Store().SetToken(connectorTool.Connector, &connect.Token{AccessToken: "test"})
		connectorManager.NoteUsed(connectorTool.Name, "")
	}
	all = append(all, connectorManager.Tools()...)

	for _, tool := range all {
		t.Run(tool.Function.Name, func(t *testing.T) {
			params := tool.Function.Parameters
			if params == nil {
				t.Fatal("parameters are nil")
			}
			if params["type"] != "object" {
				t.Errorf("schema type = %v, want object", params["type"])
			}
			properties, ok := params["properties"]
			if !ok || properties == nil {
				t.Error("schema properties are missing or nil")
			}
			if required, ok := params["required"]; ok && required == nil {
				t.Error("schema required is nil")
			}
			raw, err := json.Marshal(params)
			if err != nil {
				t.Fatal(err)
			}
			for _, invalid := range []string{`"required":null`, `"properties":null`} {
				if strings.Contains(string(raw), invalid) {
					t.Errorf("schema contains %s: %s", invalid, raw)
				}
			}
			if tool.Function.Name == "" {
				t.Error(fmt.Sprintf("tool has no name: %s", raw))
			}
		})
	}
}
