package main

// Blaze self-knowledge for Blaze CLI: the ABOUT YOU block in the system prompt and the
// about_blaze tool (internal/selfknow, shared with Blaze Desktop; names match Blaze
// Android's agent/SelfKnowledge.kt). This file reads the CLI's live state into it.

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/cloud"
	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/selfknow"
	"github.com/lacksonkapapahue/blaze-cli/internal/store"
	"github.com/lacksonkapapahue/blaze-cli/internal/sysinfo"
	"github.com/lacksonkapapahue/blaze-cli/internal/tools"
)

func init() { tools.AboutFacts = cliFacts }

var cliInfo *sysinfo.Info

func cliBuild() string {
	parts := []string{runtime.Version(), runtime.GOOS + "/" + runtime.GOARCH}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				parts = append(parts, "commit "+s.Value[:7])
			}
		}
	}
	return strings.Join(parts, " · ")
}

// routerUp says whether Blaze Desktop's BlazeRouter answers on its default port.
func routerUp() bool {
	c, err := net.DialTimeout("tcp", "127.0.0.1:20128", 250*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// mcpNames reads the server names from the shared ~/.blaze/mcp.json.
func mcpNames(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var f struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if json.Unmarshal(b, &f) != nil {
		return nil
	}
	var n []string
	for k := range f.Servers {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

func cliFacts(full bool) selfknow.Facts {
	dir := config.Dir()
	f := selfknow.Facts{App: "Blaze CLI", Version: version, Build: cliBuild(), DataDir: dir}
	f.Account = selfknow.Account{Service: cloud.DefaultBase, Online: true,
		SignIn: "`blaze login` or /login in a chat; /whoami shows who is signed in; /logout signs out here and in Blaze Desktop (they share the sign-in)."}
	if u := (&account{}).cachedUser(); u != nil {
		f.Account.SignedIn, f.Account.Email, f.Account.Name = true, u.Email, u.Name
	}
	f.Account.KeysSync = config.Keys().LastSync()
	f.Link.Devices = selfknow.Devices(dir) // the CLI has no pairing server of its own
	if cliInfo == nil {
		in := sysinfo.Detect()
		cliInfo = &in
	}
	in := cliInfo
	f.System = selfknow.System{OSName: in.OSName, OS: in.OS, Arch: in.Arch, CPU: in.CPU, Cores: in.Cores, RAMGB: in.RAMGB, GPU: in.GPU, Shell: in.Shell, Host: in.Hostname}
	if runtime.GOOS == "linux" {
		f.System.Desktop, f.System.Session = selfknow.DesktopEnv(os.Getenv)
	}
	if !full {
		return f
	}
	if routerUp() {
		f.Router.Note = "Blaze Desktop's BlazeRouter is answering on http://127.0.0.1:20128 right now."
	}
	for _, c := range slashMenuCmds {
		f.Commands = append(f.Commands, selfknow.Entry{Name: strings.TrimPrefix(c.name, "/"), Desc: c.desc})
	}
	if m := getConns(); m != nil {
		for _, c := range m.Views().Connectors {
			f.Connectors = append(f.Connectors, selfknow.Conn{Name: c.Name, Status: c.Status, Account: c.Account})
		}
	}
	f.MCPFile = filepath.Join(dir, "mcp.json")
	for _, n := range mcpNames(f.MCPFile) {
		f.MCP = append(f.MCP, selfknow.MCP{Name: n, Status: "configured"})
	}
	for _, s := range tools.SkillStatus() {
		if s.Enabled {
			f.Skills = append(f.Skills, selfknow.Entry{Name: s.Name, Desc: s.Description})
		} else {
			f.SkillsOff++
		}
	}
	if db, err := store.Default(); err == nil {
		f.Memories, f.MemoryDB = db.CountMemories(), db.Path
	}
	return f
}
