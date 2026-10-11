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
