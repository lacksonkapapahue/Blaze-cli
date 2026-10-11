package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.4.0", "v0.3.0", true},
		{"v0.3.10", "v0.3.9", true},
		{"v0.3.0", "v0.3.0", false},
		{"v0.2.9", "v0.3.0", false},
		{"v1.0.0-rc1", "v0.9.9", true},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%s, %s) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestSwapReplacesAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "blaze")
	tmp := self + ".new"
	if err := os.WriteFile(self, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmp, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Swap(self, tmp); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(self)
	if string(b) != "new" {
		t.Fatalf("self = %q, want new", b)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("tmp file left behind")
	}
}

func TestSwapFailureKeepsCurrent(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "blaze")
	if err := os.WriteFile(self, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Swap(self, filepath.Join(dir, "missing.new")); err == nil {
		t.Fatal("want an error for a missing download")
	}
	b, _ := os.ReadFile(self)
	if string(b) != "old" {
		t.Fatalf("self = %q, want old kept", b)
	}
}
