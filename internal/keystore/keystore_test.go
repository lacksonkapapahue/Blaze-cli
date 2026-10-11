package keystore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/zalando/go-keyring"
)

// fakeKC is an in-memory keychain; broken makes every write fail (no Secret Service).
type fakeKC struct {
	mu     sync.Mutex
	m      map[string]string
	broken bool
}

func (f *fakeKC) Get(a string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.m[a]; ok {
		return v, nil
	}
	return "", keyring.ErrNotFound
}
func (f *fakeKC) Set(a, v string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.broken {
		return errors.New("no keychain")
	}
	if f.m == nil {
		f.m = map[string]string{}
	}
	f.m[a] = v
	return nil
}
func (f *fakeKC) Delete(a string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, a)
	return nil
}

func newStore(dir string, kc Backend) *Store {
	s := &Store{dir: dir, kc: kc}
	s.migrate()
	return s
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTwoAppsShareKeychainAndFallback(t *testing.T) {
	for _, broken := range []bool{false, true} {
		dir := t.TempDir()
		kc := &fakeKC{broken: broken}
		cli, desk := newStore(dir, kc), newStore(dir, kc) // two processes, same ~/.blaze
		where, err := cli.Set("groq", " gsk_test ")
		if err != nil {
			t.Fatal(err)
		}
		if broken != (where == "~/.blaze/keys.json (owner-only)") {
			t.Fatalf("broken=%v where=%q", broken, where)
		}
		if got := desk.Main("groq"); got != "gsk_test" {
			t.Fatalf("desktop sees %q", got)
		}
		if _, err := desk.SetExtras("groq", []string{"gsk_two", "gsk_test", "gsk_two"}); err != nil {
			t.Fatal(err)
		}
		if got := cli.All("groq"); !reflect.DeepEqual(got, []string{"gsk_test", "gsk_two"}) {
			t.Fatalf("cli ring %v", got)
		}
		if err := cli.SetAll("groq", nil); err != nil {
			t.Fatal(err)
		}
		if got := desk.All("groq"); len(got) != 0 {
			t.Fatalf("deleted ring still %v", got)
		}
		if broken {
			if fi, err := os.Stat(filepath.Join(dir, "keys.json")); err == nil {
				t.Fatalf("empty keys.json left behind (%v)", fi.Size())
			}
		}
	}
}

func TestFallbackFileIsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	s := newStore(dir, &fakeKC{broken: true})
	if _, err := s.Set("gemini", "AIza-x"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}

func TestLegacyMigrationKeepsEverything(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	writeJSON(t, cfg, map[string]any{
		"chain":     []map[string]string{{"provider": "groq", "model": "m"}},
		"keys":      map[string]string{"groq": "gsk_old", "mistral": "mis_old", "groq#extra": `["gsk_x"]`},
		"model_for": map[string]string{"groq": "m"},
	})
	kc := &fakeKC{m: map[string]string{"mistral": "mis_keychain"}}
	s := newStore(dir, kc)
	if kc.m["groq"] != "gsk_old" || kc.m["groq#extra"] != `["gsk_x"]` {
		t.Fatalf("not migrated: %v", kc.m)
	}
	if kc.m["mistral"] != "mis_keychain" {
		t.Fatal("migration overwrote a newer keychain key")
	}
	// The legacy copy stays for older Blaze versions; other fields untouched.
	var raw map[string]any
	b, _ := os.ReadFile(cfg)
	_ = json.Unmarshal(b, &raw)
	if raw["keys"] == nil || raw["model_for"] == nil {
		t.Fatalf("config.json changed: %s", b)
	}
	// Deleting removes the legacy copy too, so it can't come back.
	if err := s.Delete("groq"); err != nil {
		t.Fatal(err)
	}
	if s.Get("groq") != "" {
		t.Fatal("legacy key came back")
	}
	b, _ = os.ReadFile(cfg)
	_ = json.Unmarshal(b, &raw)
	if raw["model_for"] == nil || raw["chain"] == nil {
		t.Fatalf("other fields lost: %s", b)
	}
	// Runs once only.
	kc.m["groq"] = ""
	s2 := newStore(dir, kc)
	if v, _ := kc.Get("groq"); v != "" {
		t.Fatalf("migrated twice: %q (%v)", v, s2)
	}
}

func TestLegacyMigrationWithoutKeychain(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "config.json"), map[string]any{"keys": map[string]string{"nvidia": "nvapi-1"}})
	s := newStore(dir, &fakeKC{broken: true})
	if readMap(filepath.Join(dir, "keys.json"))["nvidia"] != "nvapi-1" || s.Main("nvidia") != "nvapi-1" {
		t.Fatal("not copied to keys.json")
	}
}

