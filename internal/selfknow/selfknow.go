// Package selfknow is Blaze's knowledge of itself: what Blaze is, which apps exist and how
// they connect, this build's version, the Blaze account, BlazeRouter, Blaze Link (phone
// pairing), views, commands, data folders, connectors, MCP servers, skills, memory and the
// PC it runs on. The host (Blaze Desktop or Blaze CLI) fills Facts from live state; this
// package only formats it, so nothing here is invented.
//
// Two outputs, the same names as Blaze Android (agent/SelfKnowledge.kt):
//   - Brief: the compact ABOUT YOU block in every system prompt (a few hundred tokens).
//   - Report: the full self-report the about_blaze tool returns, whole or one section.
package selfknow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ToolName is the tool that returns Report (same name on Android, Desktop and CLI).
const ToolName = "about_blaze"

// ToolDesc is the about_blaze tool's description.
const ToolDesc = "Blaze's full report about itself, read live: what Blaze is and who made it, the Blaze apps (Desktop, CLI, Android) and how they connect, version and build, the Blaze account, Blaze Link phone pairing (steps and status), BlazeRouter, views, slash commands, data folders, connectors, MCP servers, skills, memory and this PC. Use it for any question about Blaze itself, its apps or connecting them. Optional section: " + sectionList + "."

const sectionList = "identity, apps, version, account, link, router, views, commands, data, connectors, mcp, skills, memory, system"

// Sections are the parts Report can return on their own.
var Sections = strings.Split(strings.ReplaceAll(sectionList, " ", ""), ",")

// Rule is the prompt rule that goes with the ABOUT YOU block.
const Rule = `- Questions about Blaze itself, its apps (Blaze Desktop, Blaze CLI, Blaze Android), its features or connecting them (phone and desktop, the CLI, the Blaze account, BlazeRouter) are about YOU. Answer from ABOUT YOU, and call about_blaze for steps or details. Never ask the user which apps they are building or what they mean by "the phone app": it is Blaze.
`

// Worker is the Blaze account service (blaze-auth, cloud/ in the Blaze-dekstop repo).
const Worker = "https://blaze-auth.blaze-app.workers.dev"

// Gateway is the Blaze Gateway (Blaze Cloud): one OpenAI-compatible endpoint signed in
// with the Blaze account; it holds the provider keys and picks the model per task.
const Gateway = "https://ai.blaze-app.workers.dev/v1"

// modelsLine is how Blaze gets its models (Blaze Android v0.25.0+, Blaze CLI).
const modelsLine = "Models come with the account: Blaze Cloud (the Blaze Gateway) picks a strong model for each task, so nobody needs API keys; keys of your own are an optional extra."

// Entry is one named thing with a short description (a view, a command, a preset).
type Entry struct {
	Name string `json:"name"`
	Desc string `json:"desc,omitempty"`
}

// Account is the Blaze account on this computer.
type Account struct {
	SignedIn bool      `json:"signed_in"`
	Email    string    `json:"email,omitempty"`
	Name     string    `json:"name,omitempty"`
	Service  string    `json:"service"`
	Online   bool      `json:"online"`
	KeysSync time.Time `json:"keys_sync,omitempty"` // last model-key sync with the account
	SignIn   string    `json:"sign_in,omitempty"`   // how to sign in from this app
}

// Router is BlazeRouter, the local OpenAI-compatible gateway (Desktop only).
type Router struct {
	Present   bool    `json:"present"` // this app has BlazeRouter
	Running   bool    `json:"running"`
	Enabled   bool    `json:"enabled"`
	InUse     bool    `json:"in_use"` // Blaze's own chats go through it
	URL       string  `json:"url,omitempty"`
	Port      int     `json:"port,omitempty"`
	Dashboard string  `json:"dashboard,omitempty"`
	Ready     int     `json:"ready"`
	Models    int     `json:"models"`
	Note      string  `json:"note,omitempty"`
	Presets   []Entry `json:"presets,omitempty"`
}

