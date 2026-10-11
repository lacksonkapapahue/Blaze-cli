package notify

import (
	"testing"
)

func TestSendRateLimitsDuplicates(t *testing.T) {
	t.Setenv("BLAZE_HOME", t.TempDir())
	sendMu.Lock()
	probed, execM, sent = true, &Method{Kind: "exec", Name: "true", Argv: []string{"true"}}, nil
	sendMu.Unlock()
	if _, err := Send(Note{Title: "Build done", Body: "all green"}); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if _, err := Send(Note{Title: "Build done", Body: "all green"}); err != ErrRateLimited {
		t.Fatalf("duplicate send: got %v, want ErrRateLimited", err)
	}
	if h := History(); len(h) != 1 || h[0].Method != "true" {
		t.Fatalf("history = %+v", h)
	}
}

func TestSendNoMethod(t *testing.T) {
	t.Setenv("BLAZE_HOME", t.TempDir())
	sendMu.Lock()
	probed, execM, sent = true, nil, nil
	sendMu.Unlock()
	if _, err := Send(Note{Title: "x"}); err != ErrNoMethod {
		t.Fatalf("got %v, want ErrNoMethod", err)
	}
}
