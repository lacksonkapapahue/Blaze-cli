package rea

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lacksonkapapahue/blaze-cli/internal/mcp"
)

func TestSupportsNodeVersion(t *testing.T) {
	for version, want := range map[string]bool{
		"v22.18.0": false,
		"v22.19.0": true,
		"v23.0.0":  false,
		"v24.10.0": false,
		"v24.11.0": true,
		"v26.0.0":  true,
		"v25.0.0":  false,
	} {
		if got := SupportsNodeVersion(version); got != want {
			t.Errorf("SupportsNodeVersion(%q) = %t, want %t", version, got, want)
		}
	}
}

func TestRiskyToolName(t *testing.T) {
	for name, want := range map[string]bool{
		"capture_browser_scenario": true,
		"attach_to_process":        true,
		"inspect_android_device":   true,
		"run_process":              true,
		"trace_native_calls":       true,
		"install_ghidra":           true,
		"open_ida_session":         true,
		"analyze_javascript":       false,
		"provider_status":          false,
		"validate_schema":          false,
		"list_artifacts":           false,
	} {
		if got := RiskyToolName(name); got != want {
			t.Errorf("RiskyToolName(%q) = %t, want %t", name, got, want)
		}
	}
}

func TestAutoReadOnlyToolName(t *testing.T) {
	for name, want := range map[string]bool{
		"inspect_binary":            true,
		"analyze_javascript":        true,
		"list_artifacts":            true,
		"inspect_android_device":    false,
		"observe_browser_scenario":  false,
		"install_analysis_provider": false,
		"provider_status":           false,
	} {
		if got := AutoReadOnlyToolName(name); got != want {
			t.Errorf("AutoReadOnlyToolName(%q) = %t, want %t", name, got, want)
		}
	}
}

func TestEnableDisableFileBacksUpAndPreservesOtherServers(t *testing.T) {
	home := t.TempDir()
	file := mcp.GlobalFile(home)
	initial := []byte(`{"mcpServers":{"other":{"command":"other","args":["--keep"]}}}` + "\n")
	if err := os.WriteFile(file, initial, 0o600); err != nil {
		t.Fatal(err)
	}

	backup, changed, err := EnableFile(file)
	if err != nil || !changed || backup == "" {
		t.Fatalf("enable = %q, %t, %v", backup, changed, err)
	}
	if got, err := os.ReadFile(backup); err != nil || string(got) != string(initial) {
		t.Fatalf("backup content = %q, %v", got, err)
	}
	if info, err := os.Stat(backup); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %v, %v", info, err)
	}
	if _, changed, err = EnableFile(file); err != nil || changed {
		t.Fatalf("second enable should be a no-op: changed=%t err=%v", changed, err)
	}
	servers, warnings := mcp.Load(home, "")
	if len(warnings) != 0 || len(servers) != 2 {
		t.Fatalf("servers = %+v, warnings = %v", servers, warnings)
	}
	var gotREA mcp.Server
	for _, server := range servers {
		if server.Name == ServerName {
			gotREA = server
		}
	}
	if gotREA.Command != "npx" || !reflect.DeepEqual(gotREA.Args, []string{"-y", Package, "mcp"}) {
		t.Fatalf("REA server = %+v", gotREA)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	if _, ok := document["mcpServers"]["other"]; !ok {
		t.Fatal("enabling REA removed another server")
	}

	backup, changed, err = DisableFile(file)
	if err != nil || !changed || backup == "" {
		t.Fatalf("disable = %q, %t, %v", backup, changed, err)
	}
	servers, warnings = mcp.Load(home, "")
	if len(warnings) != 0 || len(servers) != 1 || servers[0].Name != "other" {
		t.Fatalf("disable did not preserve only the other server: %+v, %v", servers, warnings)
	}
	if strings.Contains(string(raw), `"rea"`) && backup == "" {
		t.Fatal("expected a backup before removing REA")
	}
}

func TestEnableFileCreatesGlobalConfigWithoutExistingFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "mcp.json")
	backup, changed, err := EnableFile(file)
	if err != nil || !changed || backup != "" {
		t.Fatalf("enable new file = %q, %t, %v", backup, changed, err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
}

func TestDisableFileReportsInvalidMCPConfig(t *testing.T) {
	file := mcp.GlobalFile(t.TempDir())
	if err := os.WriteFile(file, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := DisableFile(file); err == nil {
		t.Fatal("expected invalid MCP config to be reported")
	}
}
