package main

// The "hook" subcommand: invoked by VS Code agent hooks (SessionStart /
// Stop). VS Code writes the hook's JSON payload to the process's stdin;
// we forward it verbatim to the bridge's /api/hook endpoint, where the
// shutdown planner decides what to do with the event.
//
// When -ntfy-topic is set, two events additionally push an ntfy.sh
// notification, both carrying the bridge's mirror URL as the
// notification's Click target (tapping the phone notification opens the
// mirror of the workspace the task ran in):
//   - Stop: the agent's last message (parsed from the session transcript,
//     best effort) as the body.
//   - PreToolUse of the ask-questions tool (tool_name
//     "vscode_askQuestions"): the questions and their options as the body,
//     so a phone notification arrives while the agent waits for an answer.
//
// The hook only notifies: it never writes to stdout (no
// hookSpecificOutput) and never influences the tool call.
//
// The subcommand ALWAYS exits with status 0: VS Code treats exit code 2
// as a blocking error (shown to the model) and other non-zero codes as
// warnings, and a missing bridge must never disrupt the agent. Failures
// are reported on stderr and appended to the app log.

import (
	"bufio"
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// hookMaxBody caps the payload read from stdin (hook payloads are small
// JSON objects; the cap is a guard against a wedged pipe).
const hookMaxBody = 1 << 20

// hookTimeout bounds the round trip to the bridge. VS Code's default hook
// timeout is 30s; we stay far inside it.
const hookTimeout = 5 * time.Second

// ntfyBaseURL is the ntfy server base (a variable so tests can point it
// at a local test server).
var ntfyBaseURL = "https://ntfy.sh"

// ntfyMaxLen caps the notification body (keep the phone notification
// readable).
const ntfyMaxLen = 1500

// ntfyTimeout bounds a single ntfy push attempt.
const ntfyTimeout = 6 * time.Second

// ntfyMaxAttempts is the number of push attempts (one retry covers
// transient proxy/CDN hiccups, e.g. an EOF mid-request). Worst-case hook
// budget: hookTimeout + ntfyMaxAttempts*ntfyTimeout + ntfyRetryDelay =
// 5s + 12s + 1s = 18s, inside the 20s hook timeout in the install doc.
const ntfyMaxAttempts = 2

// ntfyRetryDelay separates retry attempts (a var so tests can skip the
// wait).
var ntfyRetryDelay = time.Second

// askQuestionsToolName is the tool_name VS Code puts in the PreToolUse
// payload for the ask-user-questions tool (probed 2026-09-22 from a live
// hook stdin dump).
const askQuestionsToolName = "vscode_askQuestions"

// runHookCommand is the entry point for the "hook" subcommand. It always
// exits with status 0 (see the package comment).
func runHookCommand(args []string) {
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	bridge := fs.String("bridge", "http://127.0.0.1:9527", "bridge HTTP address (the mirror web server)")
	ntfyTopic := fs.String("ntfy-topic", "", "ntfy.sh topic for the task-finished notification (empty = no notification)")
	if err := fs.Parse(args); err != nil {
		os.Exit(0)
	}
	if err := runHook(*bridge, *ntfyTopic, os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, "hook:", err)
		logHookError(err)
	}
	os.Exit(0)
}

// runHook reads the hook payload from stdin, forwards it to the bridge's
// /api/hook endpoint, and (when a topic is configured) pushes an ntfy
// notification for a Stop event (the agent's last message) or a PreToolUse
// event of the ask-questions tool (the questions themselves), each
// carrying the bridge's mirror URL as the Click target. The ntfy push is
// independent of the bridge: a missing bridge still notifies (without the
// mirror Click link).
func runHook(bridge, ntfyTopic string, stdin io.Reader) error {
	body, err := io.ReadAll(io.LimitReader(stdin, hookMaxBody))
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	var ev struct {
		Name           string         `json:"hook_event_name"`
		SessionID      string         `json:"session_id"`
		TranscriptPath string         `json:"transcript_path"`
		ToolName       string         `json:"tool_name"`
		ToolInput      jsontext.Value `json:"tool_input"`
	}
	// Not fatal: the payload is forwarded verbatim regardless; the fields
	// are only needed for the ntfy notifications.
	_ = json.Unmarshal(body, &ev)

	mirrorURL, bridgeErr := bridgeHook(bridge, body)

	if ntfyTopic != "" {
		switch ev.Name {
		case "Stop":
			// Best effort: the last assistant message of the session
			// transcript (VS Code's transcript format is not a stable API).
			pushNtfy(ntfyTopic, ev.SessionID, lastAssistantText(ev.TranscriptPath), mirrorURL)
		case "PreToolUse":
			// PreToolUse fires for EVERY tool; only the ask-questions tool
			// gets a notification.
			if ev.ToolName == askQuestionsToolName {
				pushQuestionNtfy(ntfyTopic, ev.SessionID, ev.ToolInput, mirrorURL)
			}
		}
	}
	return bridgeErr
}