func TestMerge(t *testing.T) {
	now := int64(1000)
	local := map[string]Entry{
		"groq":    {K: []string{"g-new"}, T: 900},      // newer here
		"gemini":  {K: []string{"a-old"}, T: 100},      // older here
		"mistral": {K: []string{"m-local"}},            // unknown age
		"openai":  {K: nil, T: 950},                    // deleted here
		"nvidia":  {K: []string{"n-only-here"}, T: 50}, // not on the account
	}
	remote := map[string]Entry{
		"groq":     {K: []string{"g-old"}, T: 500},
		"gemini":   {K: []string{"a-new"}, T: 600},
		"mistral":  {K: []string{"m-remote"}, T: 300},
		"openai":   {K: []string{"o"}, T: 400},
		"deepseek": {K: []string{"d"}, T: 10},
		"future":   {K: []string{"keep-me"}, T: 1},
	}
	m, apply, push := Merge(local, remote, now)
	if !push {
		t.Fatal("should push")
	}
	want := map[string]Entry{
		"groq": {K: []string{"g-new"}, T: 900}, "gemini": {K: []string{"a-new"}, T: 600},
		"mistral": {K: []string{"m-remote", "m-local"}, T: now}, "openai": {K: nil, T: 950},
		"nvidia": {K: []string{"n-only-here"}, T: 50}, "deepseek": {K: []string{"d"}, T: 10},
		"future": {K: []string{"keep-me"}, T: 1},
	}
	if !reflect.DeepEqual(m, want) {
		t.Fatalf("merged\n%v\nwant\n%v", m, want)
	}
	for _, p := range []string{"gemini", "mistral", "deepseek"} {
		if _, ok := apply[p]; !ok {
			t.Fatalf("%s not applied: %v", p, apply)
		}
	}
	for _, p := range []string{"groq", "openai", "nvidia", "future"} {
		if _, ok := apply[p]; ok {
			t.Fatalf("%s applied: %v", p, apply)
		}
	}
	if _, _, push := Merge(want, want, now); push {
		t.Fatal("identical sets shouldn't push")
	}
}

func TestCrypto(t *testing.T) {
	key := make([]byte, 32)
	b, err := Encrypt(key, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	p, err := Decrypt(key, b)
	if err != nil || string(p) != `{"v":1}` {
		t.Fatalf("%s %v", p, err)
	}
	key[0] = 1
	if _, err := Decrypt(key, b); err == nil {
		t.Fatal("wrong key opened the blob")
	}
}

// fakeWorker mimics blaze-auth /v1/keys for one account.
func fakeWorker(t *testing.T) (*httptest.Server, *struct {
	rev  int64
	blob string
}) {
	st := &struct {
		rev  int64
		blob string
	}{}
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		switch r.Method {
		case "GET":
			_ = json.NewEncoder(w).Encode(map[string]any{"rev": st.rev, "blob": st.blob, "key": key})
		case "POST":
			var b struct {
				Rev  int64  `json:"rev"`
				Blob string `json:"blob"`
			}
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b.Rev != st.rev {
				w.WriteHeader(409)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "conflict", "rev": st.rev})
				return
			}
			st.rev++
			st.blob = b.Blob
			_ = json.NewEncoder(w).Encode(map[string]any{"rev": st.rev})
		}
	}))
	t.Cleanup(srv.Close)
	return srv, st
}

func TestSyncBetweenDevices(t *testing.T) {
	srv, st := fakeWorker(t)
	ctx := context.Background()
	pc := newStore(t.TempDir(), &fakeKC{})
	laptop := newStore(t.TempDir(), &fakeKC{broken: true})
	if _, err := pc.Set("gemini", "AIza-pc"); err != nil {
		t.Fatal(err)
	}
	pc.Set("anthropic", "not-synced-but-is-listed")
	pc.Set("blaze-account:session", "never-synced")
	r, err := pc.Sync(ctx, nil, srv.URL, "tok")
	if err != nil || !r.Pushed || st.rev != 1 {
		t.Fatalf("%+v %v rev=%d", r, err, st.rev)
	}
	r, err = laptop.Sync(ctx, nil, srv.URL, "tok")
	if err != nil || !reflect.DeepEqual(r.Pulled, []string{"anthropic", "gemini"}) || r.Pushed {
		t.Fatalf("%+v %v", r, err)
	}
	if laptop.Main("gemini") != "AIza-pc" || laptop.Get("blaze-account:session") != "" {
		t.Fatal("wrong keys pulled")
	}
	// Remove on the laptop, the PC follows.
	if err := laptop.SetAll("anthropic", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := laptop.Sync(ctx, nil, srv.URL, "tok"); err != nil {
		t.Fatal(err)
	}
	if _, err := pc.Sync(ctx, nil, srv.URL, "tok"); err != nil {
		t.Fatal(err)
	}
	if pc.Main("anthropic") != "" {
		t.Fatal("deletion didn't sync")
	}
	// Nothing changed: no write.
	rev := st.rev
	if r, err := pc.Sync(ctx, nil, srv.URL, "tok"); err != nil || r.Pushed || st.rev != rev {
		t.Fatalf("idle sync wrote: %+v %v", r, err)
	}
	if _, err := pc.Sync(ctx, nil, srv.URL, "bad"); !errors.Is(err, ErrSignedOut) {
		t.Fatalf("got %v", err)
	}
}

func TestSyncUnsupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"error":"not_found"}`))
	}))
	defer srv.Close()
	s := newStore(t.TempDir(), &fakeKC{})
	if _, err := s.Sync(context.Background(), nil, srv.URL, "tok"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
}
