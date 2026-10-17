package connect

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestKeychainFileFallbackSetGetDelete(t *testing.T) {
	keyring.MockInitWithError(errors.New("no secret service"))
	t.Cleanup(keyring.MockInit)
	dir := filepath.Join(t.TempDir(), "blaze")
	s := KeychainAt(dir)

	if err := s.Set("session", "token-value"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("session")
	if err != nil || got != "token-value" {
		t.Fatalf("Get() = %q, %v; want token-value", got, err)
	}
	path := filepath.Join(dir, "secrets.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("secrets.json mode = %04o, want 0600", mode)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Fatalf("secrets directory mode = %04o, want 0700", mode)
	}
	if err := s.Delete("session"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("session"); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("Get after Delete() error = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("secrets.json after deleting its last value: stat error = %v", err)
	}
}

func TestUsedFileFallbackIsOneTime(t *testing.T) {
	secretsFileMu.Lock()
	oldUsed, oldNotice := fileFallbackUsed, fallbackNoticeSent
	fileFallbackUsed, fallbackNoticeSent = true, false
	secretsFileMu.Unlock()
	t.Cleanup(func() {
		secretsFileMu.Lock()
		fileFallbackUsed, fallbackNoticeSent = oldUsed, oldNotice
		secretsFileMu.Unlock()
	})

	if !UsedFileFallback() {
		t.Fatal("UsedFileFallback() = false on first check")
	}
	if UsedFileFallback() {
		t.Fatal("UsedFileFallback() = true more than once")
	}
}

func TestKeychainSetPrefersSystemKeychain(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)
	dir := t.TempDir()
	s := KeychainAt(dir)

	if err := s.Set("session", "token-value"); err != nil {
		t.Fatal(err)
	}
	got, err := keyring.Get(service, "session")
	if err != nil || got != "token-value" {
		t.Fatalf("keyring.Get() = %q, %v; want token-value", got, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "secrets.json"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "token-value") {
		t.Fatal("keyring-backed secret was also written to secrets.json")
	}
}

func TestKeychainReadsFileWhenKeychainReturnsNotFound(t *testing.T) {
	keyring.MockInitWithError(errors.New("no secret service"))
	t.Cleanup(keyring.MockInit)
	s := KeychainAt(t.TempDir())
	if err := s.Set("session", "token-value"); err != nil {
		t.Fatal(err)
	}

	keyring.MockInit()
	got, err := s.Get("session")
	if err != nil || got != "token-value" {
		t.Fatalf("Get() after keychain restore = %q, %v; want fallback value", got, err)
	}
}
