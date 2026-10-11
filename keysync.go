package main

// Model keys follow the Blaze account: on this computer Blaze CLI and Blaze Desktop
// already share one key store (internal/keystore); while signed in, the store is also
// merged with the account's encrypted copy so the phone (Blaze Android) and other PCs
// get the same keys. Nothing here runs when signed out.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lacksonkapapahue/blaze-cli/internal/config"
	"github.com/lacksonkapapahue/blaze-cli/internal/keystore"
	"github.com/lacksonkapapahue/blaze-cli/internal/ui"
)

func init() {
	// A key saved or removed here reaches the account right away (best effort).
	config.KeysChanged = func(string) { _, _ = syncKeys(8 * time.Second) }
}

// syncKeys merges the shared key store with the account. ErrSignedOut when signed out.
func syncKeys(timeout time.Duration) (*keystore.SyncResult, error) {
	ac := getAccount()
	tok, _ := ac.sec.Get(sessionKey)
	if tok == "" {
		return nil, keystore.ErrSignedOut
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	r, err := config.Keys().Sync(ctx, ac.client.HC, ac.client.Base, tok)
	if errors.Is(err, keystore.ErrSignedOut) {
		if _, _, aerr := whoCheck(ac, tok); aerr != nil {
			_ = ac.setSession(nil)
		}
	}
	return r, err
}

// whoCheck confirms the session; an error means it was refused.
func whoCheck(ac *account, tok string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	u, err := ac.client.Me(ctx, tok)
	if err != nil {
		return "", false, err
	}
	return u.Email, true, nil
}

// autoSyncKeys runs quietly at start when signed in and the last sync is over an hour old.
func autoSyncKeys() {
	if tok, _ := getAccount().sec.Get(sessionKey); tok == "" {
		return
	}
	if time.Since(config.Keys().LastSync()) < time.Hour {
		return
	}
	_, _ = syncKeys(15 * time.Second)
}

func providerNames(ids []string) string {
	var n []string
	for _, id := range ids {
		if p, ok := config.ProviderByID(id); ok {
			name, _, _ := strings.Cut(p.Name, " (")
			n = append(n, name)
		} else {
			n = append(n, id)
		}
	}
	return strings.Join(n, ", ")
}

// reportSync prints one line about a sync.
func reportSync(r *keystore.SyncResult, err error) {
	switch {
	case errors.Is(err, keystore.ErrSignedOut):
		fail("Sign in with blaze login to sync keys with your phone and other computers.")
	case errors.Is(err, keystore.ErrUnsupported):
		ui.Step("Keys are shared with Blaze Desktop on this computer. Syncing them to your phone isn't switched on in Blaze's service yet.")
	case err != nil:
		fail("Couldn't sync keys: " + err.Error())
	case len(r.Pulled) > 0:
		ui.StepDone(true, "Keys synced", "got "+providerNames(r.Pulled)+" from your Blaze account", 0)
	case r.Pushed:
		ui.StepDone(true, "Keys synced", "your keys are now on your Blaze account", 0)
	default:
		ui.StepDone(true, "Keys synced", "already up to date", 0)
	}
}

// cmdKeys is `blaze keys [sync]`.
func cmdKeys(args []string) error {
	if len(args) > 0 && args[0] == "sync" {
		r, err := syncKeys(30 * time.Second)
		reportSync(r, err)
		return nil
	}
	if len(args) > 0 {
		return fmt.Errorf("unknown: blaze keys %s (try blaze keys or blaze keys sync)", args[0])
	}
	ks := config.Keys()
	found := false
	for _, p := range config.Providers {
		if config.Keyless(p.ID) {
			continue
		}
		n := len(ks.All(p.ID))
		if n == 0 {
			continue
		}
		found = true
		what := "1 key"
		if n > 1 {
			what = fmt.Sprintf("%d keys", n)
		}
		ui.Step(fmt.Sprintf("%-20s %s · %s", p.Name, what, ks.Where(p.ID)))
	}
	if !found {
		ui.Step("No keys saved yet. Run blaze setup, or add one in Blaze Desktop.")
	}
	ui.Println("  " + ui.Weak("Shared with Blaze Desktop on this computer."))
	if tok, _ := getAccount().sec.Get(sessionKey); tok != "" {
		if t := ks.LastSync(); !t.IsZero() {
			ui.Println("  " + ui.Weak("Last synced with your Blaze account "+t.Format("2 Jan 15:04")+" · blaze keys sync"))
		} else {
			ui.Println("  " + ui.Weak("Not synced with your Blaze account yet · blaze keys sync"))
		}
	} else {
		ui.Println("  " + ui.Weak("Sign in (blaze login) to use them on your phone too."))
	}
	return nil
}
