// Package sched registers Blaze jobs with the OS scheduler: Task Scheduler on
// Windows, the user's crontab on Linux and macOS. Each entry runs
// `blaze run-job <name>`, which logs to ~/.blaze/logs/<name>.log.
package sched

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
)

type Job struct {
	Name    string `json:"name"`
	When    string `json:"when"`
	Command string `json:"command,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
	Dir     string `json:"dir"`
	Added   string `json:"added"`
}

func file() string { return filepath.Join(config.Dir(), "schedule.json") }

func Load() []Job {
	var js []Job
	b, err := os.ReadFile(file())
	if err == nil {
		_ = json.Unmarshal(b, &js)
	}
	return js
}

func save(js []Job) error {
	_ = os.MkdirAll(config.Dir(), 0o700)
	b, _ := json.MarshalIndent(js, "", "  ")
	return os.WriteFile(file(), b, 0o600)
}

func Get(name string) (Job, bool) {
	for _, j := range Load() {
		if j.Name == name {
			return j, true
		}
	}
	return Job{}, false
}

var nameRe = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func exe() string {
	p, err := os.Executable()
	if err != nil {
		return "blaze"
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

var days = map[string][2]string{"mon": {"1", "MON"}, "tue": {"2", "TUE"}, "wed": {"3", "WED"}, "thu": {"4", "THU"}, "fri": {"5", "FRI"}, "sat": {"6", "SAT"}, "sun": {"0", "SUN"}}

// parse turns "daily 07:00" etc. into a cron line and schtasks arguments.
func parse(when string) (cron string, win []string, err error) {
	w := strings.ToLower(strings.TrimSpace(when))
	if strings.ContainsFunc(w, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", nil, fmt.Errorf("the schedule %q has control characters in it", when)
	}
	hm := func(s string) (int, int, error) {
		parts := strings.Split(s, ":")
		h, err := strconv.Atoi(parts[0])
		m := 0
		if len(parts) > 1 {
			m, _ = strconv.Atoi(parts[1])
		}
		if err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
			return 0, 0, fmt.Errorf("bad time %q (use HH:MM)", s)
		}
		return h, m, nil
	}
	f := strings.Fields(w)
	switch {
	case w == "boot" || w == "startup" || w == "login":
		return "@reboot", []string{"/SC", "ONLOGON"}, nil
	case w == "hourly":
		return "0 * * * *", []string{"/SC", "HOURLY"}, nil
	case len(f) == 2 && f[0] == "daily":
		h, m, e := hm(f[1])
		if e != nil {
			return "", nil, e
		}
		return fmt.Sprintf("%d %d * * *", m, h), []string{"/SC", "DAILY", "/ST", fmt.Sprintf("%02d:%02d", h, m)}, nil
	case len(f) == 3 && f[0] == "weekly":
		d, ok := days[f[1][:min(3, len(f[1]))]]
		if !ok {
			return "", nil, fmt.Errorf("bad day %q", f[1])
		}
		h, m, e := hm(f[2])
		if e != nil {
			return "", nil, e
		}
		return fmt.Sprintf("%d %d * * %s", m, h, d[0]), []string{"/SC", "WEEKLY", "/D", d[1], "/ST", fmt.Sprintf("%02d:%02d", h, m)}, nil
	case len(f) == 2 && f[0] == "every":
		n, unit := 0, f[1][len(f[1])-1:]
		n, e := strconv.Atoi(strings.TrimRight(f[1], "mh"))
		if e != nil || n <= 0 {
			return "", nil, fmt.Errorf("bad interval %q (use every 30m or every 2h)", f[1])
		}
		if unit == "h" {
			return fmt.Sprintf("0 */%d * * *", n), []string{"/SC", "HOURLY", "/MO", strconv.Itoa(n)}, nil
		}
		if n < 5 {
			n = 5
		}
		return fmt.Sprintf("*/%d * * * *", n), []string{"/SC", "MINUTE", "/MO", strconv.Itoa(n)}, nil
	case len(f) == 5:
		return w, nil, nil
	}
	return "", nil, fmt.Errorf("can't read the schedule %q. Use 'daily 07:00', 'hourly', 'every 30m', 'weekly mon 09:00', 'boot' or a cron line", when)
}

func Add(j Job) string {
	j.Name = strings.Trim(nameRe.ReplaceAllString(j.Name, "-"), "-")
	if j.Name == "" {
		return "Error: give the job a name."
	}
	if strings.TrimSpace(j.Command) == "" && strings.TrimSpace(j.Prompt) == "" {
		return "Error: give a command or a prompt."
	}
	cron, win, err := parse(j.When)
	if err != nil {
		return "Error: " + err.Error()
	}
	run := fmt.Sprintf(`"%s" run-job %s`, exe(), j.Name)
	if runtime.GOOS == "windows" {
		if win == nil {
			return "Error: Windows Task Scheduler needs 'daily 07:00', 'hourly', 'every 30m', 'weekly mon 09:00' or 'boot', not a cron line."
		}
		a := append([]string{"/Create", "/F", "/TN", `Blaze\` + j.Name, "/TR", run}, win...)
		if out, err := exec.Command("schtasks", a...).CombinedOutput(); err != nil {
			return "Error: Task Scheduler refused: " + strings.TrimSpace(string(out))
		}
	} else {
		if !haveCrontab() {
			return "Error: crontab isn't installed. Install cron (e.g. sudo apt install cron) and try again."
		}
		cur, err := readCrontab()
		if err != nil {
			return "Error: " + err.Error() + " Your crontab was left as it was."
		}
		next, _ := mergeCron(cur, j.Name, fmt.Sprintf("%s %s # blaze:%s", cron, run, j.Name))
		if err := writeCron(cur, next); err != nil {
			return "Error: " + err.Error()
		}
	}
	js := Load()
	var keep []Job
	for _, o := range js {
		if o.Name != j.Name {
			keep = append(keep, o)
		}
	}
	j.Added = time.Now().Format("2006-01-02 15:04")
	_ = save(append(keep, j))
	return fmt.Sprintf("Scheduled %q (%s). Logs: %s", j.Name, j.When, filepath.Join(config.Dir(), "logs", j.Name+".log"))
}

var haveCrontab = func() bool { _, err := exec.LookPath("crontab"); return err == nil }

// readCrontab returns the user's current crontab. "No crontab for this user"
// is an empty crontab, not an error. Any other failure is returned so the
// caller can stop before writing: writing after a failed read would replace
// every entry the user had with Blaze's lines alone.
var readCrontab = func() (string, error) {
	c := exec.Command("crontab", "-l")
	var stderr strings.Builder
	c.Stderr = &stderr
	out, err := c.Output()
	if err == nil {
		return string(out), nil
	}
	if _, ok := err.(*exec.ExitError); ok && noCrontab(stderr.String()) {
		return "", nil
	}
	msg := strings.TrimSpace(stderr.String())
	if msg == "" {
		msg = err.Error()
	}
	return "", fmt.Errorf("couldn't read your crontab (%s).", msg)
}

// noCrontab reports whether crontab -l's stderr means the user simply has no
// crontab yet (cronie/vixie/macOS: "no crontab for <user>"; busybox: "can't
// open '<user>': No such file or directory").
func noCrontab(stderr string) bool {
	l := strings.ToLower(stderr)
	return strings.Contains(l, "no crontab") ||
		(strings.Contains(l, "can't open") && strings.Contains(l, "no such file"))
}

// mergeCron returns cur with the named Blaze entry removed and add (if not
// empty) appended. Every other line, including comments, environment settings
// and blank lines between entries, is kept as it was; only trailing blank
// lines are trimmed. changed reports whether the result differs from cur.
func mergeCron(cur, drop, add string) (next string, changed bool) {
	cur = strings.ReplaceAll(cur, "\r\n", "\n")
	var keep []string
	tag := "# blaze:" + drop
	for _, l := range strings.Split(cur, "\n") {
		if drop != "" && strings.HasSuffix(strings.TrimRight(l, " \t"), tag) {
			changed = true
			continue
		}
		keep = append(keep, l)
	}
	for len(keep) > 0 && strings.TrimSpace(keep[len(keep)-1]) == "" {
		keep = keep[:len(keep)-1]
	}
	if add != "" {
		keep = append(keep, add)
		changed = true
	}
	if len(keep) == 0 {
		return "", changed
	}
	return strings.Join(keep, "\n") + "\n", changed
}

// writeCron installs next as the user's crontab, saving the previous one to
// ~/.blaze/crontab.bak first.
var writeCron = func(prev, next string) error {
	if strings.TrimSpace(prev) != "" {
		_ = os.MkdirAll(config.Dir(), 0o700)
		if err := os.WriteFile(filepath.Join(config.Dir(), "crontab.bak"), []byte(prev), 0o600); err != nil {
			return fmt.Errorf("couldn't back up your crontab before changing it: %v", err)
		}
	}
	c := exec.Command("crontab", "-")
	c.Stdin = strings.NewReader(next)
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("crontab: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func Remove(name string) string {
	if runtime.GOOS == "windows" {
		_ = exec.Command("schtasks", "/Delete", "/F", "/TN", `Blaze\`+name).Run()
	} else if haveCrontab() {
		cur, err := readCrontab()
		if err != nil {
			return "Error: " + err.Error() + " Your crontab was left as it was, and " + name + " is still scheduled."
		}
		if next, changed := mergeCron(cur, name, ""); changed {
			if err := writeCron(cur, next); err != nil {
				return "Error: " + err.Error()
			}
		}
	}
	js := Load()
	var keep []Job
	found := false
	for _, j := range js {
		if j.Name == name {
			found = true
			continue
		}
		keep = append(keep, j)
	}
	_ = save(keep)
	if !found {
		return "No scheduled job called " + name + "."
	}
	return "Removed " + name + "."
}

func ListText() string {
	js := Load()
	if len(js) == 0 {
		return "Nothing scheduled."
	}
	var b strings.Builder
	for i, j := range js {
		what := j.Command
		if what == "" {
			what = "blaze: " + j.Prompt
		}
		last := "never ran"
		if st, err := os.Stat(filepath.Join(config.Dir(), "logs", j.Name+".log")); err == nil {
			last = "last ran " + st.ModTime().Format("Jan 2 15:04")
		}
		fmt.Fprintf(&b, "%02d  %s · %s · %s · %s\n", i+1, j.Name, j.When, what, last)
	}
	return b.String()
}

// LogWriter opens the job's log for appending.
func LogWriter(name string) (*os.File, error) {
	d := filepath.Join(config.Dir(), "logs")
	_ = os.MkdirAll(d, 0o700)
	return os.OpenFile(filepath.Join(d, name+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}
