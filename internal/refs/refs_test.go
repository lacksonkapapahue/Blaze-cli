package refs

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFind(t *testing.T) {
	line := `look at @diff and @staged, @git:5 @git @file:main.go @folder:"my dir" @url:https://x.io/a mail me a@b.c @someone @https://y.io`
	got := Find(line)
	want := []struct {
		k   Kind
		arg string
	}{{Diff, ""}, {Staged, ""}, {Git, "5"}, {Git, "1"}, {File, "main.go"}, {Folder, "my dir"}, {URL, "https://x.io/a"}, {Path, "someone"}, {URL, "https://y.io"}}
	if len(got) != len(want) {
		t.Fatalf("got %d refs: %+v", len(got), got)
	}
	for i, w := range want {
		if got[i].Kind != w.k || got[i].Arg != w.arg {
			t.Errorf("ref %d = %v %q, want %v %q", i, got[i].Kind, got[i].Arg, w.k, w.arg)
		}
		if line[got[i].Start:got[i].Start+len(got[i].Token)] != got[i].Token {
			t.Errorf("ref %d token/offset mismatch", i)
		}
	}
	// "@staged," keeps the comma out
	if got[1].Token != "@staged," && got[1].Kind != Staged {
		t.Errorf("staged token %q", got[1].Token)
	}
}

func TestQuote(t *testing.T) {
	if Quote("/a/b.png") != "/a/b.png" || Quote("/a b.png") != `"/a b.png"` {
		t.Fatal(Quote("/a b.png"))
	}
	r := Find("@image:" + Quote(`C:\My Pics\a.png`))
	if len(r) != 1 || r[0].Arg != `C:\My Pics\a.png` {
		t.Fatalf("%+v", r)
	}
}

func TestHumanSize(t *testing.T) {
	for n, w := range map[int64]string{500: "500B", 3 * 1024: "3K", 13*1024 + 100: "13K", 1536 * 1024: "1.5M"} {
		if g := HumanSize(n); g != w {
			t.Errorf("HumanSize(%d)=%q want %q", n, g, w)
		}
	}
}

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL="+os.DevNull)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q")
	write(t, dir, "a.txt", "one\n")
	run("add", ".")
	run("commit", "-q", "-m", "first")
	write(t, dir, "a.txt", "one\ntwo\n")
	run("commit", "-q", "-am", "second")
	write(t, dir, "a.txt", "one\ntwo\nthree\n") // unstaged
	write(t, dir, "b.txt", "bee\n")
	run("add", "b.txt") // staged
	return dir
}

