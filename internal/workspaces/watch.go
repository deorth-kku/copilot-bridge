package workspaces

import (
	"context"
	"log/slog"
	"sync/atomic"

	"copilot-bridge/internal/watch"
)

// Store holds the current workspace list in an atomic pointer and can
// watch storage.json for changes, hot-reloading lock-free.
//
// Concurrency model: the watcher goroutine is the only writer and swaps
// the whole snapshot with an atomic store; every reader (the HTTP API,
// the WS pushes) takes an atomic load. No mutex is used, and a failed
// reload keeps the previous snapshot.
type Store struct {
	path    string
	log     *slog.Logger
	current atomic.Pointer[[]Workspace]
	// onChange is called (on the watcher goroutine) after every successful
	// reload, with the new snapshot. Optional.
	onChange func([]Workspace)
}

// NewStore loads path once and returns a Store. Call Watch to start
// hot-reloading in the background.
func NewStore(path string, log *slog.Logger, onChange func([]Workspace)) (*Store, error) {
	ws, err := List(path)
	if err != nil {
		return nil, err
	}
	st := &Store{path: path, log: log, onChange: onChange}
	st.current.Store(&ws)
	return st, nil
}

// Load returns the current workspace list snapshot (atomic read, no lock).
func (st *Store) Load() []Workspace {
	return *st.current.Load()
}

// Watch starts a background goroutine that watches the directory
// containing path for changes to the storage file and reloads the Store
// atomically. It returns immediately; the goroutine stops when ctx is
// cancelled.
func (st *Store) Watch(ctx context.Context) error {
	return watch.WatchFile(ctx, "workspaces", st.path, watch.DefaultDebounce, st.reload, st.log)
}

// reload re-reads the file and atomically swaps the pointer. On error the
// previous list is kept so a half-written or invalid file never blanks the
// page. On success onChange (if any) is called with the new snapshot.
func (st *Store) reload() {
	ws, err := List(st.path)
	if err != nil {
		st.log.Error("workspaces reload failed, keeping previous", "path", st.path, "err", err)
		return
	}
	st.current.Store(&ws)
	st.log.Info("workspaces reloaded", "path", st.path, "count", len(ws))
	if st.onChange != nil {
		st.onChange(ws)
	}
}