// bridgeHook forwards the payload to the bridge's /api/hook endpoint and
// returns the mirror URL from the response ("" when the bridge is
// unreachable or the response does not carry one).
func bridgeHook(bridge string, body []byte) (string, error) {
	client := &http.Client{Timeout: hookTimeout}
	resp, err := client.Post(bridge+"/api/hook", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("post to %s/api/hook: %w", bridge, err)
	}
	defer resp.Body.Close()
	rb, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if rerr != nil {
		return "", fmt.Errorf("read bridge response: %w", rerr)
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("bridge returned %s", resp.Status)
	}
	var out struct {
		Mirror string `json:"mirror"`
	}
	if jerr := json.Unmarshal(rb, &out); jerr != nil {
		// 2xx but unexpected shape: the event was accepted, there is just
		// no mirror URL to report.
		return "", nil
	}
	return out.Mirror, nil
}

// pushNtfy pushes one ntfy notification for a finished agent task. Best
// effort: failures are reported on stderr and in the app log and never
// affect the exit status. The Click header makes the phone notification
// open the mirror URL when tapped. Transient failures (transport errors,
// 5xx) are retried once — a missed task-finished notification is worse
// than one more attempt.
func pushNtfy(topic, sessionID, lastText, mirrorURL string) {
	pushNtfyMessage(topic, ntfyTitle("task finished", sessionID),
		fallback(lastText, "Agent task finished (no last message captured)"), mirrorURL)
}

// pushQuestionNtfy pushes one ntfy notification for an agent question (a
// PreToolUse event of the ask-questions tool): the questions and their
// options as the body, the mirror URL as the Click target. Same
// best-effort contract as pushNtfy.
func pushQuestionNtfy(topic, sessionID string, toolInput jsontext.Value, mirrorURL string) {
	pushNtfyMessage(topic, ntfyTitle("question", sessionID),
		fallback(formatQuestions(toolInput), "Agent asked a question (details unavailable)"), mirrorURL)
}

// ntfyTitle builds the notification title with the short session id.
func ntfyTitle(what, sessionID string) string {
	title := "VS Code Copilot: " + what
	if sessionID != "" {
		id := sessionID
		if len(id) > 8 {
			id = id[:8]
		}
		title += " [" + id + "]"
	}
	return title
}

// fallback returns msg when it is non-blank, else the default text.
func fallback(msg, def string) string {
	if strings.TrimSpace(msg) == "" {
		return def
	}
	return msg
}

// pushNtfyMessage performs the (truncating, retrying) ntfy push for a
// pre-built title/body pair.
func pushNtfyMessage(topic, title, msg, mirrorURL string) {
	if r := []rune(msg); len(r) > ntfyMaxLen {
		msg = string(r[:ntfyMaxLen]) + " ...[truncated]"
	}
	client := &http.Client{Timeout: ntfyTimeout}
	var lastErr error
	for attempt := 1; attempt <= ntfyMaxAttempts; attempt++ {
		if attempt > 1 {
			time.Sleep(ntfyRetryDelay)
		}
		lastErr = ntfyPushOnce(client, topic, title, msg, mirrorURL)
		if lastErr == nil || !ntfyRetryable(lastErr) {
			break
		}
	}
	if lastErr != nil {
		reportNtfyError(lastErr)
	}
}

