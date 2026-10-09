// Package update lets Blaze replace itself with the newest GitHub release.
package update

import (
	"context"
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

// token finds a GitHub token for the private repo: env first, then the gh CLI.
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
			return r, errors.New("the repo is private, so Blaze needs a GitHub token: run `gh auth login` once, or set GITHUB_TOKEN")
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

// Run is `blaze update`: check, download with a progress bar, swap the binary.
func Run(current string, force bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var sp ui.Spinner
	sp.Start("checking for a newer Blaze")
	tok := token()
	rel, err := latest(ctx, tok)
	sp.Stop()
	if err != nil {
		return err
	}
	saveCheck(rel.Tag)
	if !force && current != "dev" && !Newer(rel.Tag, current) {
		ui.StepDone(true, "Blaze "+current+" is the newest version.", "", 0)
		return nil
	}
	var a *asset
	for i := range rel.Assets {
		if rel.Assets[i].Name == assetName() {
			a = &rel.Assets[i]
		}
	}
	if a == nil {
		return fmt.Errorf("release %s has no build for %s/%s", rel.Tag, runtime.GOOS, runtime.GOARCH)
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if p, err := filepath.EvalSymlinks(self); err == nil {
		self = p
	}
	ui.Println("  " + ui.Sub(current+"  →  ") + ui.Accent(rel.Tag))

	res, err := get(ctx, "https://api.github.com/repos/"+repo+"/releases/assets/"+strconv.FormatInt(a.ID, 10), "application/octet-stream", tok)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("download failed: %s", res.Status)
	}
	tmp := self + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("can't write next to %s (%v); reinstall with the install script instead", self, err)
	}
	bar := ui.NewProgress("downloading", a.Size)
	_, err = io.Copy(f, io.TeeReader(res.Body, bar))
	f.Close()
	bar.Done()
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("download failed: %w", err)
	}
	// Make sure the new file actually runs before swapping it in.
	out, err := exec.Command(tmp, "version").Output()
	if err != nil || !strings.Contains(string(out), "blaze") {
		os.Remove(tmp)
		return errors.New("the downloaded build didn't start, so the current one is kept")
	}
	if runtime.GOOS == "windows" {
		old := self + ".old"
		os.Remove(old)
		if err := os.Rename(self, old); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, self); err != nil {
		os.Remove(tmp)
		return err
	}
	ui.StepDone(true, "Updated to "+strings.TrimSpace(string(out)), "", 0)
	ui.Println("  " + ui.Weak("Restart blaze to use it."))
	return nil
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
