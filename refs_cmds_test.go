package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lacksonkapapahue/blaze-cli/internal/ui"
)

func TestRefsMenu(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	_ = os.WriteFile(filepath.Join(dir, "main.go"), make([]byte, 13*1024), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "shot.png"), []byte("x"), 0o644)
	_ = os.Mkdir(filepath.Join(dir, "my dir"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "my dir", "a b.txt"), []byte("x"), 0o644)

	m := withRefsMenu(nil)("look @")
	if m == nil || m.Title != "context" {
		t.Fatalf("%+v", m)
	}
	names := []string{}
	for _, it := range m.Items {
		names = append(names, it.Name+"|"+it.Desc+"|"+it.Section)
	}
	got := strings.Join(names, "\n")
	for _, w := range []string{"@diff|Git working tree diff|references", "@url:|Fetch web content|references", "main.go|13K|files", "my dir/|folder|files"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in\n%s", w, got)
		}
	}
	if m.Items[0].Insert != "look @diff " || !m.Items[0].DescAccent {
		t.Errorf("first %+v", m.Items[0])
	}
	m = withRefsMenu(nil)("@di")
	if m.Items[0].Name != "@diff" {
		t.Errorf("@di → %+v", m.Items[0])
	}
	m = withRefsMenu(nil)("@file:")
	if m == nil || m.Items[0].Insert != `@file:"my dir/` {
		t.Fatalf("@file: %+v", m)
	}
	m = withRefsMenu(nil)(`@file:"my dir/`)
	if m == nil || len(m.Items) != 1 || m.Items[0].Insert != `@file:"my dir/a b.txt" ` {
		t.Fatalf("walk %+v", m)
	}
	m = withRefsMenu(nil)("@image:")
	for _, it := range m.Items {
		if it.Name == "main.go" {
			t.Error("non-image in @image: menu")
		}
	}
	m = withRefsMenu(nil)("@git:")
	if len(m.Items) != 4 || m.Items[2].Insert != "@git:5 " {
		t.Errorf("git %+v", m.Items)
	}
	if withRefsMenu(nil)("@url:http") != nil {
		t.Error("url menu")
	}
	called := false
	withRefsMenu(func(string) *ui.MenuSet { called = true; return nil })("/help")
	if !called {
		t.Error("base menu not used for non-@ lines")
	}
}