// Device is one phone paired with this PC (~/.blaze/pair/devices.json).
type Device struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	PairedAt time.Time `json:"paired_at"`
}

// Link is Blaze Link: the direct phone ↔ PC connection.
type Link struct {
	InBuild bool     `json:"in_build"` // this build has the PC side (pairing server + Phone view)
	Devices []Device `json:"devices"`
	Online  string   `json:"online,omitempty"` // a paired phone that checked in recently
}

// Conn is a connected account or connector.
type Conn struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Account string `json:"account,omitempty"`
}

// MCP is one MCP server.
type MCP struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Tools  int    `json:"tools"`
}

// System is the PC Blaze runs on.
type System struct {
	OSName  string   `json:"os_name"`
	OS      string   `json:"os"`
	Arch    string   `json:"arch"`
	CPU     string   `json:"cpu,omitempty"`
	Cores   int      `json:"cores"`
	RAMGB   float64  `json:"ram_gb"`
	GPU     []string `json:"gpu,omitempty"`
	Shell   string   `json:"shell,omitempty"`
	Desktop string   `json:"desktop,omitempty"` // Hyprland, GNOME, KDE… (Linux)
	Session string   `json:"session,omitempty"` // wayland or x11 (Linux)
	Host    string   `json:"host,omitempty"`
}

// Facts is everything the host read live.
type Facts struct {
	App         string  `json:"app"` // "Blaze Desktop" or "Blaze CLI"
	Version     string  `json:"version"`
	Build       string  `json:"build,omitempty"` // Go version, commit
	DataDir     string  `json:"data_dir"`
	Account     Account `json:"account"`
	Router      Router  `json:"router"`
	Link        Link    `json:"link"`
	Views       []Entry `json:"views,omitempty"`
	Commands    []Entry `json:"commands,omitempty"`
	Connectors  []Conn  `json:"connectors,omitempty"`
	MCP         []MCP   `json:"mcp,omitempty"`
	MCPFile     string  `json:"mcp_file,omitempty"`
	Skills      []Entry `json:"skills,omitempty"` // enabled skills
	SkillsOff   int     `json:"skills_off"`
	Memories    int     `json:"memories"`
	MemoryDB    string  `json:"memory_db,omitempty"`
	Model       string  `json:"model,omitempty"`
	PhonePrev   bool    `json:"phone_preview"` // Design › Phone preview exists in this app
	System      System  `json:"system"`
	MemoryNotes string  `json:"-"`
}

// ---------------- reading shared state ----------------

// Devices reads the phones paired with this PC from dir/pair/devices.json (the file Blaze
// Link's PC side writes; shared by Desktop and CLI). Missing file = none.
func Devices(dir string) []Device {
	b, err := os.ReadFile(filepath.Join(dir, "pair", "devices.json"))
	if err != nil {
		return nil
	}
	var ds []Device
	if json.Unmarshal(b, &ds) != nil {
		var m map[string]Device // tolerate an object keyed by id
		if json.Unmarshal(b, &m) != nil {
			return nil
		}
		for id, d := range m {
			if d.ID == "" {
				d.ID = id
			}
			ds = append(ds, d)
		}
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i].PairedAt.Before(ds[j].PairedAt) })
	return ds
}

// DesktopEnv is the Linux desktop and session type from the environment ("", "" elsewhere).
func DesktopEnv(getenv func(string) string) (desktop, session string) {
	if getenv == nil {
		getenv = os.Getenv
	}
	desktop = getenv("XDG_CURRENT_DESKTOP")
	if desktop == "" {
		desktop = getenv("DESKTOP_SESSION")
	}
	if getenv("HYPRLAND_INSTANCE_SIGNATURE") != "" && !strings.Contains(strings.ToLower(desktop), "hypr") {
		desktop = strings.Trim(desktop+":Hyprland", ":")
	}
	session = strings.ToLower(getenv("XDG_SESSION_TYPE"))
	if session == "" && getenv("WAYLAND_DISPLAY") != "" {
		session = "wayland"
	} else if session == "" && getenv("DISPLAY") != "" {
		session = "x11"
	}
	return desktop, session
}

