package keystore

// Syncing model keys with other devices (Blaze Android, another PC) through the Blaze
// account. This file is identical in Blaze-cli and Blaze-dekstop; change both, and
// keep the wire format in step with Blaze Android (connect/KeySync.kt) and the
// blaze-auth Worker (Blaze-dekstop cloud/src/keys.ts).
//
//	GET  /v1/keys   Authorization: Bearer <session>
//	     -> {"rev":n, "blob":"<base64>" or "", "key":"<base64, 32 bytes>", "updated_at":unix}
//	POST /v1/keys   {"rev":n, "blob":"<base64>"}  -> {"rev":n+1}
//	     409 {"error":"conflict","rev":m} when another device wrote first.
//
//	blob      = base64(nonce[12] || AES-256-GCM(plaintext, aad "blaze-keys-v1"))
//	plaintext = {"v":1,"keys":{"gemini":{"k":["main","extra"],"t":<unix ms>}, ...}}
//
// An empty "k" with a "t" is a deletion. For each provider the newer "t" wins; keys
// whose age is unknown here (saved before syncing existed) are merged with the
// account's instead of replacing them, so nothing is lost on the first sync.
//
// The Worker only ever sees the encrypted blob. The blob key is a random per-account
// key the Worker hands to signed-in sessions, kept in a separate record: this keeps
// keys out of logs and backups of the blob, but it is not end-to-end encryption
// (whoever controls the Worker and its KV could decrypt).

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"
)

// ErrUnsupported means the Blaze service has no key sync yet (an older Worker).
var ErrUnsupported = errors.New("key sync isn't available on Blaze's service yet")

// ErrSignedOut means the session was rejected.
var ErrSignedOut = errors.New("signed out of the Blaze account")

const aad = "blaze-keys-v1"

// Entry is one provider's keys in the synced blob.
type Entry struct {
	K []string `json:"k"`
	T int64    `json:"t"`
}

type payload struct {
	V    int              `json:"v"`
	Keys map[string]Entry `json:"keys"`
}

// SyncResult says which providers changed where.
type SyncResult struct {
	Pulled []string // updated on this computer from the account
	Pushed bool     // the account copy was updated
}

type remoteState struct {
	Rev     int64  `json:"rev"`
	Blob    string `json:"blob"`
	Key     string `json:"key"`
	Updated int64  `json:"updated_at"`
}

func request(ctx context.Context, hc *http.Client, method, url, token string, body any, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Blaze")
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if out != nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, out)
	}
	return resp.StatusCode, nil
}

// Encrypt seals a payload with the account's blob key.
func Encrypt(key []byte, plain []byte) (string, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(g.Seal(nonce, nonce, plain, []byte(aad))), nil
}

// Decrypt opens a blob made by Encrypt (here or on another device).
func Decrypt(key []byte, blob string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return nil, err
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	if len(raw) < g.NonceSize()+g.Overhead() {
		return nil, errors.New("blob too short")
	}
	return g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], []byte(aad))
}

// local is this computer's synced providers.
func (s *Store) local() map[string]Entry {
	out := map[string]Entry{}
	meta := s.readMeta()
	for _, p := range Synced {
		k := s.All(p)
		t := meta.Changed[p]
		if len(k) > 0 || t > 0 {
			out[p] = Entry{K: k, T: t}
		}
	}
	return out
}

// Merge combines this computer's entries with the account's. It returns the merged
// set, what to apply here (provider -> entry), and whether the account needs the
// merged set. now stamps entries whose local age was unknown.
func Merge(local, remote map[string]Entry, now int64) (merged map[string]Entry, apply map[string]Entry, push bool) {
	merged, apply = map[string]Entry{}, map[string]Entry{}
	ids := map[string]bool{}
	for p := range local {
		ids[p] = true
	}
	for p := range remote {
		ids[p] = true
	}
	for p := range ids {
		l, lok := local[p]
		r, rok := remote[p]
		l.K, r.K = clean(l.K), clean(r.K)
		switch {
		case !rok:
			if l.T == 0 {
				l.T = now
				if IsSynced(p) {
					apply[p] = l // give the local keys a known age
				}
			}
			merged[p] = l
			push = true
		case !lok:
			merged[p] = r
			if len(r.K) > 0 && IsSynced(p) {
				apply[p] = r
			}
		case l.T == 0:
			// Saved here before syncing existed: keep both sides' keys.
			u := clean(append(append([]string{}, r.K...), l.K...))
			if reflect.DeepEqual(u, r.K) {
				merged[p] = r
			} else {
				merged[p] = Entry{K: u, T: now}
				push = true
			}
			if IsSynced(p) {
				apply[p] = merged[p] // also gives the local keys a known age
			}
		case l.T > r.T:
			merged[p] = l
			push = true
		case r.T > l.T:
			merged[p] = r
			if IsSynced(p) {
				apply[p] = r // also refreshes the local timestamp when the keys already match
			}
		default:
			merged[p] = r
			if !reflect.DeepEqual(l.K, r.K) && IsSynced(p) {
				apply[p] = r
			}
		}
	}
	return merged, apply, push
}

