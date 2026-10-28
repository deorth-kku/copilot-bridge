package main

// The "hook" subcommand: invoked by VS Code agent hooks (SessionStart /
// Stop / PreToolUse / PostToolUse). VS Code writes the hook's JSON payload
// to the process's stdin; we forward it verbatim to the bridge's
// /api/hook endpoint, where the shutdown planner decides what to do with
// the event.
//
// When -ntfy-topic is set, three events additionally push an ntfy.sh
// notification, all carrying the bridge's mirror URL as the
// notification's Click target (tapping the phone notification opens the
// mirror of the workspace the task ran in):
//   - Stop: the agent's last message (parsed from the session transcript,
//     best effort) as the body.
//   - PreToolUse of the ask-questions tool (tool_name
//     "vscode_askQuestions"): the questions and their options as the body,
//     so a phone notification arrives while the agent waits for an answer.
//   - PostToolUse of the task_complete tool: the summary from the tool
//     call arguments as the body.
//
// PostToolUse of task_complete additionally ALWAYS writes
// {"continue":false} with a stopReason to stdout, stopping the entire
// agent execution: VS Code's default behavior is to return to the agent
// once more after task_complete, which is unnecessary — the tool call
// itself signals completion.
//
// The subcommand ALWAYS exits with status 0: VS Code treats exit code 2
// as a blocking error (shown to the model) and other non-zero codes as
// warnings, and a missing bridge must never disrupt the agent. Failures
// are reported on stderr and appended to the app log.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path"
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

// systemOneQuestionID is the id of the single noul question the hook asks
// the decision model.
const systemOneQuestionID = "safe"

// systemOneQuestion is the noul question text sent to the model.
const systemOneQuestion = "Is this tool call safe to execute?"

// approveConfig is the on-disk auto-approval config (-approve-config JSON).
// A zero value (no rules) disables the feature entirely.
type approveConfig struct {
	Servers map[string]approveServer `json:"servers"`
	Rules   []approveRule            `json:"rules"`
}

// approveServer names one /v1/systemone decision-model endpoint.
type approveServer struct {
	Model   string        `json:"model"`
	URL     string        `json:"url"`
	Key     string        `json:"key"`
	Timeout time.Duration `json:"timeout"`
}

// approveRule gates one set of tools by a glob match. It is EITHER a local
// Policy (no model call) OR a model rule (Server + Thresholds) — not both.
// Rules are evaluated in order; the first match wins.
type approveRule struct {
	Match      string     `json:"match"`
	Policy     string     `json:"policy"`     // "allow" | "deny" | "ask" (local)
	Server     string     `json:"server"`     // server name (model rule)
	Thresholds [2]float64 `json:"thresholds"` // [t1, t2]
}

// ntfyBaseURL is the ntfy server base (a variable so tests can point it
// at a local test server).
var ntfyBaseURL = "https://ntfy.sh"

// ntfyMaxLen caps the notification body (keep the phone notification
// readable).
const ntfyMaxLen = 1500

// ntfyTimeout bounds a single ntfy push attempt.
const ntfyTimeout = 5 * time.Second

// ntfyMaxAttempts is the number of push attempts (one retry covers
// transient proxy/CDN hiccups, e.g. an EOF mid-request).
const ntfyMaxAttempts = 2

// ntfyRetryDelay separates retry attempts (a var so tests can skip the
// wait).
var ntfyRetryDelay = time.Second

// askQuestionsToolName is the tool_name VS Code puts in the PreToolUse
// payload for the ask-user-questions tool (probed 2026-09-22 from a live
// hook stdin dump).
const askQuestionsToolName = "vscode_askQuestions"

// taskCompleteToolName is the tool_name VS Code puts in the PostToolUse
// payload for the task-completion tool.
const taskCompleteToolName = "task_complete"

// taskCompleteStopOutput is the PostToolUse stdout JSON that stops the
// entire agent execution after task_complete (the stopReason is shown
// to the user).
const taskCompleteStopOutput = `{"continue":false,"stopReason":"task_complete signals completion; no further processing is required."}`

// hookStdout is where the hook's stdout JSON goes (a var so tests can
// capture it).
var hookStdout io.Writer = os.Stdout

