// Package keystore is the one place Blaze CLI and Blaze Desktop keep model API keys on
// this computer, so a key added in either app works in the other.
//
// This file is identical in Blaze-cli and Blaze-dekstop (internal/keystore); change both.
//
// Layout (shared by both apps, never rename):
//
//   - OS keychain, service "blaze-cli", account "<provider>" for the main key and
//     "<provider>#extra" for the provider's extra keys (a JSON list, Desktop's key ring).
//   - When there is no keychain (headless Linux, locked Secret Service), the same
//     accounts go to ~/.blaze/keys.json (mode 0600), read fresh on every access so two
//     running apps never overwrite each other's keys from stale memory.
//   - ~/.blaze/keys.meta.json (0600, no secrets): when each provider's keys last
//     changed here, used to merge with other devices through the Blaze account.
//   - Legacy: Blaze up to CLI 0.6.x / Desktop 0.7.x kept keychain-less keys under
//     "keys" in ~/.blaze/config.json. They are copied into the store once and still
//     read as a last resort, and are left in place so an older Blaze on this PC keeps
//     working. Deleting a key removes it there too.
package keystore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
)

// Service is the OS keychain service name both apps use.
const Service = "blaze-cli"

// ExtraSuffix marks the account holding a provider's extra keys (JSON list).
const ExtraSuffix = "#extra"

// Synced are the model providers whose keys follow the Blaze account to other devices
// (the phone included). Connector tokens, the session and the updater's "github" token
// never leave this computer.
var Synced = []string{"gemini", "groq", "cerebras", "openrouter", "mistral", "nvidia", "deepseek", "moonshot", "openai", "anthropic", "xai"}

// IsSynced reports whether a provider's keys are synced.
func IsSynced(provider string) bool {
	for _, p := range Synced {
		if p == provider {
			return true
		}
	}
	return false
}

// Backend is a keychain. Tests swap it.
type Backend interface {
	Get(account string) (string, error)
	Set(account, value string) error
	Delete(account string) error
}

type osKeyring struct{}

func (osKeyring) Get(a string) (string, error) { return keyring.Get(Service, a) }
func (osKeyring) Set(a, v string) error        { return keyring.Set(Service, a, v) }
func (osKeyring) Delete(a string) error        { return keyring.Delete(Service, a) }

// Keychain is the backend Open uses. Tests set it to a fake.
var Keychain Backend = osKeyring{}

// Store is the key store under one Blaze home (~/.blaze).
type Store struct {
	dir string
	kc  Backend
	mu  sync.Mutex
}

var (
	omu    sync.Mutex
	opened = map[string]*Store{}
)

// Open returns the store for dir (normally config.Dir()) and runs the one-time
// migration of legacy config.json keys.
func Open(dir string) *Store {
	omu.Lock()
	defer omu.Unlock()
	if s, ok := opened[dir]; ok && s.kc == Keychain {
		return s
	}
	s := &Store{dir: dir, kc: Keychain}
	s.migrate()
	opened[dir] = s
	return s
}

func (s *Store) fallbackPath() string { return filepath.Join(s.dir, "keys.json") }
func (s *Store) metaPath() string     { return filepath.Join(s.dir, "keys.meta.json") }
func (s *Store) legacyPath() string   { return filepath.Join(s.dir, "config.json") }

// Meta is keys.meta.json.
type Meta struct {
	Migrated int              `json:"migrated,omitempty"`
	Changed  map[string]int64 `json:"changed,omitempty"` // provider -> unix ms of the last change here
	LastSync int64            `json:"last_sync,omitempty"`
	SyncRev  int64            `json:"sync_rev,omitempty"`
}

// ---------------- small file helpers ----------------

