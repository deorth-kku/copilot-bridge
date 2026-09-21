// Package watch provides the shared fsnotify file watcher used by the
// settings and workspaces stores: both hot-reload their file after a
// debounced quiet window and keep the previous snapshot on failure.
package watch

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// DefaultDebounce is the quiet period after the last change event before a
// reload is triggered. VS Code (and atomic-rename writers) can emit several
// events for a single save.
const DefaultDebounce = 150 * time.Millisecond

// WatchFile watches the directory containing path for changes to the file
// itself (matched by base name), debounces the events, and calls reload
// after each quiet window. It returns immediately; the watcher goroutine
// stops when ctx is cancelled.
//
// The directory (not the file) is watched so that atomic-rename writes
// (write temp file, rename over the file) are still detected, which a
// direct file watch would miss once the inode is replaced.
//
// name is a short label used in the watcher's log lines (e.g. "settings").
func WatchFile(ctx context.Context, name, path string, debounce time.Duration, reload func(), log *slog.Logger) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	if err := w.Add(filepath.Dir(path)); err != nil {
		w.Close()
		return err
	}
	go run(ctx, w, name, filepath.Base(path), debounce, reload, log)
	return nil
}

// run consumes watcher events, debounces them, and triggers reloads.
func run(ctx context.Context, w *fsnotify.Watcher, name, target string, debounce time.Duration, reload func(), log *slog.Logger) {
	defer w.Close()

	// Debounce: (re)arm a single timer on every matching event; the timer
	// channel is stable across Reset, so it can sit in the select below.
	timer := time.NewTimer(debounce)
	timer.Stop()
	tick := timer.C

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if filepath.Base(ev.Name) != target {
				continue
			}
			// Only content-affecting events matter. Remove is ignored: for an
			// atomic rename the file is re-created (Create fires) and the
			// in-place write path emits Write.
			if !ev.Has(fsnotify.Write) && !ev.Has(fsnotify.Create) && !ev.Has(fsnotify.Rename) {
				continue
			}
			// Drain any already-fired tick, then (re)arm the quiet window.
			if !timer.Stop() {
				select {
				case <-tick:
				default:
				}
			}
			timer.Reset(debounce)
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			log.Warn(name+" watcher error", "err", err)
		case <-tick:
			reload()
		}
	}
}