// runHookCommand is the entry point for the "hook" subcommand. It always
// exits with status 0 (see the package comment).
func runHookCommand(args []string) {
	setupHookLogger()
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	bridge := fs.String("bridge", "http://127.0.0.1:9527", "bridge HTTP address (the mirror web server)")
	ntfyTopic := fs.String("ntfy-topic", "", "ntfy.sh topic for the task-finished notification (empty = no notification)")
	approveCfg := fs.String("approve-config", "", "path to the auto-approval config JSON (servers + rules); empty = disabled")
	if err := fs.Parse(args); err != nil {
		os.Exit(0)
	}
	slog.Info("hook start", "bridge", *bridge, "ntfy-topic", *ntfyTopic, "approve-config", *approveCfg)
	approve, _ := loadApproveConfig(*approveCfg)
	// The hook's lifetime is bounded by the process signals: a SIGTERM (or
	// Ctrl+C) cancels the context, which in turn cancels every in-flight
	// HTTP request below.
	ctx, stop := signal.NotifyContext(context.Background(), signals...)
	defer stop()
	if err := runHook(ctx, hookParams{Bridge: *bridge, NtfyTopic: *ntfyTopic, Stdin: os.Stdin, Approve: approve}); err != nil {
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
//
// When the approve config has rules, a PreToolUse event is gated by the
// first rule whose glob matches the tool_name: a local policy rule decides
// directly (allow/deny/ask, no model call); a model rule asks the named
// /v1/systemone server one noul question ("is this tool call safe?") and
// maps the 0..1 answer to allow/ask/deny via the rule's thresholds. An
// "ask" also pushes an ntfy notification. A non-matching tool, or any model
// failure, leaves stdout empty so VS Code's own permission flow decides
// (fail-open). ctx bounds every inner HTTP request: each derives a
// timeout child context, so a cancelled ctx (e.g. SIGTERM) aborts them.
type hookParams struct {
	Bridge    string
	NtfyTopic string
	Stdin     io.Reader
	Approve   approveConfig
}

func runHook(ctx context.Context, p hookParams) error {
	cfg := p.Approve
	body := new(bytes.Buffer)
	tr := io.TeeReader(io.LimitReader(p.Stdin, hookMaxBody), body)
	var ev struct {
		Name           string         `json:"hook_event_name"`
		SessionID      string         `json:"session_id"`
		TranscriptPath string         `json:"transcript_path"`
		ToolName       string         `json:"tool_name"`
		ToolInput      jsontext.Value `json:"tool_input"`
	}
	// Not fatal: the payload is forwarded verbatim regardless; the fields
	// are only needed for the ntfy notifications.
	_ = json.UnmarshalRead(tr, &ev)
	slog.Info("hook event", "event", ev.Name, "tool", ev.ToolName, "session", ev.SessionID)

	mirrorURL, bridgeErr := bridgeHook(ctx, p.Bridge, body)
	if bridgeErr != nil {
		slog.Warn("bridge hook failed", "err", bridgeErr)
	} else {
		slog.Info("bridge hook ok", "mirror", mirrorURL)
	}

	// PostToolUse of task_complete: always stop the agent execution.
	// VS Code's default is to return to the agent once more after
	// task_complete, which is unnecessary — the tool call itself signals
	// completion.
	if ev.Name == "PostToolUse" && ev.ToolName == taskCompleteToolName {
		fmt.Fprintln(hookStdout, taskCompleteStopOutput)
	}

	// PreToolUse auto-approval via the systemone decision model. Runs
	// after bridgeHook so an "ask" can carry the mirror URL as its ntfy
	// Click target. Independent of bridgeErr (a missing bridge must not
	// block the decision).
	if ev.Name == "PreToolUse" && len(cfg.Rules) > 0 {
		cfg.approveToolCall(ctx, p.NtfyTopic, ev.SessionID, ev.ToolName, ev.ToolInput, mirrorURL)
	}

	if p.NtfyTopic != "" {
		switch ev.Name {
		case "Stop":
			// Best effort: the last assistant message of the session
			// transcript (VS Code's transcript format is not a stable API).
			pushNtfy(ctx, p.NtfyTopic, ev.SessionID, lastAssistantText(ev.TranscriptPath), mirrorURL)
		case "PreToolUse":
			// PreToolUse fires for EVERY tool; only the ask-questions tool
			// gets a notification.
			if ev.ToolName == askQuestionsToolName {
				pushQuestionNtfy(ctx, p.NtfyTopic, ev.SessionID, ev.ToolInput, mirrorURL)
			}
		case "PostToolUse":
			// PostToolUse fires for EVERY tool; only task_complete gets a
			// notification.
			if ev.ToolName == taskCompleteToolName {
				pushTaskCompleteNtfy(ctx, p.NtfyTopic, ev.SessionID, ev.ToolInput, mirrorURL)
			}
		}
	}
	return bridgeErr
}

func parseTime(data []byte, t *time.Duration) error {
	var str string
	err := json.Unmarshal(data, &str)
	if err != nil {
		return err
	}
	t0, err := time.ParseDuration(str)
	if err != nil {
		return err
	}
	*t = t0
	return nil
}

// approveToolCall auto-approves one PreToolUse call via the systemone
// decision model. For a matching tool it writes a permissionDecision to
// stdout and (on "ask") pushes an ntfy notification. A non-matching tool or
// any failure leaves stdout untouched (fail-open: VS Code decides).
// loadApproveConfig reads and validates the auto-approval config file. ok is
// false when the path is empty (disabled) or the file cannot be read/parsed/
// validated — the caller then runs with the feature disabled (fail-open).
func loadApproveConfig(path string) (approveConfig, bool) {
	var cfg approveConfig
	if path == "" {
		slog.Info("approve: disabled (no -approve-config)")
		return cfg, false
	}
	f, err := os.Open(path)
	if err != nil {
		reportApproveError(fmt.Errorf("open approve config: %w", err))
		return approveConfig{}, false
	}
	defer f.Close()
	if err := json.UnmarshalRead(f, &cfg, json.WithUnmarshalers(json.UnmarshalFunc(parseTime))); err != nil {
		reportApproveError(fmt.Errorf("parse approve config %s: %w", path, err))
		return approveConfig{}, false
	}
	if err := cfg.validate(); err != nil {
		reportApproveError(err)
		return approveConfig{}, false
	}
	slog.Info("approve: config loaded", "path", path, "rules", len(cfg.Rules), "servers", len(cfg.Servers))
	return cfg, true
}

// validate checks the config is internally consistent: every rule has a
// match and exactly one of a local policy or a model rule (server +
// thresholds); a model rule references a known server with url + model.
func (c approveConfig) validate() error {
	for i, r := range c.Rules {
		if r.Match == "" {
			return fmt.Errorf("rules[%d]: missing match", i)
		}
		hasPolicy := r.Policy != ""
		hasModel := r.Server != ""
		if hasPolicy == hasModel {
			return fmt.Errorf("rules[%d]: needs exactly one of policy or server", i)
		}
		if hasPolicy {
			if r.Policy != "allow" && r.Policy != "deny" && r.Policy != "ask" {
				return fmt.Errorf("rules[%d]: invalid policy %q", i, r.Policy)
			}
			continue
		}
		srv, ok := c.Servers[r.Server]
		if !ok {
			return fmt.Errorf("rules[%d]: unknown server %q", i, r.Server)
		}
		if srv.URL == "" || srv.Model == "" {
			return fmt.Errorf("rules[%d]: server %q needs url and model", i, r.Server)
		}
		if !validThresholds(r.Thresholds) {
			return fmt.Errorf("rules[%d]: thresholds must be [t1,t2] with 0<=t1<t2<=1", i)
		}
	}
	return nil
}

// validThresholds reports whether [t1,t2] is a valid deny/ask/allow split.
func validThresholds(t [2]float64) bool {
	return t[0] >= 0 && t[1] <= 1 && t[0] < t[1]
}

// toolMatchesOne reports whether name matches the single glob pattern
// (path.Match semantics: * matches any run of characters). An invalid
// pattern matches nothing.
func toolMatchesOne(name, pattern string) bool {
	ok, _ := path.Match(pattern, name)
	return ok
}

// approveToolCall gates one PreToolUse call. It walks the rules in order and
// applies the first whose glob matches the tool_name: a local policy rule
// decides directly (no model call); a model rule asks the named server one
// noul question and maps the 0..1 answer to allow/ask/deny. An "ask" pushes
// an ntfy notification. A non-matching tool, or any model failure, leaves
// stdout empty (fail-open: VS Code decides).
func (cfg approveConfig) approveToolCall(ctx context.Context, ntfyTopic, sessionID, toolName string, toolInput jsontext.Value, mirrorURL string) {
	slog.Info("approve: evaluating", "tool", toolName, "rules", len(cfg.Rules))
	for i, r := range cfg.Rules {
		if !toolMatchesOne(toolName, r.Match) {
			slog.Debug("approve: rule no match", "rule", i, "match", r.Match, "tool", toolName)
			continue
		}
		if r.Policy != "" {
			// Local rule: decide without calling the model.
			slog.Info("approve: local rule", "rule", i, "match", r.Match, "policy", r.Policy)
			emitPermissionDecision(r.Policy, "local rule: "+r.Policy)
			if r.Policy == "ask" && ntfyTopic != "" {
				pushAskNtfy(ctx, ntfyTopic, sessionID, toolName, toolInput, mirrorURL)
			}
			return
		}
		slog.Info("approve: model rule", "rule", i, "match", r.Match, "server", r.Server)
		srv := cfg.Servers[r.Server]
		v, ok := srv.callSystemOne(ctx, toolName, toolInput)
		if !ok {
			// Model unreachable / bad response: fail-open.
			slog.Warn("approve: model fail-open", "server", r.Server, "tool", toolName)
			return
		}
		decision := noulDecision(v, r.Thresholds[0], r.Thresholds[1])
		slog.Info("approve: decision", "decision", decision, "noul", v, "t1", r.Thresholds[0], "t2", r.Thresholds[1])
		emitPermissionDecision(decision, noulReason(v, r.Thresholds[0], r.Thresholds[1]))
		if decision == "ask" && ntfyTopic != "" {
			pushAskNtfy(ctx, ntfyTopic, sessionID, toolName, toolInput, mirrorURL)
		}
		return
	}
	// No rule matched: keep the current behavior (no decision).
	slog.Info("approve: no rule matched", "tool", toolName)
}

// noulDecision maps a 0..1 noul value to a permission decision using the two
// thresholds: <t1 deny, <t2 ask, else allow.
func noulDecision(v, t1, t2 float64) string {
	switch {
	case v < t1:
		return "deny"
	case v < t2:
		return "ask"
	default:
		return "allow"
	}
}

// noulReason builds the permissionDecisionReason shown to the agent.
func noulReason(v, t1, t2 float64) string {
	switch noulDecision(v, t1, t2) {
	case "deny":
		return fmt.Sprintf("systemone noul %.2f < %.2f", v, t1)
	case "ask":
		return fmt.Sprintf("systemone noul %.2f in [%.2f, %.2f)", v, t1, t2)
	default:
		return fmt.Sprintf("systemone noul %.2f >= %.2f", v, t2)
	}
}

// systemOneRequest is the /v1/systemone request body: one noul question about
// the safety of a tool call.
type systemOneRequest struct {
	Model     string `json:"model"`
	State     string `json:"state"`
	Questions struct {
		Safe struct {
			Type         string `json:"type"`
			Instructions string `json:"instructions"`
		} `json:"safe"`
	} `json:"questions"`
}

// callSystemOne asks the /v1/systemone decision model the single noul
// question "is this tool call safe?" and returns the 0..1 noul probability.
// ok is false on any transport/HTTP/parse failure or a missing noul value
// (the caller then fails open).
func (srv approveServer) callSystemOne(ctx context.Context, toolName string, toolInput jsontext.Value) (float64, bool) {
	// state = the tool call as JSON text, so the model sees name + args.
	state, err := json.Marshal(struct {
		ToolName  string         `json:"tool_name"`
		ToolInput jsontext.Value `json:"tool_input"`
	}{ToolName: toolName, ToolInput: toolInput})
	if err != nil {
		reportApproveError(fmt.Errorf("marshal state: %w", err))
		return 0, false
	}
	req := systemOneRequest{Model: srv.Model, State: string(state)}
	req.Questions.Safe.Type = "noul"
	req.Questions.Safe.Instructions = systemOneQuestion
	reqBody, err := json.Marshal(req)
	if err != nil {
		reportApproveError(fmt.Errorf("marshal request: %w", err))
		return 0, false
	}

	// Derive a per-call timeout child context instead of relying on the
	// http.Client timeout field.
	if srv.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, srv.Timeout)
		defer cancel()
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, bytes.NewReader(reqBody))
	if err != nil {
		reportApproveError(fmt.Errorf("build systemone request: %w", err))
		return 0, false
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if srv.Key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+srv.Key)
	}
	resp, err := new(http.Client).Do(httpReq)
	if err != nil {
		reportApproveError(fmt.Errorf("systemone call: %w", err))
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		reportApproveError(fmt.Errorf("systemone returned %s", resp.Status))
		return 0, false
	}
	var out struct {
		Answers struct {
			Safe struct {
				Noul float64 `json:"noul"`
			} `json:"safe"`
		} `json:"answers"`
	}
	out.Answers.Safe.Noul = math.NaN()
	if err := json.UnmarshalRead(io.LimitReader(resp.Body, 1<<20), &out); err != nil {
		reportApproveError(fmt.Errorf("parse systemone response: %w", err))
		return 0, false
	}
	if math.IsNaN(out.Answers.Safe.Noul) {
		reportApproveError(fmt.Errorf("systemone response has no answers.%s.noul", systemOneQuestionID))
		return 0, false
	}
	return out.Answers.Safe.Noul, true
}

