package main

// The "hook" subcommand: invoked by VS Code agent hooks (SessionStart /
// Stop). VS Code writes the hook's JSON payload to the process's stdin;
// we forward it verbatim to the bridge's /api/hook endpoint, where the
// shutdown planner decides what to do with the event.
//
// The subcommand ALWAYS exits with status 0: VS Code treats exit code 2
// as a blocking error (shown to the model) and other non-zero codes as
// warnings, and a missing bridge must never disrupt the agent. Failures
// are reported on stderr and appended to the app log.

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// hookMaxBody caps the payload read from stdin (hook payloads are small
// JSON objects; the cap is a guard against a wedged pipe).
const hookMaxBody = 1 << 20

// hookTimeout bounds the round trip to the bridge. VS Code's default hook
// timeout is 30s; we stay far inside it.
const hookTimeout = 5 * time.Second

// runHookCommand is the entry point for the "hook" subcommand. It always
// exits with status 0 (see the package comment).
func runHookCommand(args []string) {
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	bridge := fs.String("bridge", "http://127.0.0.1:9527", "bridge HTTP address (the mirror web server)")
	if err := fs.Parse(args); err != nil {
		os.Exit(0)
	}
	if err := runHook(*bridge, os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, "hook:", err)
		logHookError(err)
	}
	os.Exit(0)
}

// runHook reads the hook payload from stdin and POSTs it to the bridge's
// /api/hook endpoint.
func runHook(bridge string, stdin io.Reader) error {
	body, err := io.ReadAll(io.LimitReader(stdin, hookMaxBody))
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	client := &http.Client{Timeout: hookTimeout}
	resp, err := client.Post(bridge+"/api/hook", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("post to %s/api/hook: %w", bridge, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("bridge returned %s", resp.Status)
	}
	return nil
}

// logHookError appends one line to the app log (the GUI build has no
// console, so stderr alone is not visible). Best effort.
func logHookError(err error) {
	p := defaultLogPath()
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
