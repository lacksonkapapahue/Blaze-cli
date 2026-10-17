package cloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRefreshAuthFailureClassification(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   bool
	}{
		{400, `{"error":"invalid_grant"}`, true},
		{400, `{"error":"invalid_client"}`, true},
		{400, `{"error":"unauthorized_client"}`, true},
		{401, `{"error":"unauthorized"}`, true},
		{401, ``, true},
		{400, `{"error":"bad_request"}`, false},
		{429, `{"error":"rate_limited","retry_after":30}`, false},
		{500, `{"error":"invalid_grant"}`, false},
		{502, `<html>bad gateway</html>`, false},
		{503, ``, false},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			fmt.Fprint(w, tc.body)
		}))
		_, err := New(srv.URL).Refresh(context.Background(), "google", "rt")
		srv.Close()
		if err == nil {
			t.Fatalf("%d %s: expected an error", tc.status, tc.body)
		}
		if got := IsAuthFailure(err); got != tc.want {
			t.Errorf("%d %s: IsAuthFailure=%v want %v (err %v)", tc.status, tc.body, got, tc.want, err)
		}
	}
}

func TestTransientErrorsAreNotAuthFailures(t *testing.T) {
	// cancelled context
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New(srv.URL).Refresh(ctx, "google", "rt")
	if !errors.Is(err, ErrCancelled) || IsAuthFailure(err) {
		t.Fatalf("cancelled: %v", err)
	}
	// unreachable
	_, err = New("http://127.0.0.1:1").Refresh(context.Background(), "google", "rt")
	var un *Unreachable
	if !errors.As(err, &un) || IsAuthFailure(err) {
		t.Fatalf("unreachable: %v", err)
	}
	for _, e := range []error{nil, context.DeadlineExceeded, errors.New("boom"), &APIError{Status: 502, Code: "no_token"}} {
		if IsAuthFailure(e) {
			t.Errorf("%v should not be an auth failure", e)
		}
	}
}
