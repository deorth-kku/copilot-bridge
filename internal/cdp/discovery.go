package cdp

import (
	"context"
	"encoding/json/v2"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Target is a workbench page target from /json/list.
type Target struct {
	ID    string
	Title string
	WSURL string
}

// Window is one live VS Code window as seen by the mirror: the CDP target
// id (stable while the window is open) plus the current window title.
// JSON tags match the mirror protocol's lowercase keys (the browser reads
// w.id / w.title).
type Window struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Discovery runs the connection state machine for the process lifetime:
//
//	waiting    -> CDP port unreachable; poll /json/version every 2s
//	monitoring -> rescan /json/list every 2s, start/stop per-window
//	             sessions as workbench targets appear/disappear
//
// When the port disappears (VS Code fully exited) all sessions are
// stopped, the target set is cleared, and the machine returns to
// waiting. On reconnect, sessions are rebuilt from a fresh /json/list
// (target ids change across VS Code restarts, so nothing is cached).
type Discovery struct {
	base   string
	log    *slog.Logger
	events chan Event
	client *http.Client
	Poll   time.Duration

	mu       sync.Mutex
	sessions map[string]*Session
	up       bool
}

func NewDiscovery(cdpAddr string, events chan Event, log *slog.Logger) *Discovery {
	return &Discovery{
		base:     "http://" + cdpAddr,
		log:      log,
		events:   events,
		client:   &http.Client{Timeout: 2 * time.Second},
		sessions: make(map[string]*Session),
		Poll:     2 * time.Second,
	}
}

// Run blocks until ctx is cancelled, then stops all sessions.
func (d *Discovery) Run(ctx context.Context) {
	ticker := time.NewTicker(d.Poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			d.stopAll()
			return
		case <-ticker.C:
			d.scan(ctx)
		}
	}
}

func (d *Discovery) scan(ctx context.Context) {
	if !d.reachable() {
		if d.up {
			d.log.Info("VS Code disconnected")
			d.stopAll()
		}
		d.up = false
		return
	}
	if !d.up {
		d.log.Info("VS Code connected")
		d.up = true
	}
	targets, err := d.listTargets()
	if err != nil {
		d.log.Debug("list targets failed", "err", err)
		return
	}
	d.mu.Lock()
	current := make(map[string]Target, len(targets))
	for _, t := range targets {
		current[t.ID] = t
	}
	// start new (or restart dead) sessions
	for id, t := range current {
		s, ok := d.sessions[id]
		if ok && !s.IsDone() {
			s.SetTitle(t.Title)
			continue
		}
		if ok {
			d.log.Info("reconnecting window", "title", t.Title)
		}
		s = NewSession(id, t.Title, t.WSURL, d.events, d.log)
		d.sessions[id] = s
		go s.Run(ctx)
	}
	// stop gone
	for id, s := range d.sessions {
		if _, ok := current[id]; !ok {
			s.Stop()
			d.log.Info("window closed", "title", s.Title())
			delete(d.sessions, id)
		}
	}
	d.mu.Unlock()
}

// SessionFor returns the first live session whose title contains match,
// or the first live session when match is empty. It returns nil when no
// window is currently attached.
//
// Note: with match empty and several windows open, the choice is arbitrary
// (map iteration order) — callers that need a stable window must use
// Windows()/SessionForID instead.
func (d *Discovery) SessionFor(match string) *Session {
	d.mu.Lock()
	defer d.mu.Unlock()
	var first *Session
	for _, s := range d.sessions {
		if s.IsDone() {
			continue
		}
		if match == "" {
			if first == nil {
				first = s
			}
			continue
		}
		if strings.Contains(s.Title(), match) {
			return s
		}
	}
	return first
}

// Windows returns the currently live windows sorted by title, so the order
// is stable across calls (unlike map iteration). The mirror uses it both to
// render its window picker and to pick a deterministic default window.
func (d *Discovery) Windows() []Window {
	d.mu.Lock()
	defer d.mu.Unlock()
	wins := make([]Window, 0, len(d.sessions))
	for _, s := range d.sessions {
		if s.IsDone() {
			continue
		}
		wins = append(wins, Window{ID: s.ID, Title: s.Title()})
	}
	sort.Slice(wins, func(i, j int) bool { return wins[i].Title < wins[j].Title })
	return wins
}

// SessionForID returns the live session with the given CDP target id, or nil
// when no such window is currently attached.
func (d *Discovery) SessionForID(id string) *Session {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.sessions[id]
	if s == nil || s.IsDone() {
		return nil
	}
	return s
}

func (d *Discovery) stopAll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, s := range d.sessions {
		s.Stop()
		delete(d.sessions, id)
	}
}

func (d *Discovery) reachable() bool {
	resp, err := d.client.Get(d.base + "/json/version")
	if err != nil {
		return false
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (d *Discovery) listTargets() ([]Target, error) {
	resp, err := d.client.Get(d.base + "/json/list")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var list []struct {
		ID    string `json:"id"`
		Type  string `json:"type"`
		URL   string `json:"url"`
		Title string `json:"title"`
		WSURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.UnmarshalRead(resp.Body, &list); err != nil {
		return nil, err
	}
	var out []Target
	for _, t := range list {
		if t.Type != "page" || !strings.Contains(t.URL, "workbench") {
			continue
		}
		// /json/list reports host "localhost"; normalize to 127.0.0.1
		out = append(out, Target{
			ID:    t.ID,
			Title: t.Title,
			WSURL: strings.Replace(t.WSURL, "localhost", "127.0.0.1", 1),
		})
	}
	return out, nil
}
