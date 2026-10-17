// Package connect is Blaze's connectors: the user's own accounts (Google, GitHub,
// Spotify, Notion, Slack) and MCP servers, offered to the agent as tools.
//
// Blaze has no server, so OAuth happens on this computer: the system browser signs
// in, the code comes back to a 127.0.0.1 loopback port (or the blaze:// protocol),
// Blaze exchanges it with PKCE and keeps the tokens in the OS keychain first; on
// machines without one (headless Linux, Codespaces, SSH) a 0600 file in the Blaze
// home, same policy as internal/keystore. Settings that are not secret (client IDs,
// enabled flags, per-tool approval) live in ~/.blaze/desktop/connectors.json.
package connect

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/zalando/go-keyring"
)

// Secrets keeps tokens and client secrets in the OS keychain first, falling back to
// a 0600 file in the Blaze home when no keychain is available.
type Secrets interface {
	Get(key string) (string, error)
	Set(key, value string) error
	Delete(key string) error
}

// Keychain is the OS keychain (Windows Credential Manager, macOS Keychain, Linux
// Secret Service), under the same service name as Blaze's model keys, with a
// private-file fallback for machines without a keychain.
func Keychain() Secrets { return KeychainAt(config.Dir()) }

// KeychainAt creates a secret store using dir for its private-file fallback.
func KeychainAt(dir string) Secrets {
	return &keychain{path: filepath.Join(dir, "secrets.json")}
}

type keychain struct{ path string }

const service = "blaze-cli"

var (
	secretsFileMu      sync.Mutex
	fileFallbackUsed   bool
	fallbackNoticeSent bool
)

// UsedFileFallback reports true once per process after a secret has used the
// private-file fallback.
func UsedFileFallback() bool {
	secretsFileMu.Lock()
	defer secretsFileMu.Unlock()
	if !fileFallbackUsed || fallbackNoticeSent {
		return false
	}
	fallbackNoticeSent = true
	return true
}

func (s *keychain) Get(k string) (string, error) {
	if v, err := keyring.Get(service, k); err == nil {
		return v, nil
	}
	secretsFileMu.Lock()
	defer secretsFileMu.Unlock()
	secrets, err := readSecrets(s.path)
	if err != nil {
		return "", err
	}
	if v, ok := secrets[k]; ok {
		fileFallbackUsed = true
		return v, nil
	}
	return "", keyring.ErrNotFound
}

func (s *keychain) Set(k, v string) error {
	if err := keyring.Set(service, k, v); err == nil {
		secretsFileMu.Lock()
		defer secretsFileMu.Unlock()
		secrets, readErr := readSecrets(s.path)
		if readErr != nil {
			fmt.Fprintln(os.Stderr, "blaze: couldn't remove the stale sign-in fallback copy:", readErr)
			return nil
		}
		if _, ok := secrets[k]; ok {
			delete(secrets, k)
			if err := writeSecrets(s.path, secrets); err != nil {
				fmt.Fprintln(os.Stderr, "blaze: couldn't remove the stale sign-in fallback copy:", err)
			}
		}
		return nil
	} else {
		secretsFileMu.Lock()
		defer secretsFileMu.Unlock()
		secrets, readErr := readSecrets(s.path)
		if readErr != nil {
			return errors.New("couldn't save your sign-in (keychain: " + err.Error() + "; file: " + readErr.Error() + ")")
		}
		if secrets == nil {
			secrets = make(map[string]string)
		}
		secrets[k] = v
		if fileErr := writeSecrets(s.path, secrets); fileErr != nil {
			return errors.New("couldn't save your sign-in (keychain: " + err.Error() + "; file: " + fileErr.Error() + ")")
		}
		fileFallbackUsed = true
	}
	return nil
}

