package main

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	if err := runHook(srv.URL, "", strings.NewReader(payload)); err != nil {
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
	if err := runHook("http://127.0.0.1:1", "", strings.NewReader(`{"hook_event_name":"Stop"}`)); err == nil {
		t.Fatal("expected an error when the bridge is unreachable")
	}
}

func TestRunHookNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	if err := runHook(srv.URL, "", strings.NewReader(`{}`)); err == nil {
		t.Fatal("expected an error for a non-2xx bridge response")
	}
}

// writeTranscript writes one transcript fixture file and returns its path.
func writeTranscript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLastAssistantText(t *testing.T) {
	// VS Code format: the last non-empty assistant turn wins.
	p := writeTranscript(t, strings.Join([]string{
		`{"type":"user.message","data":{"content":"hi"}}`,
		`{"type":"assistant.message","data":{"content":"first answer"}}`,
		`not json at all`,
		`{"type":"assistant.message","data":{"content":"   "}}`,
		`{"type":"assistant.message","data":{"content":"  last answer  "}}`,
	}, "\n"))
	if got := lastAssistantText(p); got != "last answer" {
		t.Fatalf("VS Code format: got %q", got)
	}

	// Role-based format: nested message, top-level role, string content.
	p2 := writeTranscript(t, strings.Join([]string{
		`{"message":{"role":"assistant","content":"plain"}}`,
		`{"message":{"role":"assistant","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}}`,
		`{"role":"assistant","content":"top-level role"}`,
		`{"message":{"role":"user","content":"ignored"}}`,
	}, "\n"))
	if got := lastAssistantText(p2); got != "top-level role" {
		t.Fatalf("role-based: got %q", got)
	}

	// Block list flattening.
	p3 := writeTranscript(t, `{"message":{"role":"assistant","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}}`)
	if got := lastAssistantText(p3); got != "a\nb" {
		t.Fatalf("blocks: got %q", got)
	}

	if got := lastAssistantText(""); got != "" {
		t.Fatalf("empty path: got %q", got)
	}
	if got := lastAssistantText(filepath.Join(t.TempDir(), "missing.jsonl")); got != "" {
		t.Fatalf("missing file: got %q", got)
	}
}

// stubNtfy points the ntfy base URL at the test server.
func stubNtfy(t *testing.T, srv *httptest.Server) {
	t.Helper()
	orig := ntfyBaseURL
	ntfyBaseURL = srv.URL
	t.Cleanup(func() { ntfyBaseURL = orig })
}

