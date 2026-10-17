package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/lacksonkapapahue/blaze-cli/internal/cloud"
)

func TestSessionRefusedOnlyOn401(t *testing.T) {
	if !sessionRefused(&cloud.APIError{Status: 401, Code: "unauthorized"}) {
		t.Fatal("401 should sign out")
	}
	if !sessionRefused(fmt.Errorf("me: %w", &cloud.APIError{Status: 401, Code: "unauthorized"})) {
		t.Fatal("wrapped 401 should sign out")
	}
	for _, e := range []error{
		nil,
		&cloud.Unreachable{Err: errors.New("dial tcp: no such host")},
		cloud.ErrCancelled,
		context.DeadlineExceeded,
		&cloud.APIError{Status: 500, Code: "http_500"},
		&cloud.APIError{Status: 502, Code: "http_502"},
		&cloud.APIError{Status: 429, Code: "rate_limited"},
		&cloud.APIError{Status: 403, Code: "forbidden"},
		errors.New("unexpected answer from the sign-in service"),
	} {
		if sessionRefused(e) {
			t.Errorf("%v must not sign out", e)
		}
	}
}
