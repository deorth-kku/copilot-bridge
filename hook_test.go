package main

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
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
	if err := runHook(t.Context(), hookParams{Bridge: srv.URL, Stdin: strings.NewReader(payload)}); err != nil {
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
	if err := runHook(t.Context(), hookParams{Bridge: "http://127.0.0.1:1", Stdin: strings.NewReader(`{"hook_event_name":"Stop"}`)}); err == nil {
		t.Fatal("expected an error when the bridge is unreachable")
	}
}

func TestRunHookNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	if err := runHook(t.Context(), hookParams{Bridge: srv.URL, Stdin: strings.NewReader(`{}`)}); err == nil {
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

// stubHookStdout captures the hook's stdout JSON in a buffer.
func stubHookStdout(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	orig := hookStdout
	hookStdout = buf
	t.Cleanup(func() { hookStdout = orig })
	return buf
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
	if err := runHook(t.Context(), hookParams{Bridge: bridge.URL, NtfyTopic: "my-topic", Stdin: strings.NewReader(string(payload))}); err != nil {
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

	if err := runHook(t.Context(), hookParams{Bridge: bridge.URL, Stdin: strings.NewReader(`{"hook_event_name":"Stop"}`)}); err != nil {
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

	if err := runHook(t.Context(), hookParams{Bridge: bridge.URL, NtfyTopic: "my-topic", Stdin: strings.NewReader(`{"hook_event_name":"SessionStart"}`)}); err != nil {
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
	if err := runHook(t.Context(), hookParams{Bridge: bridge.URL, NtfyTopic: "my-topic", Stdin: strings.NewReader(string(payload))}); err != nil {
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

	if err := runHook(t.Context(), hookParams{Bridge: "http://127.0.0.1:1", NtfyTopic: "my-topic", Stdin: strings.NewReader(
		`{"hook_event_name":"PreToolUse","tool_name":"vscode_askQuestions","tool_input":{"questions":[{"question":"q?"}]}}`)}); err == nil {
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

	if err := runHook(t.Context(), hookParams{Bridge: okBridge(t).URL, NtfyTopic: "my-topic", Stdin: strings.NewReader(
		`{"hook_event_name":"PreToolUse","tool_name":"run_in_terminal","tool_input":{"command":"x"}}`)}); err != nil {
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

	if err := runHook(t.Context(), hookParams{Bridge: okBridge(t).URL, Stdin: strings.NewReader(
		`{"hook_event_name":"PreToolUse","tool_name":"vscode_askQuestions","tool_input":{"questions":[{"question":"q?"}]}}`)}); err != nil {
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

	if err := runHook(t.Context(), hookParams{Bridge: "http://127.0.0.1:1", NtfyTopic: "my-topic", Stdin: strings.NewReader(`{"hook_event_name":"Stop"}`)}); err == nil {
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
	if err := runHook(t.Context(), hookParams{Bridge: bridge.URL, NtfyTopic: "my-topic", Stdin: strings.NewReader(string(payload))}); err != nil {
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

	if err := runHook(t.Context(), hookParams{Bridge: okBridge(t).URL, NtfyTopic: "my-topic", Stdin: strings.NewReader(`{"hook_event_name":"Stop"}`)}); err != nil {
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

	if err := runHook(t.Context(), hookParams{Bridge: okBridge(t).URL, NtfyTopic: "my-topic", Stdin: strings.NewReader(`{"hook_event_name":"Stop"}`)}); err != nil {
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

	if err := runHook(t.Context(), hookParams{Bridge: okBridge(t).URL, NtfyTopic: "my-topic", Stdin: strings.NewReader(`{"hook_event_name":"Stop"}`)}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("ntfy attempts = %d, want 1 (4xx is not retried)", n)
	}
}

func TestRunHookPostToolUseTaskCompleteStops(t *testing.T) {
	out := stubHookStdout(t)
	if err := runHook(t.Context(), hookParams{Bridge: okBridge(t).URL, Stdin: strings.NewReader(
		`{"hook_event_name":"PostToolUse","tool_name":"task_complete","tool_input":{"summary":"done"}}`)}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Continue   bool   `json:"continue"`
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stdout %q is not JSON: %v", out.String(), err)
	}
	if got.Continue || got.StopReason == "" {
		t.Fatalf("stdout = %q, want continue false with a stopReason", out.String())
	}
}

func TestRunHookPostToolUseOtherToolNoBlock(t *testing.T) {
	out := stubHookStdout(t)
	if err := runHook(t.Context(), hookParams{Bridge: okBridge(t).URL, Stdin: strings.NewReader(
		`{"hook_event_name":"PostToolUse","tool_name":"run_in_terminal","tool_input":{"command":"x"}}`)}); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want empty for a non-task_complete tool", out.String())
	}
}

func TestRunHookTaskCompleteNtfy(t *testing.T) {
	var ntfyTitle, ntfyClick, ntfyBody string
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	if err := runHook(t.Context(), hookParams{Bridge: bridge.URL, NtfyTopic: "my-topic", Stdin: strings.NewReader(
		`{"hook_event_name":"PostToolUse","session_id":"01c86818-0239-4f9d-8a9c-375f7590bd28","tool_name":"task_complete","tool_input":{"summary":"Fixed the bug"}}`)}); err != nil {
		t.Fatal(err)
	}
	if ntfyTitle != "VS Code Copilot: task complete [01c86818]" {
		t.Errorf("ntfy title = %q", ntfyTitle)
	}
	if ntfyClick != "http://mirror.example/?ws=file:///x%2Fy" {
		t.Errorf("ntfy click = %q", ntfyClick)
	}
	if ntfyBody != "Fixed the bug" {
		t.Errorf("ntfy body = %q", ntfyBody)
	}
}

func TestRunHookTaskCompleteNtfyNoSummary(t *testing.T) {
	var ntfyBody string
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ntfyBody = string(b)
	}))
	defer ntfy.Close()
	stubNtfy(t, ntfy)

	if err := runHook(t.Context(), hookParams{Bridge: okBridge(t).URL, NtfyTopic: "my-topic", Stdin: strings.NewReader(
		`{"hook_event_name":"PostToolUse","tool_name":"task_complete","tool_input":{}}`)}); err != nil {
		t.Fatal(err)
	}
	if ntfyBody != "Agent task complete (no summary captured)" {
		t.Errorf("ntfy body = %q", ntfyBody)
	}
}

func TestRunHookTaskCompleteNoNtfyWithoutTopic(t *testing.T) {
	n := 0
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n++ }))
	defer ntfy.Close()
	stubNtfy(t, ntfy)

	out := stubHookStdout(t)
	if err := runHook(t.Context(), hookParams{Bridge: okBridge(t).URL, Stdin: strings.NewReader(
		`{"hook_event_name":"PostToolUse","tool_name":"task_complete","tool_input":{"summary":"done"}}`)}); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("ntfy requests = %d, want 0 without a topic", n)
	}
	if out.Len() == 0 {
		t.Fatal("expected the stop output even without a topic")
	}
}

func TestTaskCompleteSummary(t *testing.T) {
	if got := taskCompleteSummary(jsontext.Value(`{"summary":"done"}`)); got != "done" {
		t.Fatalf("got %q", got)
	}
	if got := taskCompleteSummary(jsontext.Value(`{}`)); got != "" {
		t.Fatalf("no summary: got %q", got)
	}
	if got := taskCompleteSummary(jsontext.Value(`nope`)); got != "" {
		t.Fatalf("bad json: got %q", got)
	}
	if got := taskCompleteSummary(jsontext.Value("")); got != "" {
		t.Fatalf("empty: got %q", got)
	}
}

// systemOneStub is a /v1/systemone stub answering the noul question with the
// given probability; gotAuth (if non-nil) captures the Authorization header.
func systemOneStub(t *testing.T, noul float64, gotAuth *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gotAuth != nil {
			*gotAuth = r.Header.Get("Authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"model":"m","answers":{"safe":{"type":"noul","noul":%g}}}`, noul)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// approveCfg is a fully-configured approveConfig with one model rule
// (run_* -> server "srv" at the given URL).
func approveCfg(url string) approveConfig {
	return approveConfig{
		Servers: map[string]approveServer{
			"srv": {Model: "decision-model", URL: url, Key: "test-key"},
		},
		Rules: []approveRule{
			{Match: "run_*", Server: "srv", Thresholds: [2]float64{0.1, 0.5}},
		},
	}
}

func TestToolMatchesOne(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		want    bool
	}{
		{"run_in_terminal", "run_*", true},
		{"edit", "edit", true},
		{"domprobe1", "*probe*", true},
		{"view", "run_*", false},
		{"RUN_in_terminal", "run_*", false}, // case-sensitive
		{"view", "[", false},                // invalid pattern matches nothing
	}
	for _, c := range cases {
		if got := toolMatchesOne(c.name, c.pattern); got != c.want {
			t.Errorf("toolMatchesOne(%q, %q) = %v, want %v", c.name, c.pattern, got, c.want)
		}
	}
}

func TestValidThresholds(t *testing.T) {
	if !validThresholds([2]float64{0.1, 0.5}) {
		t.Error("0.1,0.5 should be valid")
	}
	for _, bad := range [][2]float64{{0.5, 0.1}, {-1, 0.5}, {0.1, 2}, {0.5, 0.5}} {
		if validThresholds(bad) {
			t.Errorf("validThresholds(%v) should be false", bad)
		}
	}
}

func TestNoulDecision(t *testing.T) {
	cases := []struct {
		v    float64
		want string
	}{
		{0, "deny"},
		{0.09, "deny"},
		{0.1, "ask"},
		{0.49, "ask"},
		{0.5, "allow"},
		{1, "allow"},
	}
	for _, c := range cases {
		if got := noulDecision(c.v, 0.1, 0.5); got != c.want {
			t.Errorf("noulDecision(%v) = %q, want %q", c.v, got, c.want)
		}
	}
}

func TestRunHookApproveAllow(t *testing.T) {
	out := stubHookStdout(t)
	srv := systemOneStub(t, 0.9, nil)
	if err := runHook(t.Context(), hookParams{Bridge: okBridge(t).URL, Stdin: strings.NewReader(
		`{"hook_event_name":"PreToolUse","tool_name":"run_in_terminal","tool_input":{"command":"ls"}}`), Approve: approveCfg(srv.URL)}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Decision string `json:"permissionDecision"`
		Reason   string `json:"permissionDecisionReason"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stdout %q not JSON: %v", out.String(), err)
	}
	if got.Decision != "allow" || got.Reason == "" {
		t.Fatalf("stdout = %q, want allow with a reason", out.String())
	}
}

func TestRunHookApproveDeny(t *testing.T) {
	out := stubHookStdout(t)
	srv := systemOneStub(t, 0.05, nil)
	if err := runHook(t.Context(), hookParams{Bridge: okBridge(t).URL, Stdin: strings.NewReader(
		`{"hook_event_name":"PreToolUse","tool_name":"run_in_terminal","tool_input":{"command":"rm -rf /"}}`), Approve: approveCfg(srv.URL)}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Decision string `json:"permissionDecision"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stdout %q not JSON: %v", out.String(), err)
	}
	if got.Decision != "deny" {
		t.Fatalf("stdout = %q, want deny", out.String())
	}
}

func TestRunHookApproveAskNtfy(t *testing.T) {
	stubNtfyDelay(t)
	var ntfyTitle, ntfyClick, ntfyBody string
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	srv := systemOneStub(t, 0.3, nil)
	out := stubHookStdout(t)
	if err := runHook(t.Context(), hookParams{Bridge: bridge.URL, NtfyTopic: "my-topic", Stdin: strings.NewReader(
		`{"hook_event_name":"PreToolUse","session_id":"01c86818-0239-4f9d-8a9c-375f7590bd28","tool_name":"run_in_terminal","tool_input":{"command":"x"}}`), Approve: approveCfg(srv.URL)}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Decision string `json:"permissionDecision"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stdout %q not JSON: %v", out.String(), err)
	}
	if got.Decision != "ask" {
		t.Fatalf("stdout = %q, want ask", out.String())
	}
	if ntfyTitle != "VS Code Copilot: tool approval needed [01c86818]" {
		t.Errorf("ntfy title = %q", ntfyTitle)
	}
	if ntfyClick != "http://mirror.example/?ws=file:///x%2Fy" {
		t.Errorf("ntfy click = %q", ntfyClick)
	}
	if !strings.HasPrefix(ntfyBody, "Tool run_in_terminal needs approval") {
		t.Errorf("ntfy body = %q", ntfyBody)
	}
}

func TestRunHookApproveNoMatchNoOutput(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n++ }))
	defer srv.Close()
	out := stubHookStdout(t)
	if err := runHook(t.Context(), hookParams{Bridge: okBridge(t).URL, Stdin: strings.NewReader(
		`{"hook_event_name":"PreToolUse","tool_name":"view","tool_input":{"path":"/x"}}`), Approve: approveCfg(srv.URL)}); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("systemone requests = %d, want 0 for a non-matching tool", n)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want empty for a non-matching tool", out.String())
	}
}

func TestRunHookApproveModelDownFailOpen(t *testing.T) {
	out := stubHookStdout(t)
	cfg := approveCfg("http://127.0.0.1:1") // nothing listening
	if err := runHook(t.Context(), hookParams{Bridge: okBridge(t).URL, Stdin: strings.NewReader(
		`{"hook_event_name":"PreToolUse","tool_name":"run_in_terminal","tool_input":{"command":"x"}}`), Approve: cfg}); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want empty (fail-open) when the model is unreachable", out.String())
	}
}

func TestRunHookApproveSendsKey(t *testing.T) {
	var gotAuth string
	srv := systemOneStub(t, 0.9, &gotAuth)
	if err := runHook(t.Context(), hookParams{Bridge: okBridge(t).URL, Stdin: strings.NewReader(
		`{"hook_event_name":"PreToolUse","tool_name":"run_in_terminal","tool_input":{"command":"x"}}`), Approve: approveCfg(srv.URL)}); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("Authorization = %q, want Bearer test-key", gotAuth)
	}
}

func TestRunHookApproveDisabledNoCall(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n++ }))
	defer srv.Close()
	out := stubHookStdout(t)
	// Zero-value config: feature disabled, no model call, no stdout.
	if err := runHook(t.Context(), hookParams{Bridge: okBridge(t).URL, Stdin: strings.NewReader(
		`{"hook_event_name":"PreToolUse","tool_name":"run_in_terminal","tool_input":{"command":"x"}}`)}); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("systemone requests = %d, want 0 when disabled", n)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want empty when disabled", out.String())
	}
}

func TestLoadApproveConfig(t *testing.T) {
	good := `{
		"servers": {"s0": {"model":"m","url":"http://x/v1/systemone","timeout":"5s"}},
		"rules": [
			{"match":"vscode_*","policy":"allow"},
			{"match":"mcp_*","server":"s0","thresholds":[0.1,0.5]}
		]
	}`
	dir := t.TempDir()
	p := filepath.Join(dir, "approve.json")
	if err := os.WriteFile(p, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, ok := loadApproveConfig(p)
	if !ok {
		t.Fatal("expected a valid config")
	}
	if len(cfg.Rules) != 2 || cfg.Rules[1].Server != "s0" || cfg.Rules[1].Thresholds[0] != 0.1 {
		t.Fatalf("parsed config = %+v", cfg)
	}

	// Empty path = disabled.
	if _, ok := loadApproveConfig(""); ok {
		t.Error("empty path should be disabled")
	}
	// Missing file.
	if _, ok := loadApproveConfig(filepath.Join(dir, "nope.json")); ok {
		t.Error("missing file should be disabled")
	}
	// Each of these must be rejected.
	bad := func(body string) {
		t.Helper()
		bp := filepath.Join(dir, "bad.json")
		if err := os.WriteFile(bp, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := loadApproveConfig(bp); ok {
			t.Errorf("config %s should be invalid", body)
		}
	}
	bad(`{`)
	bad(`{"rules":[{"match":"a","policy":"allow","server":"s0"}]}`)         // both policy and server
	bad(`{"rules":[{"match":"a","server":"s0"}]}`)                          // server not defined
	bad(`{"rules":[{"match":"a","server":"nope","thresholds":[0.1,0.5]}]}`) // unknown server
	bad(`{"rules":[{"match":"a","policy":"maybe"}]}`)                       // bad policy
	bad(`{"rules":[{"match":"a","server":"s0","thresholds":[0.5,0.1]}]}`)   // bad thresholds
	bad(`{"rules":[{"server":"s0","thresholds":[0.1,0.5]}]}`)               // no match
}

func TestRunHookApproveLocalAllow(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n++ }))
	defer srv.Close()
	out := stubHookStdout(t)
	cfg := approveConfig{
		Servers: map[string]approveServer{"srv": {Model: "m", URL: srv.URL}},
		Rules:   []approveRule{{Match: "run_*", Policy: "allow"}},
	}
	if err := runHook(t.Context(), hookParams{Bridge: okBridge(t).URL, Stdin: strings.NewReader(
		`{"hook_event_name":"PreToolUse","tool_name":"run_in_terminal","tool_input":{"command":"ls"}}`), Approve: cfg}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Decision string `json:"permissionDecision"`
		Reason   string `json:"permissionDecisionReason"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stdout %q not JSON: %v", out.String(), err)
	}
	if got.Decision != "allow" || got.Reason != "local rule: allow" {
		t.Fatalf("stdout = %q, want allow / local rule: allow", out.String())
	}
	if n != 0 {
		t.Fatalf("systemone requests = %d, want 0 for a local rule", n)
	}
}

func TestRunHookApproveLocalAskNtfy(t *testing.T) {
	stubNtfyDelay(t)
	var ntfyTitle, ntfyBody string
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ntfyTitle = r.Header.Get("Title")
		b, _ := io.ReadAll(r.Body)
		ntfyBody = string(b)
	}))
	defer ntfy.Close()
	stubNtfy(t, ntfy)

	out := stubHookStdout(t)
	cfg := approveConfig{Rules: []approveRule{{Match: "run_*", Policy: "ask"}}}
	if err := runHook(t.Context(), hookParams{Bridge: okBridge(t).URL, NtfyTopic: "my-topic", Stdin: strings.NewReader(
		`{"hook_event_name":"PreToolUse","session_id":"01c86818-0239-4f9d-8a9c-375f7590bd28","tool_name":"run_in_terminal","tool_input":{"command":"x"}}`), Approve: cfg}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Decision string `json:"permissionDecision"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stdout %q not JSON: %v", out.String(), err)
	}
	if got.Decision != "ask" {
		t.Fatalf("stdout = %q, want ask", out.String())
	}
	if ntfyTitle != "VS Code Copilot: tool approval needed [01c86818]" {
		t.Errorf("ntfy title = %q", ntfyTitle)
	}
	if !strings.HasPrefix(ntfyBody, "Tool run_in_terminal needs approval") {
		t.Errorf("ntfy body = %q", ntfyBody)
	}
}

func TestRunHookApproveFirstRuleWins(t *testing.T) {
	out := stubHookStdout(t)
	// Both rules match run_in_terminal; the first (local allow) must win
	// and the model rule must not be consulted (its server is unreachable).
	cfg := approveConfig{
		Servers: map[string]approveServer{"srv": {Model: "m", URL: "http://127.0.0.1:1"}},
		Rules: []approveRule{
			{Match: "run_*", Policy: "allow"},
			{Match: "run_*", Server: "srv", Thresholds: [2]float64{0.1, 0.5}},
		},
	}
	if err := runHook(t.Context(), hookParams{Bridge: okBridge(t).URL, Stdin: strings.NewReader(
		`{"hook_event_name":"PreToolUse","tool_name":"run_in_terminal","tool_input":{"command":"x"}}`), Approve: cfg}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Decision string `json:"permissionDecision"`
		Reason   string `json:"permissionDecisionReason"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stdout %q not JSON: %v", out.String(), err)
	}
	if got.Decision != "allow" || got.Reason != "local rule: allow" {
		t.Fatalf("stdout = %q, want the first rule (local allow) to win", out.String())
	}
}

// TestCallSystemOne drives callSystemOne against a mock /v1/systemone server
// and checks the request it sends plus the success and fail-open responses.
func TestCallSystemOne(t *testing.T) {
	stubHookLog(t)

	var gotAuth, gotCT, gotModel, gotState string
	var respStatus int
	var respBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		var req struct {
			Model     string `json:"model"`
			State     string `json:"state"`
			Questions struct {
				Safe struct {
					Type         string `json:"type"`
					Instructions string `json:"instructions"`
				} `json:"safe"`
			} `json:"questions"`
		}
		_ = json.Unmarshal(b, &req)
		gotModel = req.Model
		gotState = req.State
		w.WriteHeader(respStatus)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	s := approveServer{Model: "dm", URL: srv.URL, Key: "k1"}
	input := jsontext.Value(`{"command":"ls"}`)

	// 1. Normal: noul present -> value returned.
	respStatus = 200
	respBody = `{"answers":{"safe":{"noul":0.7}}}`
	v, ok := s.callSystemOne(t.Context(), "run_in_terminal", input)
	if !ok || v != 0.7 {
		t.Fatalf("got %v ok=%v, want 0.7 true", v, ok)
	}
	if gotModel != "dm" {
		t.Errorf("model = %q, want dm", gotModel)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q, want application/json", gotCT)
	}
	if gotAuth != "Bearer k1" {
		t.Errorf("auth = %q, want Bearer k1", gotAuth)
	}
	// state must carry the tool name and its input.
	if !strings.Contains(gotState, `"run_in_terminal"`) || !strings.Contains(gotState, `"command":"ls"`) {
		t.Errorf("state = %q, want tool_name + tool_input", gotState)
	}

	// 2. Missing noul -> fail-open (NaN sentinel).
	respBody = `{"answers":{"safe":{}}}`
	if v, ok := s.callSystemOne(t.Context(), "run_in_terminal", input); ok {
		t.Fatalf("missing noul: got %v ok=true, want fail-open", v)
	}

	// 3. 5xx -> fail-open.
	respStatus = 500
	respBody = `{"answers":{"safe":{"noul":0.9}}}`
	if _, ok := s.callSystemOne(t.Context(), "run_in_terminal", input); ok {
		t.Fatal("5xx: want fail-open")
	}

	// 4. Malformed JSON -> fail-open.
	respStatus = 200
	respBody = `not json`
	if _, ok := s.callSystemOne(t.Context(), "run_in_terminal", input); ok {
		t.Fatal("bad json: want fail-open")
	}

	// 5. No key -> no Authorization header.
	respBody = `{"answers":{"safe":{"noul":0.2}}}`
	s2 := approveServer{Model: "dm", URL: srv.URL}
	v, ok = s2.callSystemOne(t.Context(), "run_in_terminal", input)
	if !ok || v != 0.2 {
		t.Fatalf("no-key: got %v ok=%v, want 0.2 true", v, ok)
	}
	if gotAuth != "" {
		t.Errorf("no-key: auth = %q, want empty", gotAuth)
	}
}
