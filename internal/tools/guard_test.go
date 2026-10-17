package tools

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func fakeHome(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("paths in these tables are Unix paths")
	}
	t.Setenv("HOME", "/home/tester")
}

// testEnv is an Env whose approvals are recorded and answered with answer.
func testEnv(t *testing.T, answer bool) (*Env, *[]string) {
	asked := &[]string{}
	e := &Env{Root: t.TempDir(), Shell: NewShell("sh", "/bin/sh", t.TempDir())}
	e.Confirm = func(title, detail string) bool {
		*asked = append(*asked, detail)
		return answer
	}
	return e, asked
}

func callJSON(t *testing.T, m map[string]any) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Commands on the hard blocklist: refused in every mode, with no question asked.
var blockedCmds = []string{
	"rm -r -f /",
	"rm -rf /",
	"rm -rf /*",
	"rm -rf -- ~",
	"rm -rf ~/",
	"rm -fr ~/*",
	`rm -rf "$HOME"`,
	"rm -rf ${HOME}",
	"rm -rf $HOME",
	"rm -rf $HOME/",
	`rm -rf "$HOME"/`,
	"rm --recursive --force ~",
	"rm -R ~",
	"sudo rm -rf /",
	"cd /tmp && rm -rf ~",
	"echo hi; rm -r -f -- ~",
	"rm -rf /home/tester/Documents",
	"rm -rf /home/tester/.ssh",
	"rm -rf /etc",
	"rm -rf /usr/",
	"Remove-Item $env:USERPROFILE -Recurse -Force",
	"Remove-Item -Path ~ -Recurse -Force",
	`Remove-Item C:\ -Recurse -Force`,
	`Remove-Item -LiteralPath C:\Users -Recurse`,
	"rm -r -fo $HOME",
	`rd /s /q %USERPROFILE%`,
	`rd /s /q C:\`,
	`rmdir /s /q C:\Windows`,
	"find ~ -delete",
	"find / -name '*.log' -delete",
	"find $HOME -type f -exec rm -f {} +",
	"chown -R me /",
	"chown -R me:me ~",
	"chmod -R 777 /",
	"chmod -R 755 ~",
	"wipefs -a /dev/sda",
	"fdisk /dev/sda",
	"parted /dev/sda mklabel gpt",
	"mkfs.ext4 /dev/sdb1",
	"dd if=/dev/zero of=/dev/sda bs=1M",
	"Format-Volume -DriveLetter C",
	"bcdedit /delete {current}",
	"vssadmin delete shadows /all /quiet",
	":(){ :|:& };:",
}

// Commands that ask first in Auto.
var riskyCmds = []string{
	"rm -r -f build/../..",
	"rm -rf .",
	"rm -rf *",
	"rm -r -f /tmp/build",
	"rm -rf ../other-project",
	"rm -rf $TARGET",
	"find . -name '*.tmp' | xargs rm -rf",
	"find /tmp/cache -delete",
	"chmod -R 755 /srv/app",
	"dd if=disk.img of=copy.img",
	"git push --force origin main",
	"sudo apt install jq",
	`Remove-Item -Recurse -Force ..\other`,
	"Get-ChildItem | Remove-Item -Recurse -Force",
	`shutil.rmtree("build")`,
	"fs.rmSync(dir, { recursive: true })",
}

// Commands that run with no question.
var safeCmds = []string{
	"ls -la",
	"rm file.txt",
	"rm -rf node_modules",
	"rm -rf dist build/out",
	"npm test",
	"go test ./...",
	"find . -name '*.go'",
	"chmod +x run.sh",
	"chmod -R u+w src",
	"git status",
	"echo $HOME",
	"cp -r a b",
	"mkdir -p x/y",
	`print("hello")`,
}

func TestBlockedCommands(t *testing.T) {
	fakeHome(t)
	for _, c := range blockedCmds {
		if blocked(c) == "" {
			t.Errorf("blocked(%q) = \"\", want refused", c)
		}
		if Catastrophic("Run a risky command?", c) == "" {
			t.Errorf("Catastrophic(%q) = \"\", want refused (Autopilot gate)", c)
		}
		if !risky(c) {
			t.Errorf("risky(%q) = false, want true", c)
		}
	}
	for _, c := range append(append([]string{}, riskyCmds...), safeCmds...) {
		if why := blocked(c); why != "" {
			t.Errorf("blocked(%q) = %q, want allowed", c, why)
		}
	}
}

func TestRiskyCommands(t *testing.T) {
	fakeHome(t)
	for _, c := range riskyCmds {
		if !risky(c) {
			t.Errorf("risky(%q) = false, want ask first", c)
		}
	}
	for _, c := range safeCmds {
		if risky(c) {
			t.Errorf("risky(%q) = true, want no question", c)
		}
	}
}

func TestRunShellBlocklistEveryMode(t *testing.T) {
	fakeHome(t)
	for _, yolo := range []bool{false, true} {
		e, asked := testEnv(t, true)
		e.Yolo = yolo
		for _, c := range []string{"rm -r -f /", "rm -rf -- ~", "wipefs -a /dev/sda", "chown -R me /"} {
			out := Run(e, "run_shell", callJSON(t, map[string]any{"command": c}))
			if !strings.HasPrefix(out, "Refused:") {
				t.Errorf("yolo=%v run_shell %q = %q, want Refused", yolo, c, out)
			}
		}
		if len(*asked) != 0 {
			t.Errorf("yolo=%v: blocked commands asked the user: %v", yolo, *asked)
		}
	}
}

func TestRunShellAsksForRisky(t *testing.T) {
	fakeHome(t)
	e, asked := testEnv(t, false)
	out := Run(e, "run_shell", callJSON(t, map[string]any{"command": "rm -r -f /tmp/blaze-test-not-there"}))
	if !strings.Contains(out, "did NOT approve") || len(*asked) != 1 {
		t.Fatalf("run_shell risky: out=%q asked=%v, want one question and a refusal", out, *asked)
	}
}

func TestRunCodeGated(t *testing.T) {
	fakeHome(t)
	blockedCode := []map[string]any{
		{"language": "shell", "code": "rm -rf ~"},
		{"language": "shell", "code": "echo cleaning\nrm -r -f -- \"$HOME\""},
		{"language": "powershell", "code": "Remove-Item $env:USERPROFILE -Recurse -Force"},
		{"language": "python", "code": "import shutil, os\nshutil.rmtree(os.path.expanduser('~'))"},
		{"language": "python", "code": "from pathlib import Path\nimport shutil\nshutil.rmtree(Path.home())"},
		{"language": "python", "code": "import os\nos.system('rm -rf ~')"},
		{"language": "python", "code": "import subprocess\nsubprocess.run(['rm', '-rf', '/'])"},
		{"language": "node", "code": "const fs = require('fs'), os = require('os');\nfs.rmSync(os.homedir(), {recursive: true})"},
	}
	for _, yolo := range []bool{false, true} {
		e, asked := testEnv(t, true)
		e.Yolo = yolo
		for _, c := range blockedCode {
			if out := Run(e, "run_code", callJSON(t, c)); !strings.HasPrefix(out, "Refused:") {
				t.Errorf("yolo=%v run_code %v = %q, want Refused", yolo, c, out)
			}
		}
		if len(*asked) != 0 {
			t.Errorf("yolo=%v: blocked code asked the user: %v", yolo, *asked)
		}
	}

	e, asked := testEnv(t, false)
	for _, c := range []map[string]any{
		{"language": "python", "code": "import shutil\nshutil.rmtree('build')"},
		{"language": "shell", "code": "rm -rf /tmp/blaze-test-not-there"},
		{"language": "shell", "code": "sudo reboot"},
	} {
		if out := Run(e, "run_code", callJSON(t, c)); !strings.Contains(out, "did NOT approve") {
			t.Errorf("run_code %v = %q, want it to ask and be refused", c, out)
		}
	}
	if len(*asked) != 3 {
		t.Errorf("asked %d times, want 3", len(*asked))
	}
}

func TestScheduleBlocked(t *testing.T) {
	fakeHome(t)
	e, _ := testEnv(t, true)
	out := Run(e, "schedule", callJSON(t, map[string]any{"action": "add", "name": "x", "when": "daily 07:00", "command": "rm -rf $HOME"}))
	if !strings.HasPrefix(out, "Refused:") {
		t.Errorf("schedule = %q, want Refused", out)
	}
}

func TestBadTarget(t *testing.T) {
	fakeHome(t)
	for p, want := range map[string]bool{
		"/": true, "/*": true, "~": true, "~/": true, "~/*": true, "/home/tester": true, "/home": true,
		`C:\`: true, "C:": true, `c:\windows`: true, `C:\Users\`: true, "/etc": true,
		"/tmp/x": false, "build": false, "~/proj/build": false, "./x": false, "*.log": false,
	} {
		if got := badTarget(p); got != want {
			t.Errorf("badTarget(%q) = %v, want %v", p, got, want)
		}
	}
}