// known describes the entries Blaze keeps in its data folder.
var known = map[string]string{
	"blaze.db":        "memories, chat search and tool index (SQLite, shared by Desktop and CLI)",
	"config.json":     "models, route chain and settings (shared by Desktop and CLI)",
	"keys.json":       "model API keys when the OS keychain is unavailable (shared; syncs with the Blaze account)",
	"keys.meta.json":  "which model keys exist and when they last synced",
	"skills":          "skills, one folder with a SKILL.md each (shared by Desktop and CLI)",
	"mcp.json":        "MCP servers in the standard mcpServers shape (shared by Desktop and CLI)",
	"desktop.json":    "Blaze Desktop preferences",
	"desktop":         "Blaze Desktop's own files: account, chats, projects, BlazeRouter, connectors, Design",
	"cli":             "Blaze CLI's own files: sessions, checkpoints",
	"pair":            "Blaze Link: paired phones (devices.json) and this PC's certificate",
	"logs":            "scheduled job logs",
	"schedule.json":   "scheduled jobs",
	"notify.json":     "notification settings",
	"memory.md":       "old memory file (imported into blaze.db)",
	"designs":         "Design versions",
	"whisper":         "local voice model (Whisper)",
	"connectors.json": "connected accounts (tokens are in the OS keychain)",
}

// Layout lists what is really in the data folder, with what each entry is for.
func Layout(dir string) []Entry {
	es, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Entry
	for _, e := range es {
		n := e.Name()
		if strings.HasPrefix(n, ".") || strings.HasSuffix(n, "-wal") || strings.HasSuffix(n, "-shm") {
			continue
		}
		name := filepath.Join(dir, n)
		if e.IsDir() {
			name += string(filepath.Separator)
		}
		out = append(out, Entry{Name: name, Desc: known[n]})
	}
	return out
}

// ---------------- the ABOUT YOU block ----------------

func onOff(b bool, on, off string) string {
	if b {
		return on
	}
	return off
}

// Brief is the compact ABOUT YOU block for the system prompt.
func Brief(f Facts) string {
	var b strings.Builder
	b.WriteString("\nABOUT YOU\n")
	fmt.Fprintf(&b, "- You are Blaze, running here as %s %s. Blaze is made by Luke (Lackson Kapapa, Blazeinchue). Three apps: Blaze Desktop (Windows/macOS/Linux), Blaze CLI (terminal) and Blaze Android (phone).\n", f.App, nz(f.Version, "dev"))
	b.WriteString("- How they connect: sign in to the same Blaze account in each app. " + modelsLine + " Desktop and CLI on one computer share " + nz(f.DataDir, "~/.blaze") + " (keys, skills, memory, MCP).\n")
	b.WriteString("- Phone ↔ PC (Blaze Link): " + linkLine(f) + "\n")
	if f.Router.Present {
		fmt.Fprintf(&b, "- BlazeRouter: %s, %s (dashboard /dashboard).\n", onOff(f.Router.Running, "on", "off"), nz(f.Router.URL, "http://127.0.0.1:20128"))
	}
	fmt.Fprintf(&b, "- Blaze account: %s.\n", accountLine(f.Account))
	fmt.Fprintf(&b, "- Running on %s.\n", sysLine(f.System))
	b.WriteString("- For steps, settings, views, commands, skills or data folders, call " + ToolName + ".\n")
	b.WriteString(Rule)
	return b.String()
}

func accountLine(a Account) string {
	if a.SignedIn {
		who := nz(a.Email, a.Name)
		return "signed in as " + nz(who, "a Blaze user")
	}
	return "not signed in"
}