func (s *keychain) Delete(k string) error {
	_ = keyring.Delete(service, k)
	secretsFileMu.Lock()
	defer secretsFileMu.Unlock()
	secrets, err := readSecrets(s.path)
	if err != nil {
		return err
	}
	if _, ok := secrets[k]; !ok {
		return nil
	}
	delete(secrets, k)
	if len(secrets) == 0 {
		if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return writeSecrets(s.path, secrets)
}

func readSecrets(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var secrets map[string]string
	if err := json.Unmarshal(b, &secrets); err != nil {
		return nil, err
	}
	return secrets, nil
}

func writeSecrets(path string, secrets map[string]string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(secrets)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".secrets-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// MemSecrets is an in-memory store for tests.
type MemSecrets struct {
	mu sync.Mutex
	M  map[string]string
}

func (m *MemSecrets) Get(k string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.M[k]; ok {
		return v, nil
	}
	return "", keyring.ErrNotFound
}
func (m *MemSecrets) Set(k, v string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.M == nil {
		m.M = map[string]string{}
	}
	m.M[k] = v
	return nil
}
func (m *MemSecrets) Delete(k string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.M, k)
	return nil
}

// Token is what a provider handed back, kept as JSON in the keychain.
type Token struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	Scope        string `json:"scope,omitempty"`
	ExpiresAt    int64  `json:"expires_at,omitempty"` // unix ms, 0 = doesn't expire
}

// ConnState is one connector's saved, non-secret settings.
type ConnState struct {
	ClientID    string            `json:"client_id,omitempty"`
	Redirect    string            `json:"redirect,omitempty"` // "" (loopback) or "scheme" (blaze://)
	Disabled    bool              `json:"disabled,omitempty"`
	Account     string            `json:"account,omitempty"` // "luke@gmail.com", "@lacksonkapapahue"
	Scopes      []string          `json:"scopes,omitempty"`  // granted
	Expired     bool              `json:"expired,omitempty"` // refresh failed: needs Reconnect
	Connected   bool              `json:"connected,omitempty"`
	Via         string            `json:"via,omitempty"` // "broker": one-click through Blaze's sign-in service
	ConnectedAt time.Time         `json:"connected_at,omitempty"`
	Modes       map[string]string `json:"modes,omitempty"` // tool -> ask | always | off
}

// MCPTool is one tool a server listed (cached so the agent's list is instant).
type MCPTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Schema      map[string]any `json:"input_schema,omitempty"`
	ReadOnly    bool           `json:"read_only,omitempty"`
	Destructive bool           `json:"destructive,omitempty"`
}

// MCPServer is a remote (Streamable HTTP or SSE) or local (stdio) MCP server.
type MCPServer struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Transport string            `json:"transport"` // http | sse | stdio
	URL       string            `json:"url,omitempty"`
	Header    string            `json:"header,omitempty"` // auth header name, default Authorization
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Disabled  bool              `json:"disabled,omitempty"`
	Modes     map[string]string `json:"modes,omitempty"`
	Tools     []MCPTool         `json:"tools,omitempty"`
	Error     string            `json:"error,omitempty"`
	Checked   time.Time         `json:"checked,omitempty"`
}

type storeData struct {
	Connectors map[string]*ConnState `json:"connectors"`
	MCP        []*MCPServer          `json:"mcp"`
}

// Store keeps connectors.json and the keychain entries in step.
type Store struct {
	mu   sync.Mutex
	path string
	sec  Secrets
	d    storeData
	has  sync.Map // connector id -> bool: token present (saves a keychain read per check)
}

func OpenStore(dir string, sec Secrets) *Store {
	s := &Store{path: filepath.Join(dir, "connectors.json"), sec: sec}
	b, _ := os.ReadFile(s.path)
	_ = json.Unmarshal(b, &s.d)
	if s.d.Connectors == nil {
		s.d.Connectors = map[string]*ConnState{}
	}
	return s
}

