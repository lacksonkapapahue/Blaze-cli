package sched

import (
	"errors"
	"runtime"
	"strings"
	"testing"
)

func TestMergeCronKeepsUserLines(t *testing.T) {
	cur := "MAILTO=me@example.com\n# my backups\n0 3 * * * /usr/bin/backup\n\n*/5 * * * * \"/b/blaze\" run-job old # blaze:old\n@reboot /usr/bin/other\n0 1 * * * \"/b/blaze\" run-job keep # blaze:keep\n\n\n"
	next, changed := mergeCron(cur, "old", "0 7 * * * \"/b/blaze\" run-job old # blaze:old")
	if !changed {
		t.Fatal("expected a change")
	}
	want := "MAILTO=me@example.com\n# my backups\n0 3 * * * /usr/bin/backup\n\n@reboot /usr/bin/other\n0 1 * * * \"/b/blaze\" run-job keep # blaze:keep\n0 7 * * * \"/b/blaze\" run-job old # blaze:old\n"
	if next != want {
		t.Fatalf("got\n%q\nwant\n%q", next, want)
	}
}

func TestMergeCronRemoveOnlyNamedJob(t *testing.T) {
	cur := "0 3 * * * /usr/bin/backup\n0 1 * * * x run-job a # blaze:a\n0 2 * * * x run-job ba # blaze:ba\n"
	next, changed := mergeCron(cur, "a", "")
	if !changed {
		t.Fatal("expected a change")
	}
	if want := "0 3 * * * /usr/bin/backup\n0 2 * * * x run-job ba # blaze:ba\n"; next != want {
		t.Fatalf("got %q want %q", next, want)
	}
	if _, changed := mergeCron(cur, "missing", ""); changed {
		t.Fatal("removing an absent job should not report a change")
	}
}

func TestMergeCronEmpty(t *testing.T) {
	next, _ := mergeCron("", "j", "0 * * * * x # blaze:j")
	if next != "0 * * * * x # blaze:j\n" {
		t.Fatalf("got %q", next)
	}
	if next, _ := mergeCron("0 * * * * x # blaze:j\n", "j", ""); next != "" {
		t.Fatalf("got %q", next)
	}
}

func TestNoCrontab(t *testing.T) {
	for _, s := range []string{"no crontab for alice", "crontab: no crontab for alice\n", "crontab: can't open 'alice': No such file or directory"} {
		if !noCrontab(s) {
			t.Errorf("%q should mean no crontab", s)
		}
	}
	for _, s := range []string{"", "crontab: permission denied", "PAM failure", "crontab: can't open temp file: Permission denied"} {
		if noCrontab(s) {
			t.Errorf("%q should be an error", s)
		}
	}
}

// A failed crontab -l must abort Add and Remove without writing anything.
func TestReadFailureNeverWrites(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("crontab is not used on Windows")
	}
	t.Setenv("BLAZE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	oldR, oldW, oldH := readCrontab, writeCron, haveCrontab
	t.Cleanup(func() { readCrontab, writeCron, haveCrontab = oldR, oldW, oldH })
	haveCrontab = func() bool { return true }
	readCrontab = func() (string, error) { return "", errors.New("couldn't read your crontab (PAM failure).") }
	wrote := false
	writeCron = func(prev, next string) error { wrote = true; return nil }

	if got := Add(Job{Name: "j", When: "hourly", Command: "true"}); !strings.HasPrefix(got, "Error:") {
		t.Fatalf("Add: %q", got)
	}
	if got := Remove("j"); !strings.HasPrefix(got, "Error:") {
		t.Fatalf("Remove: %q", got)
	}
	if wrote {
		t.Fatal("crontab was written after a failed read")
	}
}

func TestMergeViaAddKeepsLines(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("crontab is not used on Windows")
	}
	t.Setenv("BLAZE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	oldR, oldW, oldH := readCrontab, writeCron, haveCrontab
	t.Cleanup(func() { readCrontab, writeCron, haveCrontab = oldR, oldW, oldH })
	haveCrontab = func() bool { return true }
	cron := "0 3 * * * /usr/bin/backup\n"
	readCrontab = func() (string, error) { return cron, nil }
	writeCron = func(prev, next string) error { cron = next; return nil }
	if got := Add(Job{Name: "j", When: "hourly", Command: "true"}); strings.HasPrefix(got, "Error:") {
		t.Fatal(got)
	}
	if !strings.HasPrefix(cron, "0 3 * * * /usr/bin/backup\n") || !strings.Contains(cron, "# blaze:j") {
		t.Fatalf("got %q", cron)
	}
	Remove("j")
	if cron != "0 3 * * * /usr/bin/backup\n" {
		t.Fatalf("after remove got %q", cron)
	}
}

func TestParseRejectsControlChars(t *testing.T) {
	if _, _, err := parse("#\n@reboot cmd a b"); err == nil {
		t.Fatal("expected an error")
	}
}
