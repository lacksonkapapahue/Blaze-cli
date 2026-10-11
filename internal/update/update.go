// Package update lets Blaze replace itself with the newest GitHub release.
package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/ui"
)

const repo = "lacksonkapapahue/Blaze-cli"

type asset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type release struct {
	Tag    string  `json:"tag_name"`
	Assets []asset `json:"assets"`
}

// token finds an optional GitHub token (avoids rate limits, works if the repo goes private): env first, then the gh CLI.
func token() string {
	for _, k := range []string{"BLAZE_GITHUB_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	if p, err := exec.LookPath("gh"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, p, "auth", "token").Output(); err == nil {
			return strings.TrimSpace(string(out))
		}
	}
	return ""
}

func get(ctx context.Context, url, accept, tok string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "blaze-cli")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	return http.DefaultClient.Do(req)
}

func latest(ctx context.Context, tok string) (release, error) {
	var r release
	res, err := get(ctx, "https://api.github.com/repos/"+repo+"/releases/latest", "application/vnd.github+json", tok)
	if err != nil {
		return r, fmt.Errorf("couldn't reach GitHub: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == 404 || res.StatusCode == 401 {
		if tok == "" {
			return r, errors.New("GitHub couldn't find the release; if the repo is private, run `gh auth login` once or set GITHUB_TOKEN")
		}
		return r, errors.New("GitHub refused the token (it needs read access to " + repo + ")")
	}
	if res.StatusCode != 200 {
		return r, fmt.Errorf("GitHub answered %s", res.Status)
	}
	return r, json.NewDecoder(res.Body).Decode(&r)
}

func assetName() string {
	n := "blaze-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		n += ".exe"
	}
	return n
}

// Newer reports whether tag a is a higher version than b ("v0.2.1" > "v0.2.0").
func Newer(a, b string) bool {
	pa, pb := parts(a), parts(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parts(v string) [3]int {
	var p [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	v, _, _ = strings.Cut(v, "-")
	for i, s := range strings.SplitN(v, ".", 3) {
		p[i], _ = strconv.Atoi(s)
	}
	return p
}

// Check asks GitHub for the newest release: its tag and whether it is newer than current.
func Check(current string) (latestTag string, newer bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rel, err := latest(ctx, token())
	if err != nil {
		return "", false, err
	}
	saveCheck(rel.Tag)
	return rel.Tag, current == "dev" || Newer(rel.Tag, current), nil
}

// Swap atomically replaces self with the verified file at tmp. On Unix this is one
// rename (the running process keeps its old inode). Windows can't replace a running
// .exe, so the old one is moved aside first and moved back if the swap fails.
func Swap(self, tmp string) error {
	if runtime.GOOS != "windows" {
		if err := os.Rename(tmp, self); err != nil {
			os.Remove(tmp)
			return err
		}
		return nil
	}
	old := self + ".old"
	os.Remove(old)
	if err := os.Rename(self, old); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, self); err != nil {
		_ = os.Rename(old, self) // roll back: the current version keeps working
		os.Remove(tmp)
		return err
	}
	return nil
}

// Self is the path of the running binary, symlinks resolved.
func Self() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if p, err := filepath.EvalSymlinks(self); err == nil {
		self = p
	}
	return self, nil
}

// Run is `blaze update`: check, download with a progress bar, swap the binary.
// updated is true when a new binary was installed (restart to use it).
func Run(current string, force bool) (updated bool, err error) {
	updated, err = run(current, force)
	return
}

func run(current string, force bool) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var sp ui.Spinner
	sp.Start("checking for a newer Blaze")
	tok := token()
	rel, err := latest(ctx, tok)
	sp.Stop()
	if err != nil {
		return false, err
	}
	saveCheck(rel.Tag)
	if !force && current != "dev" && !Newer(rel.Tag, current) {
		ui.StepDone(true, "Blaze "+current+" is the newest version.", "", 0)
		return false, nil
	}
	var a, sums *asset
	for i := range rel.Assets {
		switch rel.Assets[i].Name {
		case assetName():
			a = &rel.Assets[i]
		case "SHA256SUMS":
			sums = &rel.Assets[i]
		}
	}
	if a == nil {
		return false, fmt.Errorf("release %s has no build for %s/%s", rel.Tag, runtime.GOOS, runtime.GOARCH)
	}
	self, err := Self()
	if err != nil {
		return false, err
	}
	ui.Println("  " + ui.Sub(current+"  →  ") + ui.Accent(rel.Tag))

	res, err := get(ctx, "https://api.github.com/repos/"+repo+"/releases/assets/"+strconv.FormatInt(a.ID, 10), "application/octet-stream", tok)
	if err != nil {
		return false, fmt.Errorf("download failed: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return false, fmt.Errorf("download failed: %s", res.Status)
	}
	tmp := self + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return false, fmt.Errorf("can't write next to %s (%v); reinstall with the install script instead", self, err)
	}
	bar := ui.NewProgress("downloading", a.Size)
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.TeeReader(res.Body, bar))
	f.Close()
	bar.Done()
	if err != nil {
		os.Remove(tmp)
		return false, fmt.Errorf("download failed: %w", err)
	}
	// Releases from v0.8.1 on carry SHA256SUMS; older ones are only smoke-tested below.
	if sums != nil {
		want, err := fetchSum(ctx, *sums, assetName(), tok)
		if err != nil {
			os.Remove(tmp)
			return false, fmt.Errorf("couldn't check the download (%v); the current build is kept", err)
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != want {
			os.Remove(tmp)
			return false, errors.New("the download doesn't match the release checksum, so the current build is kept")
		}
	}
	// Make sure the new file actually runs before swapping it in.
	out, err := exec.Command(tmp, "version").Output()
	if err != nil || !strings.Contains(string(out), "blaze") {
		os.Remove(tmp)
		return false, errors.New("the downloaded build didn't start, so the current one is kept")
	}
	if err := Swap(self, tmp); err != nil {
		return false, fmt.Errorf("couldn't swap in the new build (%v); the current one is kept", err)
	}
	ui.StepDone(true, "Updated to "+strings.TrimSpace(string(out)), "", 0)
	return true, nil
}

