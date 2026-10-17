// Package rea manages Blaze's pinned REA MCP server registration.
package rea

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/mcp"
)

const (
	ServerName = "rea"
	Package    = "rea-agents@6.5.0"
)

var nodeVersionRE = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:[-+]([0-9A-Za-z.-]+))?$`)

type Status struct {
	NodeVersion   string
	NodeFound     bool
	NodeSupported bool
	NodeError     string
	NpxPath       string
	Registered    bool
	Procps        bool
	ConfigError   string
}

func SupportsNodeVersion(version string) bool {
	m := nodeVersionRE.FindStringSubmatch(strings.TrimSpace(version))
	if m == nil || m[4] != "" {
		return false
	}
	var major, minor int
	if _, err := fmt.Sscanf(m[1], "%d", &major); err != nil {
		return false
	}
	if _, err := fmt.Sscanf(m[2], "%d", &minor); err != nil {
		return false
	}
	return major >= 26 || major == 24 && minor >= 11 || major == 22 && minor >= 19
}

func Check(home string) Status {
	s := Status{}
	node, err := exec.LookPath("node")
	if err == nil {
		s.NodeFound = true
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		out, runErr := exec.CommandContext(ctx, node, "--version").Output()
		if runErr != nil {
			s.NodeError = runErr.Error()
		} else {
			s.NodeVersion = strings.TrimSpace(string(out))
			s.NodeSupported = SupportsNodeVersion(s.NodeVersion)
		}
	} else {
		s.NodeError = "node is not on PATH"
	}
	s.NpxPath, _ = exec.LookPath("npx")
	if runtime.GOOS == "linux" {
		s.Procps = hasProcpsPS()
	} else {
		s.Procps = true
	}
	servers, warnings := mcp.Load(home, "")
	s.ConfigError = strings.Join(warnings, "; ")
	for _, server := range servers {
		if server.Name == ServerName {
			s.Registered = true
			break
		}
	}
	return s
}

func hasProcpsPS() bool {
	ps, err := exec.LookPath("ps")
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, ps, "--version").CombinedOutput()
	return err == nil && strings.Contains(strings.ToLower(string(out)), "procps")
}

func (s Status) Ready() bool {
	return s.NodeSupported && s.NpxPath != "" && s.Procps
}

func (s Status) Fixes(goos string) []string {
	var fixes []string
	if !s.NodeSupported {
		switch goos {
		case "darwin":
			fixes = append(fixes, "brew install node@22 && brew link --overwrite node@22")
		case "windows":
			fixes = append(fixes, "winget install OpenJS.NodeJS.LTS")
		default:
			fixes = append(fixes, "nvm install 22 && nvm use 22")
		}
	}
	if s.NpxPath == "" {
		fixes = append(fixes, "npm install --global npm")
	}
	if goos == "linux" && !s.Procps {
		switch {
		case commandExists("apt-get"):
			fixes = append(fixes, "sudo apt-get install procps")
		case commandExists("dnf"):
			fixes = append(fixes, "sudo dnf install procps-ng")
		case commandExists("yum"):
			fixes = append(fixes, "sudo yum install procps-ng")
		case commandExists("apk"):
			fixes = append(fixes, "sudo apk add procps")
		default:
			fixes = append(fixes, "install procps with your Linux package manager (it provides `ps`)")
		}
	}
	if !s.Registered {
		fixes = append(fixes, "blaze rea enable")
	}
	return fixes
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func RiskyToolName(name string) bool {
	name = strings.ToLower(name)
	terms := []string{"runtime", "process", "capture", "observe", "attach", "device", "adb", "browser", "launch", "run", "spawn", "execute", "shell", "record", "screenshot", "snapshot", "instrument", "hook", "debug", "trace", "probe", "simulate", "emulate", "replay", "start", "stop", "terminate", "kill", "install", "setup", "download", "hopper", "ghidra", "ida", "jadx"}
	for _, part := range strings.FieldsFunc(name, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		for _, term := range terms {
			if part == term || term != "ida" && strings.HasPrefix(part, term) {
				return true
			}
		}
	}
	return false
}

func AutoReadOnlyToolName(name string) bool {
	if RiskyToolName(name) {
		return false
	}
	name = strings.ToLower(name)
	for _, prefix := range []string{"inspect", "analyze", "list"} {
		if name == prefix || strings.HasPrefix(name, prefix+"_") {
			return true
		}
	}
	return false
}

func GlobalFile(home string) string { return mcp.GlobalFile(home) }

// EnableFile adds the exact pinned server definition to a global MCP config.
// Existing non-REA servers are preserved. An already-correct entry is a no-op.
func EnableFile(file string) (backup string, changed bool, err error) {
	home := filepath.Dir(file)
	servers, warnings := mcp.Load(home, "")
	if len(warnings) > 0 {
		return "", false, fmt.Errorf("couldn't read the global MCP config: %s", strings.Join(warnings, "; "))
	}
	for _, server := range servers {
		if server.Name == ServerName && !server.Disabled && server.Command == "npx" && equalArgs(server.Args, []string{"-y", Package, "mcp"}) {
			return "", false, nil
		}
	}
	if backup, err = Backup(file); err != nil {
		return "", false, err
	}
	if err = mcp.Add(file, mcp.Server{
		Name:    ServerName,
		Command: "npx",
		Args:    []string{"-y", Package, "mcp"},
	}); err != nil {
		return backup, false, err
	}
	return backup, true, nil
}

// DisableFile removes only the REA entry from a global MCP config.
func DisableFile(file string) (backup string, changed bool, err error) {
	servers, warnings := mcp.Load(filepath.Dir(file), "")
	if len(warnings) > 0 {
		return "", false, fmt.Errorf("couldn't read the global MCP config: %s", strings.Join(warnings, "; "))
	}
	found := false
	for _, server := range servers {
		if server.Name == ServerName {
			found = true
			break
		}
	}
	if !found {
		return "", false, nil
	}
	if backup, err = Backup(file); err != nil {
		return "", false, err
	}
	changed, err = mcp.Remove(file, ServerName)
	return backup, changed, err
}

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Backup copies a config to a private, timestamped sibling before it is edited.
func Backup(file string) (string, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	for i := 0; ; i++ {
		suffix := stamp
		if i > 0 {
			suffix = fmt.Sprintf("%s-%d", stamp, i)
		}
		backup := file + ".bak-" + suffix
		f, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, writeErr := f.Write(data)
		closeErr := f.Close()
		if writeErr != nil {
			return backup, writeErr
		}
		if closeErr != nil {
			return backup, closeErr
		}
		return backup, nil
	}
}
