package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFilesMenu(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	for _, d := range []string{"src", ".git", "docs"} {
		_ = os.Mkdir(d, 0o755)
	}
	for _, f := range []string{"main.go", "README.md", ".env", filepath.Join("src", "app.go"), filepath.Join("src", "api.go")} {
		_ = os.WriteFile(f, []byte("hello"), 0o644)
	}

	m := slashMenu("explain @")
	if m == nil {
		t.Fatal("no menu for @")
	}
	var names []string
	for _, it := range m.Items {
		names = append(names, it.Name)
	}
	want := []string{"docs/", "src/", "main.go", "README.md"}
	if len(names) != len(want) {
		t.Fatalf("items %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("items %v, want %v (folders first, no dotfiles or .git)", names, want)
		}
	}
	if m.Items[1].Insert != "explain @src/" || m.Items[2].Insert != "explain @main.go " || m.Items[2].Desc != "5 B" {
		t.Errorf("inserts: %+v", m.Items)
	}

	m = slashMenu("explain @src/ap")
	if m == nil || m.Title != "files · src/" || len(m.Items) != 2 || m.Items[0].Insert != "explain @src/api.go " {
		t.Fatalf("inside src: %+v", m)
	}
	if m := slashMenu("explain @."); m == nil || m.Items[0].Name != ".env" {
		t.Errorf("a leading dot shows dotfiles: %+v", m)
	}
	if slashMenu("explain @main.go ") != nil {
		t.Error("the menu stays open after a picked file")
	}
	if slashMenu("@nope/") != nil {
		t.Error("a menu for a missing folder")
	}
	if m := slashMenu("/sk"); m == nil || m.Title != "commands" {
		t.Error("the / menu broke")
	}
}

func TestArgMenus(t *testing.T) {
	get := func(line string) []string {
		m := slashMenu(line)
		if m == nil {
			return nil
		}
		var out []string
		for _, it := range m.Items {
			out = append(out, it.Insert)
		}
		return out
	}
	has := func(line, insert string) {
		t.Helper()
		for _, in := range get(line) {
			if in == insert {
				return
			}
		}
		t.Errorf("%q: no %q in %v", line, insert, get(line))
	}
	has("/connect ", "/connect google")
	has("/connect ", "/connect")
	has("/connect go", "/connect google")
	has("/connect google ", "/connect google --manual")
	has("/disconnect ", "/disconnect github")
	has("/login ", "/login github")
	has("/keys ", "/keys sync")
	has("/learn ", "/learn off")
	has("/update ", "/update --force")
	has("/mcp ", "/mcp reload")
	has("/mcp ", "/mcp remove ")
	has("/agents ", "/agents msg ")
	has("/agents cap ", "/agents cap 3")
	if get("/learn off ") != nil || get("/login github x") != nil {
		t.Error("a menu after the last option")
	}
	if m := slashMenu("/connect "); m == nil || m.Items[0].Submit != true {
		t.Error("picking an app should run it")
	}
	if m := slashMenu("/mcp "); m == nil {
		t.Fatal("no /mcp menu")
	} else {
		for _, it := range m.Items {
			if it.Insert == "/mcp remove " && it.Submit {
				t.Error("remove needs a name: Enter must fill in, not run")
			}
		}
	}
	// every command whose menu entry promises options (›) opens a menu after the space
	for _, c := range slashMenuCmds {
		if c.sub && slashMenu(c.name+" ") == nil && c.name != "/model" {
			t.Errorf("%s is marked as having options but opens no menu", c.name)
		}
	}
}