// fetchSum downloads a release's SHA256SUMS and returns the hash listed for name.
func fetchSum(ctx context.Context, a asset, name, tok string) (string, error) {
	res, err := get(ctx, "https://api.github.com/repos/"+repo+"/releases/assets/"+strconv.FormatInt(a.ID, 10), "application/octet-stream", tok)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", fmt.Errorf("SHA256SUMS: %s", res.Status)
	}
	return sumFor(io.LimitReader(res.Body, 1<<20), name)
}

// sumFor finds name's hash in sha256sum output ("<hex>  <name>", "*" marks binary mode).
func sumFor(r io.Reader, name string) (string, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == 64 {
			return strings.ToLower(f[0]), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("SHA256SUMS has no entry for %s", name)
}

// ---------- quiet daily check ----------

func checkFile() string { return filepath.Join(config.Dir(), "update-check") }

func saveCheck(tag string) {
	_ = os.MkdirAll(config.Dir(), 0o700)
	_ = os.WriteFile(checkFile(), []byte(time.Now().Format(time.RFC3339)+" "+tag), 0o600)
}

// Hint returns the cached newer version (no network) and, at most once a day,
// refreshes the cache in the background for next time.
func Hint(current string) string {
	if current == "dev" || os.Getenv("BLAZE_NO_UPDATE_CHECK") != "" {
		return ""
	}
	// Tidy a leftover from a Windows self-update.
	if self, err := os.Executable(); err == nil {
		os.Remove(self + ".old")
	}
	var when time.Time
	tag := ""
	if b, err := os.ReadFile(checkFile()); err == nil {
		f := strings.Fields(string(b))
		if len(f) == 2 {
			when, _ = time.Parse(time.RFC3339, f[0])
			tag = f[1]
		}
	}
	if time.Since(when) > 24*time.Hour {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			if r, err := latest(ctx, token()); err == nil {
				saveCheck(r.Tag)
			}
		}()
	}
	if tag != "" && Newer(tag, current) {
		return tag
	}
	return ""
}
