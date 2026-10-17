package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProjectTrustFollowsContentHash(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	if p := LoadProject(home, dir); p.Trust != TrustNone || p.Hash != "" {
		t.Fatalf("empty folder: %+v", p)
	}
	writeJSON(t, filepath.Join(dir, ".mcp.json"), `{"mcpServers":{"x":{"command":"echo","args":["hi"]}}}`)
	p := LoadProject(home, dir)
	if p.Trust != TrustUntrusted || p.Hash == "" || len(p.Servers) != 1 || p.Servers[0].MayStart() {
		t.Fatalf("new file: %+v", p)
	}
	if err := Trust(home, dir, p.Hash); err != nil {
		t.Fatal(err)
	}
	p2 := LoadProject(home, dir)
	if !p2.Trusted() || !p2.Servers[0].Trusted || !p2.Servers[0].MayStart() {
		t.Fatalf("trusted: %+v", p2)
	}
	if ss, _ := Load(home, dir); len(ss) != 1 || !ss[0].Trusted {
		t.Fatalf("load after trust: %+v", ss)
	}

	// Any change to either project file makes it untrusted again.
	writeJSON(t, filepath.Join(dir, ".mcp.json"), `{"mcpServers":{"x":{"command":"evil"}}}`)
	if p3 := LoadProject(home, dir); p3.Trust != TrustChanged || p3.Hash == p.Hash || p3.Servers[0].Trusted {
		t.Fatalf("changed: %+v", p3)
	}
	writeJSON(t, filepath.Join(dir, ".mcp.json"), `{"mcpServers":{"x":{"command":"echo","args":["hi"]}}}`)
	if !LoadProject(home, dir).Trusted() {
		t.Fatal("restored content should be trusted again")
	}
	writeJSON(t, filepath.Join(dir, ".blaze", "mcp.json"), `{"mcpServers":{"y":{"command":"other"}}}`)
	if p4 := LoadProject(home, dir); p4.Trust != TrustChanged || len(p4.Servers) != 2 {
		t.Fatalf("second file added: %+v", p4)
	}

	// Trust is per folder.
	other := t.TempDir()
	writeJSON(t, filepath.Join(other, ".mcp.json"), `{"mcpServers":{"x":{"command":"echo","args":["hi"]}}}`)
	if LoadProject(home, other).Trust != TrustUntrusted {
		t.Fatal("trust leaked to another folder")
	}

	if ok, err := Untrust(home, dir); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, _ := Untrust(home, dir); ok {
		t.Fatal("untrusted twice")
	}
	if LoadProject(home, dir).Trust != TrustUntrusted {
		t.Fatal("untrust")
	}
	if err := Trust(home, dir, ""); err == nil {
		t.Fatal("trusting nothing should fail")
	}
}

func TestApprovalKeysDistinct(t *testing.T) {
	dir := t.TempDir()
	global := Server{Name: "gh", Command: "npx", Args: []string{"-y", "server-github"}, Source: "/home/u/.blaze/mcp.json", Trusted: true}
	proj := global
	proj.Project, proj.Source = true, filepath.Join(dir, ".mcp.json")
	proj2 := proj
	proj2.Source = filepath.Join(dir, ".blaze", "mcp.json")
	changed := global
	changed.Args = []string{"-y", "evil"}
	remote := Server{Name: "gh", URL: "https://a/mcp", Trusted: true}
	remote2 := remote
	remote2.URL = "https://b/mcp"
	keys := map[string]string{}
	for label, s := range map[string]Server{"global": global, "project": proj, "project2": proj2, "changed": changed, "remote": remote, "remote2": remote2} {
		k := ApprovalKey(s)
		if prev, dup := keys[k]; dup {
			t.Fatalf("%s and %s share approval key %s", label, prev, k)
		}
		keys[k] = label
		if allowName(k) != "gh" || allowName(k+"/tool") != "gh" {
			t.Fatalf("allowName(%q) = %q", k, allowName(k))
		}
	}
	if again := global; ApprovalKey(again) != ApprovalKey(global) {
		t.Fatal("key not stable")
	}

	// A global "Always allow" (new or legacy name-only) never covers the project server.
	m := New(t.TempDir(), dir, "test")
	if err := m.allow(global, "echo"); err != nil {
		t.Fatal(err)
	}
	if !m.allowed(global, "echo") || m.allowed(changed, "echo") || m.allowed(proj, "echo") {
		t.Fatal("a changed command or a project server inherited the approval")
	}
	// Legacy name-only entries still count for global servers only.
	if err := m.writeAllow(allowDoc{Allow: append(m.readAllow().Allow, "gh", "gh/echo")}); err != nil {
		t.Fatal(err)
	}
	if !m.allowed(global, "echo") || m.allowed(proj, "echo") || m.allowed(proj2, "echo") {
		t.Fatal("approvals leaked across sources")
	}
	if err := m.allow(proj, ""); err != nil || !m.allowed(proj, "anything") || m.allowed(proj2, "anything") {
		t.Fatal("project approval")
	}
	if err := m.Forget("gh"); err != nil || len(m.readAllow().Allow) != 0 {
		t.Fatalf("forget left %v", m.readAllow().Allow)
	}
}

func TestNoExpansionWhenUntrusted(t *testing.T) {
	t.Setenv("BLAZE_TEST_SECRET", "s3cret")
	s := Server{Name: "x", URL: "https://x/?k=${BLAZE_TEST_SECRET}", Headers: map[string]string{"A": "${BLAZE_TEST_SECRET}"}, Project: true, Source: ".mcp.json"}
	if got := s.ex(s.URL); strings.Contains(got, "s3cret") {
		t.Fatalf("untrusted project expanded: %q", got)
	}
	if got := s.exMap(s.Headers)["A"]; got != "${BLAZE_TEST_SECRET}" {
		t.Fatalf("untrusted header expanded: %q", got)
	}
	if _, err := Connect(context.Background(), s, t.TempDir(), "test"); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("untrusted connect: %v", err)
	}
	s.Trusted = true
	if got := s.ex(s.URL); got != "https://x/?k=s3cret" {
		t.Fatalf("trusted project should expand: %q", got)
	}
	g := Server{Name: "g", URL: "https://g/${BLAZE_TEST_SECRET}", Trusted: true}
	if got := g.ex(g.URL); got != "https://g/s3cret" {
		t.Fatalf("global should expand: %q", got)
	}
}

func TestManagerSkipsUntrustedProject(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	marker := filepath.Join(dir, "ran")
	// A command that would leave a marker if it ever ran.
	writeJSON(t, filepath.Join(dir, ".mcp.json"), `{"mcpServers":{"evil":{"command":"sh","args":["-c","touch `+filepath.ToSlash(marker)+`"]}}}`)
	m := New(home, dir, "test")
	m.Start()
	defer m.Close()
	m.Wait(5 * time.Second)
	sts := m.States()
	if len(sts) != 1 || sts[0].Status != Untrusted {
		t.Fatalf("states: %+v", sts)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("untrusted project command ran")
	}
	if len(m.Tools()) != 0 {
		t.Fatal("untrusted server offered tools")
	}
}

func TestAtomicWrite(t *testing.T) {
	d := t.TempDir()
	f := filepath.Join(d, "x.json")
	if err := writeFileAtomic(f, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(f, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(f); string(b) != "two" {
		t.Fatalf("%q", b)
	}
	ents, _ := os.ReadDir(d)
	if len(ents) != 1 {
		t.Fatalf("temp files left: %v", ents)
	}
}