func write(t *testing.T, dir, name, s string) {
	t.Helper()
	p := filepath.Join(dir, name)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExpandGit(t *testing.T) {
	dir := gitRepo(t)
	res := Expand(context.Background(), "review @diff @staged @git:2", Options{Cwd: dir})
	for _, w := range []string{`<context ref="@diff">`, "+three", `<context ref="@staged">`, "+bee", `<context ref="@git:2">`, "second", "first", "</context>"} {
		if !strings.Contains(res.Text, w) {
			t.Errorf("missing %q in:\n%s", w, res.Text)
		}
	}
	if !strings.HasPrefix(res.Text, "review @diff @staged @git:2\n\n") {
		t.Errorf("line not kept first: %q", res.Text[:40])
	}
	if len(res.Receipts) != 3 || !res.Receipts[0].OK || !strings.HasSuffix(res.Receipts[0].Detail, "lines") {
		t.Errorf("receipts %+v", res.Receipts)
	}
	if !strings.Contains(res.ReceiptLine(), "@git:2 2 commits") {
		t.Errorf("receipt line %q", res.ReceiptLine())
	}
}

func TestExpandGitFailsSoftly(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	res := Expand(context.Background(), "x @diff", Options{Cwd: dir})
	if len(res.Receipts) != 1 || res.Receipts[0].OK || res.Receipts[0].Detail != "not a git repository" {
		t.Fatalf("%+v", res.Receipts)
	}
	if !strings.Contains(res.Text, "(couldn't read: not a git repository)") {
		t.Fatal(res.Text)
	}
}

func TestExpandFilesAndCaps(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "main.go", "package main\n")
	write(t, dir, "big.txt", strings.Repeat("line of text\n", 2000))
	write(t, dir, "sub/x.txt", "ex\n")
	write(t, dir, "sub/deep/y.txt", "why\n")
	write(t, dir, "bin.dat", "a\x00b")
	res := Expand(context.Background(), "see @main.go @file:big.txt @folder:sub @bin.dat @nobody", Options{Cwd: dir, PerRef: 4000, Total: 100000})
	if !strings.Contains(res.Text, `<context ref="@main.go">`+"\npackage main\n</context>") {
		t.Errorf("plain path block:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "…(truncated:") {
		t.Error("big file not truncated")
	}
	if !strings.Contains(res.Text, "deep/") || !strings.Contains(res.Text, `<file path="x.txt">`) || !strings.Contains(res.Text, `<file path="deep/y.txt">`) {
		t.Errorf("folder block:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "binary file") {
		t.Error("binary not noted")
	}
	if len(res.Receipts) != 4 { // @nobody is not a path: no receipt
		t.Errorf("receipts %+v", res.Receipts)
	}
	if !strings.Contains(res.Receipts[1].Detail, "(cut)") {
		t.Errorf("cut not in receipt: %+v", res.Receipts[1])
	}
	// total cap
	res = Expand(context.Background(), "@file:big.txt @main.go", Options{Cwd: dir, PerRef: 20000, Total: 20100})
	if res.Receipts[1].OK || !strings.Contains(res.Text, "context cap") {
		t.Errorf("total cap: %+v", res.Receipts)
	}
}

func TestExpandURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<html><head><title>Hello &amp; welcome</title><style>.x{}</style></head>
<body><nav>menu menu</nav><h1>Title</h1><p>First para &mdash; ok.</p><script>alert(1)</script><ul><li>one</li><li>two</li></ul></body></html>`))
		case "/slow":
			time.Sleep(500 * time.Millisecond)
		case "/bin":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte{0, 1, 2})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	opt := Options{Timeout: 200 * time.Millisecond}
	res := Expand(context.Background(), "sum @url:"+srv.URL+"/page", opt)
	for _, w := range []string{"# Hello & welcome", "# Title", "First para — ok.", "- one", "- two"} {
		if !strings.Contains(res.Text, w) {
			t.Errorf("missing %q in:\n%s", w, res.Text)
		}
	}
	for _, bad := range []string{"alert", "menu menu", ".x{}"} {
		if strings.Contains(res.Text, bad) {
			t.Errorf("kept %q", bad)
		}
	}
	if !res.Receipts[0].OK || !strings.HasPrefix(res.Receipts[0].Ref, "@url 127.0.0.1") {
		t.Errorf("%+v", res.Receipts)
	}
	for path, want := range map[string]string{"/slow": "timed out", "/nope": "HTTP 404", "/bin": "not a text page"} {
		res = Expand(context.Background(), "@url:"+srv.URL+path, opt)
		if res.Receipts[0].OK || !strings.Contains(res.Receipts[0].Detail, want) {
			t.Errorf("%s: %+v", path, res.Receipts)
		}
	}
	res = Expand(context.Background(), "@url:file:///etc/passwd", opt)
	if res.Receipts[0].OK || !strings.Contains(res.Text, "only http and https") {
		t.Errorf("file url: %+v %s", res.Receipts, res.Text)
	}
}

func pngFile(t *testing.T, dir, name string, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(x ^ y), 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExpandImages(t *testing.T) {
	dir := t.TempDir()
	pngFile(t, dir, "shot.png", 40, 30)
	pngFile(t, dir, "my pic.png", 3000, 100)
	res := Expand(context.Background(), `what is @image:shot.png vs @"my pic.png"? and @shot.png`, Options{Cwd: dir})
	if len(res.Images) != 3 {
		t.Fatalf("images %d %+v", len(res.Images), res.Receipts)
	}
	if res.Text != "what is [image 1] vs [image 2]? and [image 3]" {
		t.Errorf("text %q", res.Text)
	}
	for _, u := range res.Images {
		if !strings.HasPrefix(u, "data:image/png;base64,") {
			t.Fatalf("url %q", u[:30])
		}
	}
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(res.Images[1], "data:image/png;base64,"))
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width != maxImageSide || cfg.Height != 68 {
		t.Errorf("scaled to %dx%d (%v)", cfg.Width, cfg.Height, err)
	}
	// size cap: refused with a note, line kept
	res = Expand(context.Background(), "@image:shot.png", Options{Cwd: dir, MaxImage: 100})
	if len(res.Images) != 0 || res.Receipts[0].OK || !strings.Contains(res.Text, "not attached") {
		t.Errorf("cap: %+v %q", res.Receipts, res.Text)
	}
	// not an image
	write(t, dir, "fake.png", "hello")
	res = Expand(context.Background(), "@image:fake.png", Options{Cwd: dir})
	if len(res.Images) != 0 || res.Receipts[0].Detail != "not a PNG, JPEG or GIF image" {
		t.Errorf("fake: %+v", res.Receipts)
	}
	// image count cap
	res = Expand(context.Background(), "@shot.png @shot.png", Options{Cwd: dir, MaxImages: 1})
	if len(res.Images) != 1 || res.Receipts[1].OK {
		t.Errorf("count cap: %+v", res.Receipts)
	}
}

func TestNoRefs(t *testing.T) {
	res := Expand(context.Background(), "plain words, mail a@b.c", Options{})
	if res.Text != "plain words, mail a@b.c" || len(res.Receipts) != 0 {
		t.Fatalf("%+v", res)
	}
}
