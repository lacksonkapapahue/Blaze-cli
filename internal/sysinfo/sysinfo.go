// Package sysinfo detects the PC Blaze is running on: OS, hardware, shell,
// package managers and installed developer tools. Everything is best-effort and
// never fails: unknown values are left empty.
package sysinfo

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Info struct {
	OS          string            `json:"os"`      // windows, darwin, linux
	OSName      string            `json:"os_name"` // "Windows 11 Pro 23H2", "macOS 15.1", "Ubuntu 24.04 LTS"
	Arch        string            `json:"arch"`    // amd64, arm64
	Hostname    string            `json:"hostname"`
	User        string            `json:"user"`
	CPU         string            `json:"cpu"`
	Cores       int               `json:"cores"`
	RAMGB       float64           `json:"ram_gb"`
	GPU         []string          `json:"gpu"`
	Shell       string            `json:"shell"` // bash, zsh, powershell, pwsh, cmd, fish, sh
	ShellPath   string            `json:"shell_path"`
	PkgManagers []string          `json:"pkg_managers"` // winget, choco, scoop, brew, apt, dnf, pacman, zypper, apk
	Tools       map[string]string `json:"tools"`        // name -> version (installed only)
	WSL         bool              `json:"wsl"`
	Home        string            `json:"home"`
	DiskFreeGB  float64           `json:"disk_free_gb"`
}

// Tools Blaze cares about, with the flag that prints a version.
var toolList = [][2]string{
	{"git", "--version"}, {"python3", "--version"}, {"python", "--version"}, {"pip", "--version"},
	{"node", "--version"}, {"npm", "--version"}, {"go", "version"}, {"java", "-version"},
	{"docker", "--version"}, {"cargo", "--version"}, {"gcc", "--version"}, {"make", "--version"},
	{"curl", "--version"}, {"ollama", "--version"}, {"code", "--version"}, {"gh", "--version"},
}

var pkgList = []string{"winget", "choco", "scoop", "brew", "port", "apt", "dnf", "yum", "pacman", "zypper", "apk", "nix-env"}

func out(timeout time.Duration, name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	b, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil && len(b) == 0 {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// Detect gathers everything in parallel; it takes about a second.
func Detect() Info {
	in := Info{OS: runtime.GOOS, Arch: runtime.GOARCH, Cores: runtime.NumCPU(), Tools: map[string]string{}}
	in.Hostname, _ = os.Hostname()
	in.Home, _ = os.UserHomeDir()
	in.User = os.Getenv("USER")
	if in.User == "" {
		in.User = os.Getenv("USERNAME")
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	run := func(f func()) { wg.Add(1); go func() { defer wg.Done(); f() }() }

	run(func() { n := osName(); mu.Lock(); in.OSName = n; mu.Unlock() })
	run(func() { c, r := cpuRAM(); mu.Lock(); in.CPU, in.RAMGB = c, r; mu.Unlock() })
	run(func() { g := gpus(); mu.Lock(); in.GPU = g; mu.Unlock() })
	run(func() { d := diskFree(in.Home); mu.Lock(); in.DiskFreeGB = d; mu.Unlock() })
	for _, t := range toolList {
		t := t
		run(func() {
			if _, err := exec.LookPath(t[0]); err != nil {
				return
			}
			v := firstLine(out(4*time.Second, t[0], t[1]))
			if v == "" {
				v = "installed"
			}
			mu.Lock()
			in.Tools[t[0]] = v
			mu.Unlock()
		})
	}
	for _, p := range pkgList {
		if _, err := exec.LookPath(p); err == nil {
			in.PkgManagers = append(in.PkgManagers, p)
		}
	}
	in.Shell, in.ShellPath = shell()
	if in.OS == "linux" {
		if b, err := os.ReadFile("/proc/version"); err == nil && strings.Contains(strings.ToLower(string(b)), "microsoft") {
			in.WSL = true
		}
	}
	wg.Wait()
	// On Windows "python3" is often the Store stub; keep "python" when it's the real one.
	if v, ok := in.Tools["python3"]; ok && !strings.HasPrefix(v, "Python") {
		delete(in.Tools, "python3")
	}
	return in
}

func osName() string {
	switch runtime.GOOS {
	case "windows":
		s := out(6*time.Second, "powershell", "-NoProfile", "-Command", "(Get-CimInstance Win32_OperatingSystem).Caption + ' ' + (Get-ItemProperty 'HKLM:\\SOFTWARE\\Microsoft\\Windows NT\\CurrentVersion').DisplayVersion")
		if s != "" {
			return strings.TrimPrefix(firstLine(s), "Microsoft ")
		}
		return "Windows"
	case "darwin":
		return strings.TrimSpace("macOS " + out(3*time.Second, "sw_vers", "-productVersion"))
	default:
		if f, err := os.Open("/etc/os-release"); err == nil {
			defer f.Close()
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
					return strings.Trim(v, `"`)
				}
			}
		}
		return "Linux " + firstLine(out(2*time.Second, "uname", "-r"))
	}
}

func cpuRAM() (string, float64) {
	switch runtime.GOOS {
	case "windows":
		cpu := firstLine(out(6*time.Second, "powershell", "-NoProfile", "-Command", "(Get-CimInstance Win32_Processor | Select-Object -First 1).Name"))
		ram, _ := strconv.ParseFloat(firstLine(out(6*time.Second, "powershell", "-NoProfile", "-Command", "(Get-CimInstance Win32_ComputerSystem).TotalPhysicalMemory")), 64)
		return cpu, round1(ram / 1e9)
	case "darwin":
		cpu := out(3*time.Second, "sysctl", "-n", "machdep.cpu.brand_string")
		ram, _ := strconv.ParseFloat(out(3*time.Second, "sysctl", "-n", "hw.memsize"), 64)
		return cpu, round1(ram / 1e9)
	default:
		cpu := ""
		if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(l, "model name") || strings.HasPrefix(l, "Model") || strings.HasPrefix(l, "Hardware") {
					if _, v, ok := strings.Cut(l, ":"); ok {
						cpu = strings.TrimSpace(v)
						break
					}
				}
			}
		}
		ram := 0.0
		if b, err := os.ReadFile("/proc/meminfo"); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(l, "MemTotal:") {
					f := strings.Fields(l)
					if len(f) >= 2 {
						kb, _ := strconv.ParseFloat(f[1], 64)
						ram = round1(kb * 1024 / 1e9)
					}
				}
			}
		}
		return cpu, ram
	}
}