func linkLine(f Facts) string {
	if !f.Link.InBuild {
		s := "the phone side ships in Blaze Android (Settings › Blaze Desktop, scan the PC's QR code), but this " + f.App + " build has no PC side yet, so a phone can't pair with it today."
		if len(f.Link.Devices) > 0 {
			s += fmt.Sprintf(" %d phone(s) were paired by an earlier build.", len(f.Link.Devices))
		}
		return s
	}
	if len(f.Link.Devices) == 0 {
		return "no phone paired. Pair from Phone › Show pairing QR code, then scan it in Blaze Android › Settings › Blaze Desktop."
	}
	return fmt.Sprintf("%d phone(s) paired (%s)%s.", len(f.Link.Devices), deviceNames(f.Link.Devices), onOff(f.Link.Online != "", ", "+f.Link.Online+" online", ""))
}

func deviceNames(ds []Device) string {
	n := make([]string, 0, len(ds))
	for _, d := range ds {
		n = append(n, nz(d.Name, d.ID))
	}
	return strings.Join(n, ", ")
}

func sysLine(s System) string {
	parts := []string{nz(s.OSName, s.OS) + " " + s.Arch}
	if s.Desktop != "" || s.Session != "" {
		parts = append(parts, strings.TrimSpace(s.Desktop+" "+s.Session))
	}
	if s.CPU != "" {
		parts = append(parts, s.CPU)
	}
	if s.RAMGB > 0 {
		parts = append(parts, fmt.Sprintf("%.0f GB RAM", s.RAMGB))
	}
	return strings.Join(parts, ", ")
}

