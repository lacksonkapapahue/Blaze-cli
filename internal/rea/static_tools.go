package rea

import "strings"

// staticTools contains only the pinned server's tools that inspect files and
// analysis artifacts already on disk. Everything else requires approval.
var staticTools = map[string]struct{}{
	"analyze_function":                  {},
	"analyze_javascript_application":    {},
	"analyze_swift_types":               {},
	"analyze_web_bundle":                {},
	"compare_application_versions":      {},
	"compare_artifacts":                 {},
	"compare_bundles":                   {},
	"compare_functions":                 {},
	"compare_javascript_export_shapes":  {},
	"compare_managed_members":           {},
	"compare_source_to_bundle":          {},
	"compare_wasm_interfaces":           {},
	"compare_web_screenshots":           {},
	"get_call_graph":                    {},
	"get_evidence_bundle":               {},
	"get_objc_classes":                  {},
	"get_objc_protocols":                {},
	"inspect_address_context":           {},
	"inspect_android_class":             {},
	"inspect_android_method":            {},
	"inspect_android_package":           {},
	"inspect_artifact":                  {},
	"inspect_asset_catalog":             {},
	"inspect_binary_layout":             {},
	"inspect_dart_aot":                  {},
	"inspect_electron_fuses":            {},
	"inspect_evm_interface":             {},
	"inspect_firmware_regions":          {},
	"inspect_go_binary":                 {},
	"inspect_keyed_archive":             {},
	"inspect_macho":                     {},
	"inspect_managed_artifact":          {},
	"inspect_managed_members":           {},
	"inspect_managed_native_boundaries": {},
	"inspect_native_api":                {},
	"inspect_native_data_type":          {},
	"inspect_native_dispatch_metadata":  {},
	"inspect_native_instruction":        {},
	"inspect_native_load_image":         {},
	"inspect_packet_fields":             {},
	"inspect_pe_resources":              {},
	"inspect_plist":                     {},
	"inspect_recorded_crash":            {},
	"inspect_rust_binary":               {},
	"inspect_sqlite_database":           {},
	"inspect_wasm_artifact":             {},
	"inspect_with_objdump":              {},
	"list_architectures":                {},
	"list_bookmarks":                    {},
	"list_documents":                    {},
	"list_names":                        {},
	"list_procedures":                   {},
	"list_segments":                     {},
	"list_strings":                      {},
	"list_unknowns":                     {},
	"procedure_address":                 {},
	"procedure_assembly":                {},
	"procedure_callees":                 {},
	"procedure_callers":                 {},
	"procedure_info":                    {},
	"procedure_pseudo_code":             {},
	"procedure_references":              {},
	"search_android_classes":            {},
	"search_bytes":                      {},
	"search_procedures":                 {},
	"search_strings":                    {},
	"xrefs":                             {},
}

// RiskyToolName identifies REA operations that may interact with live targets
// or install/run external tools. They always require an explicit approval.
func RiskyToolName(name string) bool {
	name = strings.ToLower(name)
	parts := strings.FieldsFunc(name, func(r rune) bool {
		return r < 'a' || r > 'z'
	})
	tokens := make(map[string]bool, len(parts))
	for _, part := range parts {
		tokens[part] = true
	}
	for _, part := range []string{"runtime", "process", "device", "adb", "browser", "frida", "gdb", "cutter", "rizin", "hopper", "ghidra", "ida", "jadx", "install", "setup", "download", "capture", "observe", "attach", "instrument", "launch", "run", "spawn", "execute", "shell", "record", "screenshot", "snapshot", "start", "stop", "terminate", "kill"} {
		if tokens[part] {
			return true
		}
	}
	return tokens["debug"] && tokens["session"] ||
		tokens["electron"] && (tokens["page"] || tokens["targets"]) ||
		tokens["web"] && (tokens["page"] || tokens["event"] && tokens["listeners"]) ||
		tokens["webmcp"]
}

// AutoReadOnlyToolName is an exact allowlist check for the pinned REA catalog.
// It intentionally does not infer safety from tool names or MCP annotations.
func AutoReadOnlyToolName(name string) bool {
	name = strings.ToLower(name)
	if RiskyToolName(name) {
		return false
	}
	_, ok := staticTools[name]
	return ok
}