func TestRunHookNtfyPush(t *testing.T) {
	var ntfyPath, ntfyTitle, ntfyClick, ntfyBody string
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ntfyPath = r.URL.Path
		ntfyTitle = r.Header.Get("Title")
		ntfyClick = r.Header.Get("Click")
		b, _ := io.ReadAll(r.Body)
		ntfyBody = string(b)
	}))
	defer ntfy.Close()
	stubNtfy(t, ntfy)

	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"mirror":"http://mirror.example/?ws=file:///x%2Fy"}`))
	}))
	defer bridge.Close()

	transcript := writeTranscript(t, `{"type":"assistant.message","data":{"content":"done with the task"}}`)
	payload, _ := json.Marshal(map[string]string{
		"hook_event_name": "Stop",
		"session_id":      "01c86818-0239-4f9d-8a9c-375f7590bd28",
		"transcript_path": transcript,
	})
	if err := runHook(bridge.URL, "my-topic", strings.NewReader(string(payload))); err != nil {
		t.Fatal(err)
	}
	if ntfyPath != "/my-topic" {
		t.Errorf("ntfy path = %q, want /my-topic", ntfyPath)
	}
	if ntfyTitle != "VS Code Copilot: task finished [01c86818]" {
		t.Errorf("ntfy title = %q", ntfyTitle)
	}
	if ntfyBody != "done with the task" {
		t.Errorf("ntfy body = %q", ntfyBody)
	}
	if ntfyClick != "http://mirror.example/?ws=file:///x%2Fy" {
		t.Errorf("ntfy click = %q", ntfyClick)
	}
}

func TestRunHookNoNtfyWithoutTopic(t *testing.T) {
	n := 0
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n++ }))
	defer ntfy.Close()
	stubNtfy(t, ntfy)

	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer bridge.Close()

	if err := runHook(bridge.URL, "", strings.NewReader(`{"hook_event_name":"Stop"}`)); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("ntfy requests = %d, want 0 without a topic", n)
	}
}

func TestRunHookNoNtfyOnSessionStart(t *testing.T) {
	n := 0
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n++ }))
	defer ntfy.Close()
	stubNtfy(t, ntfy)

	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer bridge.Close()

	if err := runHook(bridge.URL, "my-topic", strings.NewReader(`{"hook_event_name":"SessionStart"}`)); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("ntfy requests = %d, want 0 for a SessionStart event", n)
	}
}

func TestRunHookQuestionNtfy(t *testing.T) {
	var ntfyPath, ntfyTitle, ntfyClick, ntfyBody string
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ntfyPath = r.URL.Path
		ntfyTitle = r.Header.Get("Title")
		ntfyClick = r.Header.Get("Click")
		b, _ := io.ReadAll(r.Body)
		ntfyBody = string(b)
	}))
	defer ntfy.Close()
	stubNtfy(t, ntfy)

	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"mirror":"http://mirror.example/?ws=file:///x%2Fy"}`))
	}))
	defer bridge.Close()

	// The payload shape probed from a live PreToolUse hook stdin dump.
	payload, _ := json.Marshal(map[string]any{
		"hook_event_name": "PreToolUse",
		"session_id":      "01c86818-0239-4f9d-8a9c-375f7590bd28",
		"tool_name":       "vscode_askQuestions",
		"tool_input": map[string]any{
			"questions": []map[string]any{
				{
					"header":   "Scope",
					"question": "Which files should the fix cover?",
					"options":  []map[string]string{{"label": "all"}, {"label": "core only"}},
				},
				{
					"header":   "Style",
					"question": "Keep the existing style?",
					"options":  []map[string]string{{"label": "yes"}},
				},
			},
		},
	})
	if err := runHook(bridge.URL, "my-topic", strings.NewReader(string(payload))); err != nil {
		t.Fatal(err)
	}
	if ntfyPath != "/my-topic" {
		t.Errorf("ntfy path = %q, want /my-topic", ntfyPath)
	}
	if ntfyTitle != "VS Code Copilot: question [01c86818]" {
		t.Errorf("ntfy title = %q", ntfyTitle)
	}
	if ntfyClick != "http://mirror.example/?ws=file:///x%2Fy" {
		t.Errorf("ntfy click = %q", ntfyClick)
	}
	want := "1. Which files should the fix cover?\n   - all\n   - core only\n\n2. Keep the existing style?\n   - yes"
	if ntfyBody != want {
		t.Errorf("ntfy body = %q, want %q", ntfyBody, want)
	}
}

func TestRunHookQuestionNtfyBridgeDown(t *testing.T) {
	var ntfyClick, ntfyBody string
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ntfyClick = r.Header.Get("Click")
		b, _ := io.ReadAll(r.Body)
		ntfyBody = string(b)
	}))
	defer ntfy.Close()
	stubNtfy(t, ntfy)

	if err := runHook("http://127.0.0.1:1", "my-topic", strings.NewReader(
		`{"hook_event_name":"PreToolUse","tool_name":"vscode_askQuestions","tool_input":{"questions":[{"question":"q?"}]}}`)); err == nil {
		t.Fatal("expected an error when the bridge is unreachable")
	}
	if ntfyClick != "" {
		t.Errorf("ntfy click = %q, want empty without a bridge", ntfyClick)
	}
	if ntfyBody != "1. q?" {
		t.Errorf("ntfy body = %q", ntfyBody)
	}
}

func TestRunHookPreToolUseOtherToolNoNtfy(t *testing.T) {
	n := 0
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n++ }))
	defer ntfy.Close()
	stubNtfy(t, ntfy)

	if err := runHook(okBridge(t).URL, "my-topic", strings.NewReader(
		`{"hook_event_name":"PreToolUse","tool_name":"run_in_terminal","tool_input":{"command":"x"}}`)); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("ntfy requests = %d, want 0 for a non-question tool", n)
	}
}

func TestRunHookQuestionNoNtfyWithoutTopic(t *testing.T) {
	n := 0
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n++ }))
	defer ntfy.Close()
	stubNtfy(t, ntfy)

	if err := runHook(okBridge(t).URL, "", strings.NewReader(
		`{"hook_event_name":"PreToolUse","tool_name":"vscode_askQuestions","tool_input":{"questions":[{"question":"q?"}]}}`)); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("ntfy requests = %d, want 0 without a topic", n)
	}
}