// formatQuestions renders the ask-questions tool input as a phone-friendly
// message: each question's text followed by its option labels. The tool
// input shape (probed 2026-09-22): {"questions":[{"header","question",
// "options":[{"label"}...]}]}.
func formatQuestions(toolInput jsontext.Value) string {
	var in struct {
		Questions []struct {
			Question string `json:"question"`
			Options  []struct {
				Label string `json:"label"`
			} `json:"options"`
		} `json:"questions"`
	}
	if len(toolInput) == 0 || json.Unmarshal(toolInput, &in) != nil {
		return ""
	}
	var b strings.Builder
	for i, q := range in.Questions {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "%d. %s", i+1, q.Question)
		for _, o := range q.Options {
			if o.Label != "" {
				b.WriteString("\n   - ")
				b.WriteString(o.Label)
			}
		}
	}
	return b.String()
}

// ntfyPushOnce performs a single ntfy push attempt.
func ntfyPushOnce(client *http.Client, topic, title, msg, mirrorURL string) error {
	req, err := http.NewRequest(http.MethodPost, ntfyBaseURL+"/"+topic, strings.NewReader(msg))
	if err != nil {
		return fmt.Errorf("build ntfy request: %w", err)
	}
	req.Header.Set("Title", title)
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if mirrorURL != "" {
		req.Header.Set("Click", mirrorURL)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("ntfy push: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return ntfyStatusErr{code: resp.StatusCode}
	}
	return nil
}

// ntfyStatusErr marks a non-2xx ntfy response.
type ntfyStatusErr struct{ code int }

func (e ntfyStatusErr) Error() string {
	return fmt.Sprintf("ntfy push returned %d", e.code)
}

// ntfyRetryable reports whether a push failure is worth retrying:
// transport errors and 5xx responses are; 4xx (e.g. unknown topic) is not.
func ntfyRetryable(err error) bool {
	if se, ok := errors.AsType[ntfyStatusErr](err); ok {
		return se.code >= 500
	}
	return true
}

// reportNtfyError reports an ntfy failure on stderr and in the app log
// (the GUI build has no console, so stderr alone is not visible).
func reportNtfyError(err error) {
	fmt.Fprintln(os.Stderr, "hook:", err)
	logHookError(err)
}

// lastAssistantText extracts the last assistant text from a session
// transcript (best effort: the transcript format is not a stable API per
// VS Code docs). VS Code format: one JSON object per line, assistant turns
// as {"type":"assistant.message","data":{"content":"..."}}. Fallback:
// role-based entries (e.g. Claude Code): {"message":{"role":"assistant",
// "content": ...}}.
func lastAssistantText(path string) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := bufio.NewScanner(f)
	lastText := ""
	for buf.Scan() {
		line := strings.TrimSpace(buf.Text())
		if line == "" || buf.Err() != nil {
			continue
		}
		var entry struct {
			Type    string         `json:"type"`
			Role    string         `json:"role"`
			Content jsontext.Value `json:"content"`
			Data    struct {
				Content string `json:"content"`
			} `json:"data"`
			Message struct {
				Role    string         `json:"role"`
				Content jsontext.Value `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		// VS Code format.
		if entry.Type == "assistant.message" {
			if c := strings.TrimSpace(entry.Data.Content); c != "" {
				lastText = c
			}
			continue
		}
		// Generic role-based fallback.
		role := entry.Message.Role
		if role == "" {
			role = entry.Role
		}
		if role == "" {
			role = entry.Type
		}
		if role != "assistant" {
			continue
		}
		raw := entry.Message.Content
		if len(raw) == 0 {
			raw = entry.Content
		}
		if c := strings.TrimSpace(assistantContent(raw)); c != "" {
			lastText = c
		}
	}
	return lastText
}

// assistantContent flattens a role-based transcript content value: a
// plain string, or a list of blocks ({"text":"..."} objects and bare
// strings).
func assistantContent(raw jsontext.Value) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []jsontext.Value
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		var bs string
		if err := json.Unmarshal(b, &bs); err == nil {
			parts = append(parts, bs)
			continue
		}
		var blk struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(b, &blk); err == nil && blk.Text != "" {
			parts = append(parts, blk.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// hookLogPath is the app log path for hook error lines (a var so tests
// can redirect it away from the real log).
var hookLogPath = defaultLogPath()

// logHookError appends one line to the app log (the GUI build has no
// console, so stderr alone is not visible). Best effort.
func logHookError(err error) {
	p := hookLogPath
	if dir := filepath.Dir(p); dir != "" && dir != "." {
		if e := os.MkdirAll(dir, 0o755); e != nil {
			return
		}
	}
	f, e := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if e != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "time=%s hook: %v\n", time.Now().Format(time.RFC3339), err)
}