func (s *Store) saveLocked() error {
	_ = os.MkdirAll(filepath.Dir(s.path), 0o700)
	b, _ := json.MarshalIndent(s.d, "", "  ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Conn returns a copy of a connector's state.
func (s *Store) Conn(id string) ConnState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.d.Connectors[id]; c != nil {
		cp := *c
		cp.Scopes = append([]string(nil), c.Scopes...)
		cp.Modes = map[string]string{}
		for k, v := range c.Modes {
			cp.Modes[k] = v
		}
		return cp
	}
	return ConnState{Modes: map[string]string{}}
}

// Update changes a connector's state and saves it.
func (s *Store) Update(id string, fn func(c *ConnState)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.d.Connectors[id]
	if c == nil {
		c = &ConnState{}
		s.d.Connectors[id] = c
	}
	fn(c)
	return s.saveLocked()
}

func tokenKey(id string) string  { return "connector:" + id }
func secretKey(id string) string { return "connector:" + id + "#client_secret" }
func mcpKey(id string) string    { return "mcp:" + id }

func (s *Store) Token(id string) *Token {
	v, err := s.sec.Get(tokenKey(id))
	if err != nil || v == "" {
		return nil
	}
	var t Token
	if json.Unmarshal([]byte(v), &t) != nil || t.AccessToken == "" {
		return nil
	}
	return &t
}

func (s *Store) SetToken(id string, t *Token) error {
	b, _ := json.Marshal(t)
	s.has.Delete(id)
	return s.sec.Set(tokenKey(id), string(b))
}

func (s *Store) DeleteToken(id string) { s.has.Delete(id); _ = s.sec.Delete(tokenKey(id)) }

// HasToken is whether a connector has a saved token (cached).
func (s *Store) HasToken(id string) bool {
	if v, ok := s.has.Load(id); ok {
		return v.(bool)
	}
	ok := s.Token(id) != nil
	s.has.Store(id, ok)
	return ok
}

func (s *Store) ClientSecret(id string) string {
	v, _ := s.sec.Get(secretKey(id))
	return v
}

func (s *Store) SetClientSecret(id, v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		_ = s.sec.Delete(secretKey(id))
		return nil
	}
	return s.sec.Set(secretKey(id), v)
}

// ---------- MCP servers ----------

func (s *Store) MCPList() []MCPServer {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]MCPServer, 0, len(s.d.MCP))
	for _, m := range s.d.MCP {
		out = append(out, copyMCP(m))
	}
	return out
}

func copyMCP(m *MCPServer) MCPServer {
	cp := *m
	cp.Args = append([]string(nil), m.Args...)
	cp.Tools = append([]MCPTool(nil), m.Tools...)
	cp.Modes = map[string]string{}
	for k, v := range m.Modes {
		cp.Modes[k] = v
	}
	return cp
}

func (s *Store) MCP(id string) (MCPServer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.d.MCP {
		if m.ID == id {
			return copyMCP(m), true
		}
	}
	return MCPServer{}, false
}

func (s *Store) PutMCP(m MCPServer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, x := range s.d.MCP {
		if x.ID == m.ID {
			s.d.MCP[i] = &m
			return s.saveLocked()
		}
	}
	s.d.MCP = append(s.d.MCP, &m)
	return s.saveLocked()
}

func (s *Store) UpdateMCP(id string, fn func(m *MCPServer)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.d.MCP {
		if m.ID == id {
			fn(m)
			return s.saveLocked()
		}
	}
	return errors.New("no MCP server " + id)
}

func (s *Store) RemoveMCP(id string) error {
	s.mu.Lock()
	var keep []*MCPServer
	for _, m := range s.d.MCP {
		if m.ID != id {
			keep = append(keep, m)
		}
	}
	s.d.MCP = keep
	err := s.saveLocked()
	s.mu.Unlock()
	_ = s.sec.Delete(mcpKey(id))
	return err
}

func (s *Store) MCPToken(id string) string {
	v, _ := s.sec.Get(mcpKey(id))
	return v
}

func (s *Store) SetMCPToken(id, v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		_ = s.sec.Delete(mcpKey(id))
		return nil
	}
	return s.sec.Set(mcpKey(id), v)
}
