// vscode-load-llama: background GUI process that watches VS Code Copilot
// chat inputs via CDP and pre-loads the selected model on the local
// llama.cpp server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"vscode-load-llama/internal/cdp"
	"vscode-load-llama/internal/config"
	"vscode-load-llama/internal/loader"
	"vscode-load-llama/internal/mirror"
)

func main() {
	cdpAddr := flag.String("cdp", "127.0.0.1:9222", "CDP HTTP address")
	settingsPath := flag.String("settings", defaultSettingsPath(), "path to VS Code settings.json")
	cooldown := flag.Duration("cooldown", 30*time.Second, "per-model load cooldown")
	logPath := flag.String("log", defaultLogPath(), "log file path")
	verbose := flag.Bool("verbose", false, "enable debug logging")
	web := flag.String("web", "0.0.0.0:9527", "mirror web address (empty to disable)")
	pane := flag.String("pane", defaultPaneSelectors, "comma-separated candidate selectors for the Copilot pane root")
	window := flag.String("window", "", "mirror this window (title substring; empty = first window)")
	flag.Parse()

	if err := run(*cdpAddr, *settingsPath, *cooldown, *logPath, *verbose, *web, *pane, *window); err != nil {
		// GUI builds have no console; the error is also in the log file
		// (if it could be opened).
		fmt.Fprintln(os.Stderr, err)
	}
}

func run(cdpAddr, settingsPath string, cooldown time.Duration, logPath string, verbose bool, webAddr, paneSel, windowFilter string) error {
	if dir := filepath.Dir(logPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create log dir: %w", err)
		}
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log file %s: %w", logPath, err)
	}
	defer f.Close()

	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	// settings.json is loaded once up front, then hot-reloaded via fsnotify.
	// The current snapshot lives in an atomic pointer inside the Store, so
	// readers never take a lock.
	store, err := config.NewStore(settingsPath, log)
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	log.Info("settings loaded", "path", settingsPath, "models", len(store.Load().Models))

	ctx, stop := signal.NotifyContext(context.Background(), signals...)
	defer stop()
	if err := store.Watch(ctx); err != nil {
		// Non-fatal: keep monitoring with the initial snapshot.
		log.Error("start settings watcher", "err", err)
	}

	// The loader resolves the proxy per-request from the current atomic
	// snapshot, so http.proxy / http.noProxy changes hot-reload too.
	ld := loader.New(cooldown, log, store.ProxyFunc)
	events := make(chan cdp.Event, 256)
	disc := cdp.NewDiscovery(cdpAddr, events, log)
	go disc.Run(ctx)

	// Optional: forward the Copilot pane to a browser page.
	if webAddr != "" {
		mir := mirror.New(disc, log, webAddr, splitSelectors(paneSel), windowFilter)
		go func() {
			if err := mir.Run(ctx); err != nil {
				log.Error("mirror server", "err", err)
			}
		}()
	}

	log.Info("monitoring", "cdp", cdpAddr, "cooldown", cooldown.String())

	for {
		select {
		case <-ctx.Done():
			log.Info("shutting down")
			return nil
		case ev := <-events:
			processEvent(ev, store, ld, log)
		}
	}
}

// processEvent handles one chat input event: skip empty input / missing
// model, look the model up in the settings table, then request a load
// (cooldown-gated inside the loader).
func processEvent(ev cdp.Event, store *config.Store, ld *loader.Loader, log *slog.Logger) {
	// Monaco inserts nbsp; normalize before the emptiness check.
	input := strings.TrimSpace(strings.ReplaceAll(ev.Input, "\u00a0", " "))
	if input == "" {
		log.Debug("skip: empty input", "window", ev.Window)
		return
	}
	if ev.Model == "" {
		log.Debug("skip: no model", "window", ev.Window)
		return
	}
	// Atomic read of the current settings snapshot (no lock).
	m, ok := store.Load().Models[ev.Model]
	if !ok {
		log.Warn("model not found in settings, skip", "model", ev.Model, "window", ev.Window)
		return
	}
	log.Info("input event", "window", ev.Window, "model", ev.Model,
		"effort", ev.Effort, "mode", ev.Mode, "inputLen", len(input))
	// Async: Load does a blocking HTTP POST (up to the client timeout).
	// A goroutine keeps the event loop responsive for all windows; the
	// mutex-gated cooldown inside the loader still dedupes concurrent
	// requests per model.
	go ld.Load(m)
}

// defaultSettingsPath returns the VS Code user settings.json location for
// the current platform:
//   - Windows: %APPDATA%\Code\User\settings.json
//   - Linux:   ~/.config/Code/User/settings.json
//   - macOS:   ~/Library/Application Support/Code/User/settings.json
func defaultSettingsPath() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "Code", "User", "settings.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "Code", "User", "settings.json")
}

// defaultLogPath returns the log file location in the platform temp dir,
// e.g. %TEMP%\vscode-load-llama\app.log on Windows, /tmp/... on Linux,
// $TMPDIR/... on macOS.
func defaultLogPath() string {
	return filepath.Join(os.TempDir(), "vscode-load-llama", "app.log")
}

// defaultPaneSelectors are the candidate Copilot pane root selectors, tried
// in priority order. The full chat pane container comes first: it spans both
// the chat-view title bar (back button / session title / sidebar toggle, so
// the mirror can navigate back to the session picker) and the agent-sessions
// list (the session-selection view shown when the user goes back), neither of
// which is inside .interactive-session. The narrower chat-session and input
// selectors remain as fallbacks. Override with -pane if your VS Code version
// uses different markup.
const defaultPaneSelectors = ".voice-agent-controls-wrapper,.pane.chat-viewpane-container,.pane-body.chat-viewpane,.interactive-session,.chat-view-part,.chat-view,.chat-editor,.interactive-input-part,.chat-input-container"

// splitSelectors splits a comma-separated selector list, trimming blanks.
func splitSelectors(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
