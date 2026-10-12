package main

import (
	"strings"
	"testing"

	"github.com/lacksonkapapahue/blaze-cli/internal/sysinfo"
	"github.com/lacksonkapapahue/blaze-cli/internal/tools"
)

func TestAboutBlazeToolCLI(t *testing.T) {
	t.Setenv("BLAZE_HOME", t.TempDir())
	cliInfo = &sysinfo.Info{OS: "linux", OSName: "Arch Linux", Arch: "amd64", Cores: 8, RAMGB: 16}
	if b := tools.AboutBrief(); !strings.Contains(b, "ABOUT YOU") || !strings.Contains(b, "Blaze CLI") || !strings.Contains(b, "not signed in") {
		t.Fatalf("brief:\n%s", b)
	}
	e := &tools.Env{Info: cliInfo, Root: t.TempDir()}
	full := tools.Run(e, "about_blaze", `{}`)
	for _, want := range []string{"BLAZE SELF-REPORT (read live from this Blaze CLI)", "BLAZE LINK", "Settings › Blaze Desktop", "part of Blaze Desktop", "/login", "SLASH COMMANDS", "/model", "DATA FOLDERS", "Arch Linux", "has no views"} {
		if !strings.Contains(full, want) {
			t.Errorf("about_blaze misses %q:\n%s", want, full)
		}
	}
	if s := tools.Run(e, "about_blaze", `{"section":"account"}`); !strings.Contains(s, "blaze login") {
		t.Errorf("account section: %s", s)
	}
}
