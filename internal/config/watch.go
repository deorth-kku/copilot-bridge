package config

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"sync/atomic"

	"copilot-bridge/internal/watch"
)

// Store holds the current Settings in an atomic pointer and can watch the
// settings file for changes, hot-reloading lock-free.
//
// Concurrency model: the watcher goroutine is the only writer and swaps the
// whole snapshot with an atomic store; every reader (the event loop, the
// loader's proxy resolver) takes an atomic load. No mutex is used anywhere,
// and a failed reload keeps the previous snapshot.
type Store struct {
	path    string
	log     *slog.Logger
	current atomic.Pointer[Settings]
}

// NewStore loads path once and returns a Store. Call Watch to start
// hot-reloading in the background.
func NewStore(path string, log *slog.Logger) (*Store, error) {
	s, err := Load(path)
	if err != nil {
		return nil, err
	}
	st := &Store{path: path, log: log}
	st.current.Store(s)
	return st, nil
}

// Load returns the current Settings snapshot (atomic read, no lock). The
// returned pointer is immutable for the lifetime of the snapshot.
func (st *Store) Load() *Settings {
	return st.current.Load()
}

func (st *Store) ProxyFunc(req *http.Request) (*url.URL, error) {
	pf := st.Load().ProxyFunc()
	if pf == nil {
		pf = http.ProxyFromEnvironment
	}
	return pf(req)
}

// Watch starts a background goroutine that watches the directory containing
// path for changes to the settings file and reloads the Store atomically.
// It returns immediately; the goroutine stops when ctx is cancelled.
func (st *Store) Watch(ctx context.Context) error {
	return watch.WatchFile(ctx, "settings", st.path, watch.DefaultDebounce, st.reload, st.log)
}

// reload re-reads the file and atomically swaps the pointer. On error the
// previous Settings are kept so a half-written or invalid file never blanks
// the model table.
func (st *Store) reload() {
	s, err := Load(st.path)
	if err != nil {
		st.log.Error("settings reload failed, keeping previous", "path", st.path, "err", err)
		return
	}
	st.current.Store(s)
	st.log.Info("settings reloaded", "path", st.path, "models", len(s.Models))
}
