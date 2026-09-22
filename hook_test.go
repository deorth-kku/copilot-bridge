package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunHookForwardsStdin(t *testing.T) {
	var gotPath, gotMethod, gotCT string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
	}))
	defer srv.Close()

	payload := `{"hook_event_name":"Stop","session_id":"abc","cwd":"/x"}`
	if err := runHook(srv.URL, strings.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/hook" {
		t.Errorf("path = %q, want /api/hook", gotPath)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q, want application/json", gotCT)
	}
	if string(gotBody) != payload {
		t.Errorf("forwarded body = %q, want %q", gotBody, payload)
	}
}

func TestRunHookBridgeDown(t *testing.T) {
	// Nothing listens on 127.0.0.1:1: the error must be returned (the
	// caller decides to exit 0 anyway).
	if err := runHook("http://127.0.0.1:1", strings.NewReader(`{"hook_event_name":"Stop"}`)); err == nil {
		t.Fatal("expected an error when the bridge is unreachable")
	}
}

func TestRunHookNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	if err := runHook(srv.URL, strings.NewReader(`{}`)); err == nil {
		t.Fatal("expected an error for a non-2xx bridge response")
	}
}