// preToolUseDecisionOutput is the PreToolUse stdout JSON the Copilot
// extension expects. chatHookService.executePreToolUseHook reads the decision
// from hookSpecificOutput.permissionDecision and returns early (ignoring the
// decision) when hookSpecificOutput is absent, so the decision MUST be wrapped
// in a hookSpecificOutput object — a top-level permissionDecision is dropped.
type preToolUseDecisionOutput struct {
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

// emitPermissionDecision writes the PreToolUse decision JSON to stdout. The
// reason is always included (required for "deny").
func emitPermissionDecision(decision, reason string) {
	var out preToolUseDecisionOutput
	out.HookSpecificOutput.HookEventName = "PreToolUse"
	out.HookSpecificOutput.PermissionDecision = decision
	out.HookSpecificOutput.PermissionDecisionReason = reason
	b, err := json.Marshal(out)
	if err != nil {
		reportApproveError(fmt.Errorf("marshal decision: %w", err))
		return
	}
	fmt.Fprintln(hookStdout, string(b))
}

// pushAskNtfy pushes one ntfy notification for a tool call the model scored
// as "ask": the tool name and its input as the body, the mirror URL as the
// Click target. Same best-effort contract as the other ntfy pushes.
func pushAskNtfy(ctx context.Context, topic, sessionID, toolName string, toolInput jsontext.Value, mirrorURL string) {
	toolInput.Indent(jsontext.WithIndent("  "))
	body := fmt.Sprintf("Tool %s needs approval\n%s", toolName, string(toolInput))
	pushNtfyMessage(ctx, topic, ntfyTitle("tool approval needed", sessionID), body, mirrorURL)
}

// reportApproveError reports a systemone approval failure on stderr and in
// the app log. Failures are fail-open: they never write a decision to stdout.
func reportApproveError(err error) {
	fmt.Fprintln(os.Stderr, "hook:", err)
	logHookError(err)
}

// bridgeHook forwards the payload to the bridge's /api/hook endpoint and
// returns the mirror URL from the response ("" when the bridge is
// unreachable or the response does not carry one).
func bridgeHook(ctx context.Context, bridge string, body io.Reader) (string, error) {
	// Derive a per-call timeout child context instead of relying on the
	// http.Client timeout field.
	ctx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, bridge+"/api/hook", body)
	if err != nil {
		return "", fmt.Errorf("build bridge request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := new(http.Client).Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("post to %s/api/hook: %w", bridge, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("bridge returned %s", resp.Status)
	}
	var out struct {
		Mirror string `json:"mirror"`
	}
	if jerr := json.UnmarshalRead(io.LimitReader(resp.Body, 1<<20), &out); jerr != nil {
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
func pushNtfy(ctx context.Context, topic, sessionID, lastText, mirrorURL string) {
	pushNtfyMessage(ctx, topic, ntfyTitle("task finished", sessionID),
		fallback(lastText, "Agent task finished (no last message captured)"), mirrorURL)
}

// pushQuestionNtfy pushes one ntfy notification for an agent question (a
// PreToolUse event of the ask-questions tool): the questions and their
// options as the body, the mirror URL as the Click target. Same
// best-effort contract as pushNtfy.
func pushQuestionNtfy(ctx context.Context, topic, sessionID string, toolInput jsontext.Value, mirrorURL string) {
	pushNtfyMessage(ctx, topic, ntfyTitle("question", sessionID),
		fallback(formatQuestions(toolInput), "Agent asked a question (details unavailable)"), mirrorURL)
}

// pushTaskCompleteNtfy pushes one ntfy notification for a completed task
// (a PostToolUse event of the task_complete tool): the summary from the
// tool call arguments as the body, the mirror URL as the Click target.
// Same best-effort contract as pushNtfy.
func pushTaskCompleteNtfy(ctx context.Context, topic, sessionID string, toolInput jsontext.Value, mirrorURL string) {
	pushNtfyMessage(ctx, topic, ntfyTitle("task complete", sessionID),
		fallback(taskCompleteSummary(toolInput), "Agent task complete (no summary captured)"), mirrorURL)
}

// taskCompleteSummary extracts the summary field from the task_complete
// tool call arguments.
func taskCompleteSummary(toolInput jsontext.Value) string {
	var in struct {
		Summary string `json:"summary"`
	}
	if len(toolInput) == 0 || json.Unmarshal(toolInput, &in) != nil {
		return ""
	}
	return in.Summary
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
func pushNtfyMessage(ctx context.Context, topic, title, msg, mirrorURL string) {
	if r := []rune(msg); len(r) > ntfyMaxLen {
		msg = string(r[:ntfyMaxLen]) + " ...[truncated]"
	}
	var lastErr error
	for attempt := 1; attempt <= ntfyMaxAttempts; attempt++ {
		if attempt > 1 {
			// Honor cancellation during the retry delay.
			select {
			case <-ctx.Done():
				return
			case <-time.After(ntfyRetryDelay):
			}
		}
		lastErr = ntfyPushOnce(ctx, topic, title, msg, mirrorURL)
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
func ntfyPushOnce(ctx context.Context, topic, title, msg, mirrorURL string) error {
	// Derive a per-attempt timeout child context instead of relying on the
	// http.Client timeout field.
	ctx, cancel := context.WithTimeout(ctx, ntfyTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ntfyBaseURL+"/"+topic, strings.NewReader(msg))
	if err != nil {
		return fmt.Errorf("build ntfy request: %w", err)
	}
	req.Header.Set("Title", title)
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if mirrorURL != "" {
		req.Header.Set("Click", mirrorURL)
	}
	resp, err := new(http.Client).Do(req)
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

// setupHookLogger installs the hook subcommand's file logger as the process
// default (slog.SetDefault), mirroring the daemon's run(): it opens
// defaultHookLogPath ONCE (the same folder as the service log, a different
// filename) and writes slog text records to it. If the file cannot be
// opened it falls back to stderr so a logging failure never breaks the hook.
// Tests call runHook directly (not runHookCommand) and install their own
// default logger.
func setupHookLogger() {
	p := defaultHookLogPath()
	var w io.Writer = os.Stderr
	if dir := filepath.Dir(p); dir != "" && dir != "." {
		if e := os.MkdirAll(dir, 0o755); e == nil {
			if f, e := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); e == nil {
				w = f
			}
		}
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})))
}

// logHookError reports one hook error on the default logger (the GUI build
// has no console, so stderr alone is not visible). Best effort.
func logHookError(err error) {
	slog.Error("hook", "err", err)
}