func readMap(path string) map[string]string {
	m := map[string]string{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// writeAtomic writes a 0600 file through a temp file + rename.
func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_ = tmp.Chmod(0o600)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return os.Chmod(path, 0o600)
}

func writeMap(path string, m map[string]string) error {
	if len(m) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return writeAtomic(path, b)
}

func (s *Store) readMeta() Meta {
	var m Meta
	if b, err := os.ReadFile(s.metaPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	if m.Changed == nil {
		m.Changed = map[string]int64{}
	}
	return m
}

func (s *Store) writeMeta(m Meta) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	return writeAtomic(s.metaPath(), b)
}

// legacyKeys is "keys" in config.json.
func (s *Store) legacyKeys() map[string]string {
	var f struct {
		Keys map[string]string `json:"keys"`
	}
	if b, err := os.ReadFile(s.legacyPath()); err == nil {
		_ = json.Unmarshal(b, &f)
	}
	if f.Keys == nil {
		return map[string]string{}
	}
	return f.Keys
}

// dropLegacy removes accounts from config.json "keys", keeping every other field as is.
func (s *Store) dropLegacy(accounts ...string) error {
	b, err := os.ReadFile(s.legacyPath())
	if err != nil {
		return nil
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		return nil
	}
	var keys map[string]string
	if r, ok := raw["keys"]; !ok || json.Unmarshal(r, &keys) != nil || len(keys) == 0 {
		return nil
	}
	changed := false
	for _, a := range accounts {
		if _, ok := keys[a]; ok {
			delete(keys, a)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if len(keys) == 0 {
		delete(raw, "keys")
	} else {
		raw["keys"], _ = json.Marshal(keys)
	}
	out, _ := json.MarshalIndent(raw, "", "  ")
	return writeAtomic(s.legacyPath(), out)
}

// ---------------- migration ----------------

// migrate copies legacy config.json keys into the keychain (or keys.json) once.
// Keys already in the store win; the legacy copy stays for older Blaze versions.
func (s *Store) migrate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.readMeta()
	if m.Migrated >= 1 {
		return
	}
	legacy := s.legacyKeys()
	fb := readMap(s.fallbackPath())
	fbChanged := false
	for a, v := range legacy {
		if strings.TrimSpace(v) == "" {
			continue
		}
		if cur, err := s.kc.Get(a); err == nil && cur != "" {
			continue
		}
		if fb[a] != "" {
			continue
		}
		if s.kc.Set(a, v) != nil {
			fb[a] = v
			fbChanged = true
		}
	}
	if fbChanged {
		_ = writeMap(s.fallbackPath(), fb)
	}
	m.Migrated = 1
	_ = s.writeMeta(m)
}

// ---------------- reads and writes ----------------

// Get returns an account's value: keychain, then keys.json, then legacy config.json.
func (s *Store) Get(account string) string {
	if v, err := s.kc.Get(account); err == nil && v != "" {
		return v
	}
	if v := readMap(s.fallbackPath())[account]; v != "" {
		return v
	}
	return s.legacyKeys()[account]
}

// InKeychain reports whether the account currently lives in the OS keychain.
func (s *Store) InKeychain(account string) bool {
	v, err := s.kc.Get(account)
	return err == nil && v != ""
}

// Where describes where a stored account lives, for messages.
func (s *Store) Where(account string) string {
	if s.InKeychain(account) {
		return "your system keychain"
	}
	return "~/.blaze/keys.json (owner-only)"
}

func providerOf(account string) string { return strings.TrimSuffix(account, ExtraSuffix) }

// now is the time to stamp a change made here: the clock, but always after the
// provider's last stamp, so an edit made in the same millisecond as a sync still
// counts as newer and wins the merge.
func (s *Store) now(account string) int64 {
	t := time.Now().UnixMilli()
	if last := s.readMeta().Changed[providerOf(account)]; t <= last {
		t = last + 1
	}
	return t
}

func (s *Store) stamp(account string, at int64) {
	m := s.readMeta()
	m.Changed[providerOf(account)] = at
	_ = s.writeMeta(m)
}

// Set stores an account's value (an empty value deletes it) and returns where it went.
func (s *Store) Set(account, value string) (string, error) {
	return s.set(account, value, s.now(account))
}

func (s *Store) set(account, value string, at int64) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", s.del(account, at)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	where := ""
	fb := readMap(s.fallbackPath())
	if err := s.kc.Set(account, value); err == nil {
		where = "your system keychain"
		if _, ok := fb[account]; ok {
			delete(fb, account)
			if err := writeMap(s.fallbackPath(), fb); err != nil {
				return where, err
			}
		}
	} else {
		where = "~/.blaze/keys.json (owner-only)"
		fb[account] = value
		if err := writeMap(s.fallbackPath(), fb); err != nil {
			return "", err
		}
	}
	// A stale legacy copy would come back if the new key were removed later.
	_ = s.dropLegacy(account)
	if at > 0 {
		s.stamp(account, at)
	}
	return where, nil
}

// Delete removes an account everywhere (keychain, keys.json, legacy config.json).
func (s *Store) Delete(account string) error { return s.del(account, s.now(account)) }

func (s *Store) del(account string, at int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.kc.Delete(account) // not found is fine; the other copies still have to go
	fb := readMap(s.fallbackPath())
	if _, ok := fb[account]; ok {
		delete(fb, account)
		if err := writeMap(s.fallbackPath(), fb); err != nil {
			return err
		}
	}
	if err := s.dropLegacy(account); err != nil {
		return err
	}
	if at > 0 {
		s.stamp(account, at)
	}
	return nil
}

// ---------------- provider key rings ----------------

// Main is a provider's main key.
func (s *Store) Main(provider string) string { return s.Get(provider) }

// Extras are a provider's additional keys.
func (s *Store) Extras(provider string) []string {
	var out []string
	_ = json.Unmarshal([]byte(s.Get(provider+ExtraSuffix)), &out)
	return clean(out)
}

// SetExtras replaces a provider's extra keys.
func (s *Store) SetExtras(provider string, l []string) (string, error) {
	return s.setExtras(provider, l, s.now(provider))
}

func (s *Store) setExtras(provider string, l []string, at int64) (string, error) {
	l = clean(l)
	if len(l) == 0 {
		return "", s.del(provider+ExtraSuffix, at)
	}
	b, _ := json.Marshal(l)
	return s.set(provider+ExtraSuffix, string(b), at)
}

// All is the key ring: the main key first, then the extras, without duplicates.
func (s *Store) All(provider string) []string {
	return clean(append([]string{s.Main(provider)}, s.Extras(provider)...))
}

// SetAll replaces a provider's whole ring (first = main). An empty list deletes it.
func (s *Store) SetAll(provider string, keys []string) error {
	return s.setAll(provider, keys, s.now(provider))
}

func (s *Store) setAll(provider string, keys []string, at int64) error {
	keys = clean(keys)
	if len(keys) == 0 {
		if err := s.del(provider, at); err != nil {
			return err
		}
		return s.del(provider+ExtraSuffix, at)
	}
	if _, err := s.set(provider, keys[0], at); err != nil {
		return err
	}
	_, err := s.setExtras(provider, keys[1:], at)
	return err
}

func clean(l []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, k := range l {
		k = strings.TrimSpace(k)
		if k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// Changed is when a provider's keys last changed on this computer (0 = unknown).
func (s *Store) Changed(provider string) int64 { return s.readMeta().Changed[provider] }
