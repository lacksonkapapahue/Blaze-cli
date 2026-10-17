package rea

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lacksonkapapahue/blaze-cli/internal/mcp"
)

func TestSupportsNodeVersion(t *testing.T) {
	for _, tc := range []struct {
		name, version string
		want          bool
	}{
		{name: "22.18 fails", version: "v22.18.0", want: false},
		{name: "22.19 ok", version: "v22.19.0", want: true},
		{name: "23.x fails", version: "v23.0.0", want: false},
		{name: "24.10 fails", version: "v24.10.0", want: false},
		{name: "24.11 ok", version: "v24.11.0", want: true},
		{name: "26.0 ok", version: "v26.0.0", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SupportsNodeVersion(tc.version); got != tc.want {
				t.Errorf("SupportsNodeVersion(%q) = %t, want %t", tc.version, got, tc.want)
			}
		})
	}
}

func TestREARiskyToolName(t *testing.T) {
	for name, want := range map[string]bool{
		"capture_browser_scenario":        true,
		"attach_to_process":               true,
		"inspect_android_device":          true,
		"run_process":                     true,
		"gdb_console":                     true,
		"cutter_command":                  true,
		"list_electron_targets":           true,
		"inspect_electron_page":           true,
		"inspect_web_page":                true,
		"inspect_web_event_listeners":     true,
		"discover_webmcp_tools":           true,
		"list_javascript_runtime_targets": true,
		"execute_rizin_command":           true,
		"instrument_with_frida":           true,
		"install_adb_package":             true,
		"install_ghidra":                  true,
		"install_analysis_provider":       true,
		"configure_hopper":                true,
		"capture_process_scenario":        true,
		"analyze_javascript_application":  false,
		"provider_status":                 false,
		"validate_schema":                 false,
		"list_artifacts":                  false,
	} {
		if got := RiskyToolName(name); got != want {
			t.Errorf("RiskyToolName(%q) = %t, want %t", name, got, want)
		}
	}
}

func TestREAApprovalPolicy(t *testing.T) {
	for name, want := range map[string]bool{
		"analyze_javascript_application":  true,
		"inspect_macho":                   true,
		"list_strings":                    true,
		"gdb_console":                     false,
		"cutter_command":                  false,
		"inspect_electron_page":           false,
		"inspect_web_page":                false,
		"list_electron_targets":           false,
		"list_adb_devices":                false,
		"list_frida_processes":            false,
		"list_browser_targets":            false,
		"list_javascript_runtime_targets": false,
		"instrument_with_frida":           false,
		"install_adb_package":             false,
		"install_analysis_provider":       false,
		"capture_process_scenario":        false,
		"extract_artifact":                false,
		"compare_process_captures":        false,
		"trace_feature":                   false,
		"unknown_future_tool":             false,
	} {
		if got := AutoReadOnlyToolName(name); got != want {
			t.Errorf("AutoReadOnlyToolName(%q) = %t, want %t", name, got, want)
		}
	}
}

func TestREAAllowlistedToolsAreInPinnedCatalog(t *testing.T) {
	file, err := os.Open(filepath.Join("testdata", "tools-6.5.0.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	catalog := make(map[string]bool)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		name := strings.TrimSpace(scanner.Text())
		if name != "" {
			catalog[name] = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	for name := range staticTools {
		if !catalog[name] {
			t.Errorf("allowlisted tool %q is missing from the pinned catalog", name)
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