func nz(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

// ---------------- the full report ----------------

// Report is the about_blaze answer: every section, or the one asked for.
func Report(f Facts, section string) string {
	section = strings.ToLower(strings.TrimSpace(section))
	if section == "" || section == "all" {
		var b strings.Builder
		b.WriteString("BLAZE SELF-REPORT (read live from this " + f.App + ")\n")
		for _, s := range Sections {
			b.WriteString("\n" + one(f, s))
		}
		return b.String()
	}
	for _, s := range Sections {
		if s == section || strings.HasPrefix(s, section) {
			return one(f, s)
		}
	}
	switch section {
	case "phone", "pair", "pairing", "blaze link", "mobile", "android":
		return one(f, "link")
	case "blazerouter", "models", "presets":
		return one(f, "router")
	case "os", "pc", "hardware":
		return one(f, "system")
	case "dirs", "folders", "paths", "files":
		return one(f, "data")
	case "slash", "slash commands":
		return one(f, "commands")
	}
	return "Error: unknown section " + section + ". Use one of: " + sectionList + "."
}

func one(f Facts, s string) string {
	var b strings.Builder
	h := func(t string) { b.WriteString(strings.ToUpper(t) + "\n") }
	li := func(format string, a ...any) { b.WriteString("- " + fmt.Sprintf(format, a...) + "\n") }
	switch s {
	case "identity":
		h("Identity")
		li("Blaze: an AI assistant that acts on your own devices (runs commands, edits files, builds things, remembers you).")
		li("Made by Luke (Lackson Kapapa) of Blazeinchue, a computer science student at Northrise University.")
		li("This app: %s %s.", f.App, nz(f.Version, "dev"))
		if f.Model != "" {
			li("Current model: %s (Blaze Cloud picks per task when signed in; it may fall back when one is busy).", f.Model)
		}
	case "apps":
		h("Apps and how they connect")
		li("Blaze Desktop: Windows, macOS, Linux app (Go + Wails). github.com/lacksonkapapahue/Blaze-dekstop")
		li("Blaze CLI: the same Blaze in a terminal (`blaze`). github.com/lacksonkapapahue/Blaze-cli")
		li("Blaze Android: the phone app. github.com/lacksonkapapahue/Blaze-agent")
		li("Blaze account (all three): sign in with the same account in each app. %s Own model keys, if any, sync encrypted through %s. The account service also keeps custom skills for Blaze Android.", modelsLine, nz(f.Account.Service, Worker))
		li("Desktop + CLI on one computer: they share %s (model keys, skills, memory in blaze.db, MCP servers), so what you set up in one works in the other at once.", nz(f.DataDir, "~/.blaze"))
		li("Phone + PC directly (Blaze Link): see LINK below.")
		if f.PhonePrev {
			li("Previewing a design on the phone: Design › Phone shows a QR code; scan it with the phone's camera (same Wi-Fi) to open the live preview and leave comments.")
		}
	case "version":
		h("Version")
		li("%s %s", f.App, nz(f.Version, "dev"))
		if f.Build != "" {
			li("Build: %s", f.Build)
		}
		li("Updates: /update (Settings › Updates) checks GitHub releases.")
	case "account":
		h("Blaze account")
		li("Service: %s (%s)", nz(f.Account.Service, Worker), onOff(f.Account.Online, "reachable", "not reachable right now"))
		li("State: %s", accountLine(f.Account))
		if !f.Account.KeysSync.IsZero() {
			li("Model keys last synced: %s", f.Account.KeysSync.Format("2 Jan 2006 15:04"))
		}
		li("What it gives: Blaze Cloud models (no API keys needed; the gateway at %s picks a model per task, with a free daily allowance), your own model keys if you added any (encrypted sync across Desktop, CLI and Android), and the sign-in for one-click connectors. Chats, memory and connector tokens stay on this computer.", Gateway)
		if f.Account.SignIn != "" {
			li("Sign in: %s", f.Account.SignIn)
		}
	case "link":
		h("Blaze Link (phone ↔ PC)")
		li("What it is: Blaze Android talks straight to this PC, no server in the middle. From the phone, ask_pc runs a Blaze turn on the PC (its files and programs) with approvals on the phone; from the PC, phone_ask asks the phone to text, call or check notifications.")
		li("Security: HTTPS on port 7377 with the PC's own certificate, which the phone pins from the QR code; a one-time code (5 minutes) is traded for a per-phone token. Works on the same Wi-Fi, or anywhere when both are on Tailscale.")
		if f.Link.InBuild {
			li("Pair, step by step: 1) Open Blaze Desktop › Phone and press Show pairing QR code. 2) On the phone open Blaze › Settings › Blaze Desktop and scan it. 3) Keep both on the same Wi-Fi (or Tailscale). 4) The phone shows the PC as connected; unpair any time from the Phone view.")
		} else {
			li("Status in this build: the PC side is NOT in this %s %s build (it was built in Blaze-dekstop PR #3, not merged yet), so a phone can't pair with this PC today. The phone side is shipped in Blaze Android (Settings › Blaze Desktop).", f.App, nz(f.Version, "dev"))
			li("What works now between phone and PC: sign in to the same Blaze account on both (Blaze Cloud answers on both, no keys needed), and use Design › Phone to preview designs on the phone.")
		}
		if len(f.Link.Devices) == 0 {
			li("Paired phones: none.")
		} else {
			for _, d := range f.Link.Devices {
				li("Paired phone: %s (since %s)", nz(d.Name, d.ID), d.PairedAt.Format("2 Jan 2006"))
			}
			if f.Link.Online != "" {
				li("Online now: %s", f.Link.Online)
			}
		}
	case "router":
		h("BlazeRouter")
		if !f.Router.Present {
			li("BlazeRouter (the local AI gateway) is part of Blaze Desktop, not %s. With Desktop running it serves http://127.0.0.1:20128/v1 and /dashboard on this computer.", f.App)
			if f.Router.Note != "" {
				li("%s", f.Router.Note)
			}
			break
		}
		li("What it is: Blaze's local OpenAI-compatible gateway (a Go port of OmniRoute). Any app can use %s/v1 with the local key; it picks a working model across your providers.", nz(f.Router.URL, "http://127.0.0.1:20128"))
		li("Status: %s, %s, autostart %s. Blaze's own chats %s.", onOff(f.Router.Running, "running", "off"), onOff(f.Router.Enabled, "enabled", "disabled"), "per Settings", onOff(f.Router.InUse, "go through it", "use the direct route chain"))
		if f.Router.Port > 0 {
			li("Port: %d. Providers ready: %d, models: %d.", f.Router.Port, f.Router.Ready, f.Router.Models)
		}
		if f.Router.Note != "" {
			li("Note: %s", f.Router.Note)
		}
		li("Dashboard: %s/dashboard (open with /router, the top-bar router button, or the tray).", nz(f.Router.URL, "http://127.0.0.1:20128"))
		if len(f.Router.Presets) > 0 {
			n := make([]string, 0, len(f.Router.Presets))
			for _, p := range f.Router.Presets {
				n = append(n, p.Name)
			}
			li("Presets (%d): %s", len(n), strings.Join(n, ", "))
		}
	case "views":
		h("Views")
		if len(f.Views) == 0 {
			li("%s has no views; it works in the terminal.", f.App)
			break
		}
		li("Chat is home. /views opens the picker; open_view switches to one.")
		for _, v := range f.Views {
			li("%s: %s", v.Name, v.Desc)
		}
	case "commands":
		h("Slash commands")
		for _, c := range f.Commands {
			li("/%s: %s", c.Name, c.Desc)
		}
		if len(f.Commands) == 0 {
			li("none found")
		}
	case "data":
		h("Data folders")
		li("Blaze's folder: %s (set BLAZE_HOME to move it).", nz(f.DataDir, "~/.blaze"))
		for _, e := range Layout(f.DataDir) {
			if e.Desc != "" {
				li("%s: %s", e.Name, e.Desc)
			} else {
				li("%s", e.Name)
			}
		}
	case "connectors":
		h("Connected accounts")
		on := 0
		for _, c := range f.Connectors {
			if c.Status == "connected" || c.Status == "expired" {
				on++
				li("%s: %s%s", c.Name, c.Status, onOff(c.Account != "", " ("+c.Account+")", ""))
			}
		}
		if on == 0 {
			li("None connected. /connect lists what can be connected.")
		}
		if n := len(f.Connectors) - on; n > 0 {
			li("%d more available to connect.", n)
		}
	case "mcp":
		h("MCP servers")
		if f.MCPFile != "" {
			li("Config: %s (shared by Desktop and CLI)", f.MCPFile)
		}
		for _, m := range f.MCP {
			li("%s: %s, %d tools", m.Name, m.Status, m.Tools)
		}
		if len(f.MCP) == 0 {
			li("None added.")
		}
	case "skills":
		h("Skills")
		n := make([]string, 0, len(f.Skills))
		for _, s := range f.Skills {
			n = append(n, s.Name)
		}
		li("%d on, %d off. Folder: %s", len(f.Skills), f.SkillsOff, filepath.Join(nz(f.DataDir, "~/.blaze"), "skills"))
		if len(n) > 0 {
			li("On: %s", strings.Join(n, ", "))
		}
	case "memory":
		h("Memory")
		li("%d memories saved in %s (shared by Desktop and CLI).", f.Memories, nz(f.MemoryDB, filepath.Join(nz(f.DataDir, "~/.blaze"), "blaze.db")))
		li("remember / recall / forget manage them; each turn gets the memories that match it.")
	case "system":
		h("System (this PC)")
		sy := f.System
		li("OS: %s (%s/%s)", nz(sy.OSName, sy.OS), sy.OS, sy.Arch)
		if sy.Desktop != "" || sy.Session != "" {
			li("Desktop: %s, session %s", nz(sy.Desktop, "unknown"), nz(sy.Session, "unknown"))
		}
		li("CPU: %s (%d threads), RAM %.1f GB", nz(sy.CPU, "unknown"), sy.Cores, sy.RAMGB)
		li("GPU: %s", nz(strings.Join(sy.GPU, "; "), "none detected"))
		if sy.Shell != "" {
			li("Shell: %s", sy.Shell)
		}
		if sy.Host != "" {
			li("Host name: %s", sy.Host)
		}
	}
	return b.String()
}