func gpus() []string {
	var res []string
	if _, err := exec.LookPath("nvidia-smi"); err == nil {
		s := out(5*time.Second, "nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader")
		for _, l := range strings.Split(s, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				res = append(res, "NVIDIA "+strings.TrimPrefix(l, "NVIDIA "))
			}
		}
		if len(res) > 0 {
			return res
		}
	}
	switch runtime.GOOS {
	case "windows":
		s := out(6*time.Second, "powershell", "-NoProfile", "-Command", "(Get-CimInstance Win32_VideoController).Name")
		for _, l := range strings.Split(s, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				res = append(res, l)
			}
		}
	case "darwin":
		s := out(8*time.Second, "system_profiler", "SPDisplaysDataType")
		for _, l := range strings.Split(s, "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(l), "Chipset Model:"); ok {
				res = append(res, strings.TrimSpace(v))
			}
		}
	default:
		if _, err := exec.LookPath("lspci"); err == nil {
			for _, l := range strings.Split(out(4*time.Second, "lspci"), "\n") {
				if strings.Contains(l, "VGA") || strings.Contains(l, "3D controller") {
					if i := strings.Index(l, ": "); i >= 0 {
						res = append(res, strings.TrimSpace(l[i+2:]))
					}
				}
			}
		}
	}
	return res
}