func TestFormatQuestions(t *testing.T) {
	// Normal shape: two questions, the second without options.
	in := jsontext.Value(`{"questions":[{"question":"a?","options":[{"label":"x"},{"label":"y"}]},{"question":"b?"}]}`)
	if got := formatQuestions(in); got != "1. a?\n   - x\n   - y\n\n2. b?" {
		t.Fatalf("got %q", got)
	}
	// Unparseable, empty, or question-less input.
	if got := formatQuestions(jsontext.Value(`nope`)); got != "" {
		t.Fatalf("bad json: got %q", got)
	}
	if got := formatQuestions(jsontext.Value("")); got != "" {
		t.Fatalf("empty: got %q", got)
	}
	if got := formatQuestions(jsontext.Value(`{}`)); got != "" {
		t.Fatalf("no questions: got %q", got)
	}
}

func TestRunHookNtfyBridgeDown(t *testing.T) {
	var ntfyClick, ntfyBody string
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ntfyClick = r.Header.Get("Click")
		b, _ := io.ReadAll(r.Body)
		ntfyBody = string(b)
	}))
	defer ntfy.Close()
	stubNtfy(t, ntfy)

	if err := runHook("http://127.0.0.1:1", "my-topic", strings.NewReader(`{"hook_event_name":"Stop"}`)); err == nil {
		t.Fatal("expected an error when the bridge is unreachable")
	}
	if ntfyClick != "" {
		t.Errorf("ntfy click = %q, want empty without a bridge", ntfyClick)
	}
	if ntfyBody != "Agent task finished (no last message captured)" {
		t.Errorf("ntfy body = %q", ntfyBody)
	}
}

func TestRunHookNtfyTruncatesLongText(t *testing.T) {
	var ntfyBody string
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ntfyBody = string(b)
	}))
	defer ntfy.Close()
	stubNtfy(t, ntfy)

	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer bridge.Close()

	long := strings.Repeat("x", ntfyMaxLen+100)
	transcript := writeTranscript(t, `{"type":"assistant.message","data":{"content":"`+long+`"}}`)
	payload, _ := json.Marshal(map[string]string{
		"hook_event_name": "Stop",
		"transcript_path": transcript,
	})
	if err := runHook(bridge.URL, "my-topic", strings.NewReader(string(payload))); err != nil {
		t.Fatal(err)
	}
	want := strings.Repeat("x", ntfyMaxLen) + " ...[truncated]"
	if ntfyBody != want {
		t.Errorf("ntfy body len = %d, want %d", len(ntfyBody), len(want))
	}
}

// stubNtfyDelay removes the retry delay for tests.
func stubNtfyDelay(t *testing.T) {
	t.Helper()
	orig := ntfyRetryDelay
	ntfyRetryDelay = 0
	t.Cleanup(func() { ntfyRetryDelay = orig })
}

// stubHookLog redirects hook error logging to a temp file.
func stubHookLog(t *testing.T) {
	t.Helper()
	orig := hookLogPath
	hookLogPath = filepath.Join(t.TempDir(), "app.log")
	t.Cleanup(func() { hookLogPath = orig })
}

// okBridge is a minimal bridge stub answering 200.
func okBridge(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRunHookNtfyRetriesTransportError(t *testing.T) {
	stubNtfyDelay(t)
	var n int
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			// Drop the connection without a response (the transient EOF
			// observed against the real ntfy.sh).
			hc, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				hc.Close()
			}
			return
		}
	}))
	defer ntfy.Close()
	stubNtfy(t, ntfy)

	if err := runHook(okBridge(t).URL, "my-topic", strings.NewReader(`{"hook_event_name":"Stop"}`)); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("ntfy attempts = %d, want 2 (one retry)", n)
	}
}

func TestRunHookNtfyRetries5xx(t *testing.T) {
	stubNtfyDelay(t)
	var n int
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
	}))
	defer ntfy.Close()
	stubNtfy(t, ntfy)

	if err := runHook(okBridge(t).URL, "my-topic", strings.NewReader(`{"hook_event_name":"Stop"}`)); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("ntfy attempts = %d, want 2 (5xx is retried)", n)
	}
}

func TestRunHookNtfyNoRetryOn4xx(t *testing.T) {
	stubNtfyDelay(t)
	stubHookLog(t)
	var n int
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ntfy.Close()
	stubNtfy(t, ntfy)

	if err := runHook(okBridge(t).URL, "my-topic", strings.NewReader(`{"hook_event_name":"Stop"}`)); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("ntfy attempts = %d, want 1 (4xx is not retried)", n)
	}
}