// Sync merges this computer's model keys with the Blaze account's copy. base is the
// blaze-auth URL, token the session. Safe to call often; it is a no-op when nothing
// changed. ErrUnsupported means the service has no key sync yet.
func (s *Store) Sync(ctx context.Context, hc *http.Client, base, token string) (*SyncResult, error) {
	if token == "" {
		return nil, ErrSignedOut
	}
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	base = strings.TrimRight(base, "/")
	res := &SyncResult{}
	for attempt := 0; attempt < 3; attempt++ {
		var st remoteState
		code, err := request(ctx, hc, "GET", base+"/v1/keys", token, nil, &st)
		if err != nil {
			return nil, err
		}
		switch {
		case code == 404 || code == 405:
			return nil, ErrUnsupported
		case code == 401:
			return nil, ErrSignedOut
		case code >= 400:
			return nil, fmt.Errorf("key sync failed (http %d)", code)
		}
		key, err := base64.StdEncoding.DecodeString(st.Key)
		if err != nil || len(key) != 32 {
			return nil, errors.New("key sync: the service sent no usable key")
		}
		remote := map[string]Entry{}
		if st.Blob != "" {
			plain, err := Decrypt(key, st.Blob)
			if err != nil {
				return nil, errors.New("key sync: couldn't open the account's keys")
			}
			var p payload
			if err := json.Unmarshal(plain, &p); err != nil {
				return nil, errors.New("key sync: the account's keys are unreadable")
			}
			if p.V > 1 {
				return nil, errors.New("key sync: the account's keys were saved by a newer Blaze; update Blaze")
			}
			if p.Keys != nil {
				remote = p.Keys
			}
		}
		merged, apply, push := Merge(s.local(), remote, time.Now().UnixMilli())
		ids := make([]string, 0, len(apply))
		for p := range apply {
			ids = append(ids, p)
		}
		sort.Strings(ids)
		for _, p := range ids {
			e := apply[p]
			before := s.All(p)
			if err := s.setAll(p, e.K, e.T); err != nil {
				return res, err
			}
			if !reflect.DeepEqual(before, clean(e.K)) && !contains(res.Pulled, p) {
				res.Pulled = append(res.Pulled, p)
			}
		}
		if !push {
			s.noteSync(st.Rev)
			return res, nil
		}
		plain, _ := json.Marshal(payload{V: 1, Keys: merged})
		blob, err := Encrypt(key, plain)
		if err != nil {
			return res, err
		}
		var put struct {
			Rev   int64  `json:"rev"`
			Error string `json:"error"`
		}
		code, err = request(ctx, hc, "POST", base+"/v1/keys", token, map[string]any{"rev": st.Rev, "blob": blob}, &put)
		if err != nil {
			return res, err
		}
		switch {
		case code == 409:
			continue // another device wrote first: merge again on top of it
		case code == 401:
			return res, ErrSignedOut
		case code >= 400:
			return res, fmt.Errorf("key sync failed (http %d %s)", code, put.Error)
		}
		res.Pushed = true
		s.noteSync(put.Rev)
		return res, nil
	}
	return res, errors.New("key sync: other devices kept writing; try again")
}

func (s *Store) noteSync(rev int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.readMeta()
	m.LastSync = time.Now().Unix()
	m.SyncRev = rev
	_ = s.writeMeta(m)
}

// LastSync is when this computer last synced keys (zero when never).
func (s *Store) LastSync() time.Time {
	if t := s.readMeta().LastSync; t > 0 {
		return time.Unix(t, 0)
	}
	return time.Time{}
}

func contains(l []string, x string) bool {
	for _, v := range l {
		if v == x {
			return true
		}
	}
	return false
}