func shell() (string, string) {
	if runtime.GOOS == "windows" {
		if p, err := exec.LookPath("pwsh"); err == nil {
			return "pwsh", p
		}
		if p, err := exec.LookPath("powershell"); err == nil {
			return "powershell", p
		}
		return "cmd", os.Getenv("ComSpec")
	}
	if s := os.Getenv("SHELL"); s != "" {
		return filepath.Base(s), s
	}
	for _, s := range []string{"bash", "zsh", "sh"} {
		if p, err := exec.LookPath(s); err == nil {
			return s, p
		}
	}
	return "sh", "/bin/sh"
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

// Summary is the block that goes into the system prompt.
func (in Info) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "OS: %s (%s/%s)", in.OSName, in.OS, in.Arch)
	if in.WSL {
		b.WriteString(" inside WSL")
	}
	fmt.Fprintf(&b, "\nMachine: %s, user %s, home %s\n", in.Hostname, in.User, in.Home)
	fmt.Fprintf(&b, "CPU: %s (%d threads), RAM: %.1f GB, free disk: %.0f GB\n", nz(in.CPU, "unknown"), in.Cores, in.RAMGB, in.DiskFreeGB)
	fmt.Fprintf(&b, "GPU: %s\n", nz(strings.Join(in.GPU, "; "), "none detected"))
	fmt.Fprintf(&b, "Shell: %s (%s)\n", in.Shell, in.ShellPath)
	fmt.Fprintf(&b, "Package managers: %s\n", nz(strings.Join(in.PkgManagers, ", "), "none"))
	names := make([]string, 0, len(in.Tools))
	for k := range in.Tools {
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, k := range names {
		parts = append(parts, k+" ("+short(in.Tools[k])+")")
	}
	fmt.Fprintf(&b, "Installed tools: %s", nz(strings.Join(parts, ", "), "none of the usual ones"))
	return b.String()
}

// Missing lists common tools that aren't installed.
func (in Info) Missing() []string {
	var m []string
	for _, t := range []string{"git", "python", "node", "curl"} {
		if t == "python" {
			if _, a := in.Tools["python3"]; a {
				continue
			}
		}
		if _, ok := in.Tools[t]; !ok {
			m = append(m, t)
		}
	}
	return m
}

// InstallCommand returns the package-manager command that installs a tool, or "".
func (in Info) InstallCommand(tool string) string {
	pkg := map[string]map[string]string{
		"winget": {"git": "Git.Git", "python": "Python.Python.3.12", "node": "OpenJS.NodeJS.LTS", "curl": "cURL.cURL", "go": "GoLang.Go", "docker": "Docker.DockerDesktop", "ollama": "Ollama.Ollama"},
		"choco":  {"git": "git", "python": "python", "node": "nodejs-lts", "curl": "curl", "go": "golang", "ollama": "ollama"},
		"scoop":  {"git": "git", "python": "python", "node": "nodejs-lts", "curl": "curl", "go": "go"},
		"brew":   {"git": "git", "python": "python", "node": "node", "curl": "curl", "go": "go", "ollama": "ollama", "docker": "--cask docker"},
		"apt":    {"git": "git", "python": "python3 python3-pip python3-venv", "node": "nodejs npm", "curl": "curl", "go": "golang"},
		"dnf":    {"git": "git", "python": "python3 python3-pip", "node": "nodejs npm", "curl": "curl", "go": "golang"},
		"yum":    {"git": "git", "python": "python3", "node": "nodejs", "curl": "curl", "go": "golang"},
		"pacman": {"git": "git", "python": "python python-pip", "node": "nodejs npm", "curl": "curl", "go": "go", "ollama": "ollama"},
		"zypper": {"git": "git", "python": "python3 python3-pip", "node": "nodejs npm", "curl": "curl", "go": "go"},
		"apk":    {"git": "git", "python": "python3 py3-pip", "node": "nodejs npm", "curl": "curl", "go": "go"},
	}
	for _, pm := range in.PkgManagers {
		p, ok := pkg[pm][tool]
		if !ok {
			continue
		}
		switch pm {
		case "winget":
			return "winget install --id " + p + " -e --accept-source-agreements --accept-package-agreements"
		case "choco":
			return "choco install -y " + p
		case "scoop":
			return "scoop install " + p
		case "brew":
			return "brew install " + p
		case "apt":
			return "sudo apt-get install -y " + p
		case "dnf", "yum":
			return "sudo " + pm + " install -y " + p
		case "pacman":
			return "sudo pacman -S --noconfirm " + p
		case "zypper":
			return "sudo zypper install -y " + p
		case "apk":
			return "sudo apk add " + p
		}
	}
	if tool == "ollama" && runtime.GOOS == "linux" {
		return "curl -fsSL https://ollama.com/install.sh | sh"
	}
	return ""
}

func nz(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

func short(v string) string {
	f := strings.Fields(v)
	for _, w := range f {
		w = strings.TrimPrefix(strings.Trim(w, `",()`), "v")
		if len(w) > 0 && w[0] >= '0' && w[0] <= '9' {
			return w
		}
	}
	if len(v) > 20 {
		return v[:20]
	}
	return v
}
