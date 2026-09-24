package mirror

// Package mirror forwards a VS Code Copilot pane to a browser page.
//
// Rendering: the pane's HTML subtree plus the (self-contained) workbench CSS
// are extracted from the live page over CDP and pushed to the browser over a
// WebSocket. Interactivity: the browser captures mouse/keyboard events as
// coordinates and the mirror translates them back onto the live page using
// the CDP Input domain. No page-side JS is injected for interactivity, so
// the page is modified as little as possible.

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go4org/hashtriemap"
	"github.com/gorilla/websocket"

	"copilot-bridge/internal/cdp"
	"copilot-bridge/internal/shutdown"
	"copilot-bridge/internal/workspaces"
)

// Mirror owns the web server, the set of connected browser tabs, and the
// publish loop that keeps each mirrored pane in sync with its live page.
type Mirror struct {
	disc      *cdp.Discovery
	log       *slog.Logger
	addr      string
	selectors []string
	window    string
	// storagePath is the VS Code globalStorage storage.json the workspaces
	// page lists.
	storagePath string
	// sshConfigPath is the SSH client config (~/.ssh/config) the hook
	// endpoint uses to map a remote request's source host to a machine
	// (empty = no remote identification).
	sshConfigPath string
	// opener launches workspaces through the code CLI (workspaces page).
	opener *workspaces.Opener
	// wsStore is the hot-reloaded workspace list (storage.json, watched like
	// settings.json). Readers take an atomic load; the watcher pushes a
	// "workspaces" message to the workspaces-page clients on every
	// successful reload. Nil when the initial load failed (the HTTP API
	// then falls back to a fresh read per request).
	wsStore *workspaces.Store
	// fallback bounds how often the publish loop probes even when the page
	// sent no wake event. It covers changes the injected observer cannot
	// see (CSSOM-only edits, dropped wakes) and re-resolves the session
	// after a window switch or reload.
	fallback time.Duration

	upgrader websocket.Upgrader

	// clients is the set of connected browser tabs. It is a lock-free
	// hash-trie map, so add/remove/send never take a shared lock.
	clients hashtriemap.HashTrieMap[*client, struct{}]

	// imgs caches fetched image resources by the hash of their source URL.
	// A lock-free hash-trie map: reads from the HTTP handlers and writes
	// from the single publish-loop goroutine never block.
	imgs hashtriemap.HashTrieMap[string, imgEntry]

	// snaps holds the latest extracted pane snapshot per mirrored window.
	// The map is mutable shared state, so it is guarded by snapMu; the
	// (potentially large) html/css strings are shared by reference across
	// snapshots, so a swap is cheap.
	snapMu sync.Mutex
	snaps  map[string]*snapState

	// hub fans the MirrorWake channels of all watched sessions into one
	// channel so the publish loop can select on "any watched window
	// changed" (the watched window set is dynamic, so a plain select
	// cannot express it).
	hub *wakeHub
	// refreshNow is signalled by the WS handler (new client, picker change)
	// so the publish loop re-resolves the client groups and refreshes
	// immediately instead of waiting for the next fallback tick. Buffered
	// by 1: one pending signal coalesces.
	refreshNow chan struct{}

	// toggleTimes records the last auto Toggle-Chat click per window, so a
	// missing pane does not re-click the button on every refresh (the pane
	// takes a moment to render after a click, and a window without the
	// button must not be re-probed constantly). Only the publish-loop
	// goroutine touches it.
	toggleTimes map[string]time.Time

	// wsRefresh is signalled by the storage.json watcher (the known
	// workspace list changed) so the publish loop recomputes the
	// CDP-overlaid list. Buffered by 1: one pending signal coalesces.
	wsRefresh chan struct{}
	// lastWsPush is the last workspace list pushed to the workspaces-page
	// clients; it drives change detection for the next push. Only the
	// publish-loop goroutine touches it.
	lastWsPush []workspaces.Workspace

	// planner drives the armed power-off: the hook subcommand feeds Stop /
	// SessionStart events via POST /api/hook, the workspaces page arms /
	// disarms via POST /api/shutdown. Nil when not configured (the
	// endpoints answer 503 then).
	planner *shutdown.Planner
}

// snapState is one extracted pane snapshot.
type snapState struct {
	html       string
	css        string
	cssVer     string
	cssFP      string
	rootStyle  string
	themeVars  string
	themeVer   string
	themeBg    string
	window     string
	windowID   string
	fp         string
	rect       cdp.PaneRect
	scroll     cdp.PaneScroll
	scrollPath []int
	scrollRows []float64
	// nestedScrolls: scroll state of nested scrollers (reasoning-trace
	// lists) the main scroller does not cover.
	nestedScrolls []cdp.NestedScroll
	// inputFocused: whether the live chat input holds the page focus (the
	// mirror shows/blinks the input cursor only then).
	inputFocused bool
	// cursorChar: the live cursor's character index (-1 = no cursor); the
	// mirror re-seats the cursor by this index at its own (reflowed) width.
	cursorChar int
	// inputBreaks: hard-newline vs soft-wrap boundaries between the live
	// input's view lines (see cdp.FingerprintState.InputBreaks); the mirror
	// re-groups the view lines across soft wraps.
	inputBreaks []bool
	// popupHTML/popupFP capture the visible context view (popup menu) that
	// renders outside the pane root; popupFP == "" means no popup is open.
	popupHTML string
	popupFP   string
	popupLeft float64
	popupTop  float64
	popupW    float64
	popupH    float64
}

// client is one connected browser tab.
type client struct {
	logger *slog.Logger
	conn   *websocket.Conn
	// send carries server -> browser frames (stateMsg for mirror tabs,
	// workspacesMsg for workspaces-page tabs); the writer marshals any.
	send chan any
	done chan struct{}
	// wsPage marks a workspaces-page tab (?page=workspaces): it consumes
	// only "workspaces" list pushes and is excluded from the publish
	// loop's pane-state broadcast.
	wsPage bool
	// selID is this tab's status-bar window picker selection (nil = follow
	// the default: -window flag, else first window). Per-client, so several
	// browsers can mirror different VS Code windows at once. Written by the
	// WS handler, read by the publish loop — an atomic pointer keeps it
	// lock-free.
	selID atomic.Pointer[string]
	// lastAnchor is the DOM path (from the pane root) of the control THIS
	// tab last clicked (a pressed mouse event carrying an anchorPath). The
	// publish loop resolves it to a live rect to anchor the open popup's
	// position for this tab. Written by the WS handler, read by the publish
	// loop.
	lastAnchor atomic.Pointer[[]int]
	// lastWin is the window id of the last successful full state this tab
	// rendered (nil = none yet). When the tab's current window differs from
	// it (just connected, or just switched via the picker), the next
	// refresh forces a full HTML/CSS send for this tab even if the window
	// has not changed since the last refresh.
	lastWin atomic.Pointer[string]
}

// wakeHub fans the MirrorWake channels of several sessions into one channel
// so the publish loop can select on "any watched window changed". One
// forwarding goroutine runs per subscribed channel; it is cancelled via a
// quit channel because mirrorWake is never closed.
type wakeHub struct {
	mu   sync.Mutex
	out  chan struct{}
	subs map[<-chan struct{}]chan struct{} // wake channel -> its quit signal
}

func newWakeHub() *wakeHub {
	return &wakeHub{out: make(chan struct{}, 64), subs: make(map[<-chan struct{}]chan struct{})}
}

// add subscribes one session's wake channel. Idempotent: the forwarding
// goroutine is spawned once per channel.
func (h *wakeHub) add(ch <-chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[ch]; ok {
		return
	}
	quit := make(chan struct{})
	h.subs[ch] = quit
	go func() {
		for {
			select {
			case <-ch:
				// Non-blocking: one pending token already guarantees a
				// refresh, and the refresh reads the latest state.
				select {
				case h.out <- struct{}{}:
				default:
				}
			case <-quit:
				return
			}
		}
	}()
}

// remove unsubscribes and stops the forwarding goroutine.
func (h *wakeHub) remove(ch <-chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if q, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(q)
	}
}

// lanIP returns the machine's primary LAN IPv4 address (via the route to a
// public endpoint; no packets are sent). Empty string on failure.
func lanIP() string {
	conn, err := net.Dial("udp4", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return ""
	}
	return addr.IP.String()
}

// New creates a Mirror. addr is the web bind address; selectors are the
// candidate pane root selectors (tried in order); window is a title
// substring used to pick the VS Code window (empty = first window);
// storagePath/codePath back the workspaces page (list + launch);
// sshConfigPath backs the hook endpoint's remote-machine identification.
func New(disc *cdp.Discovery, log *slog.Logger, addr string, selectors []string, window, storagePath, codePath, sshConfigPath string) *Mirror {
	m := &Mirror{
		disc:          disc,
		log:           log,
		addr:          addr,
		selectors:     selectors,
		window:        window,
		storagePath:   storagePath,
		sshConfigPath: sshConfigPath,
		opener:        workspaces.NewOpener(codePath, log),
		fallback:      1 * time.Second,
		snaps:         make(map[string]*snapState),
		hub:           newWakeHub(),
		refreshNow:    make(chan struct{}, 1),
		wsRefresh:     make(chan struct{}, 1),
		toggleTimes:   make(map[string]time.Time),
		upgrader: websocket.Upgrader{
			// Local-only tool; accept any origin (127.0.0.1 / localhost).
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
	// The workspace list is loaded once up front, then hot-reloaded via
	// fsnotify (like settings.json). A known-list change signals the
	// publish loop, which recomputes the CDP-overlaid list (the open
	// flags follow the live windows, not storage.json's lagging
	// windowsState) and pushes it to the workspaces-page clients. A
	// missing/unreadable storage.json is not fatal: the HTTP API falls
	// back to a fresh read.
	if st, err := workspaces.NewStore(storagePath, log, m.onWorkspacesChanged); err != nil {
		log.Warn("workspaces: initial list failed", "path", storagePath, "err", err)
	} else {
		m.wsStore = st
	}
	return m
}

// SetPlanner installs the armed power-off planner. It must be called
// before Run (the hook/shutdown endpoints answer 503 while it is nil).
func (m *Mirror) SetPlanner(p *shutdown.Planner) {
	m.planner = p
}

// Run starts the web server and the publish loop. It blocks until ctx is
// cancelled, then shuts everything down.
func (m *Mirror) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", m.handlePage)
	mux.HandleFunc("/ws", m.handleWS)
	mux.HandleFunc("/img/", m.handleImage)
	mux.HandleFunc("/workspaces", m.handleWorkspacesPage)
	mux.HandleFunc("/api/workspaces", m.handleWorkspaceList)
	mux.HandleFunc("/api/workspaces/open", m.handleWorkspaceOpen)
	mux.HandleFunc("/api/hook", m.handleHook)
	mux.HandleFunc("/api/shutdown", m.handleShutdownArm)
	srv := &http.Server{Addr: m.addr, Handler: mux}

	if m.wsStore != nil {
		if err := m.wsStore.Watch(ctx); err != nil {
			// Non-fatal: the list stays at its initial snapshot and the
			// HTTP API still serves it.
			m.log.Error("start workspaces watcher", "err", err)
		}
	}

	errCh := make(chan error, 1)
	go func() {
		m.log.Info("mirror web server listening", "addr", m.addr)
		if h, _, err := net.SplitHostPort(m.addr); err == nil && (h == "0.0.0.0" || h == "::" || h == "") {
			if ip := lanIP(); ip != "" {
				port := m.addr[strings.LastIndexByte(m.addr, ':')+1:]
				m.log.Info("mirror reachable from LAN at", "url", "http://"+ip+":"+port)
			}
		}
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	go m.publishLoop(ctx)
	go m.memReporter(ctx)

	select {
	case <-ctx.Done():
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	m.closeClients()
	return nil
}

func (m *Mirror) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(pageHTML))
}

// memReporter logs process memory and the image-cache size every 30s.
// Unbounded growth (e.g. the blob-URL image cache) shows up as a steady
// climb in heapMB / imgs.
func (m *Mirror) memReporter(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			m.log.Debug("mem", "heapMB", ms.HeapAlloc/1024/1024, "sysMB", ms.Sys/1024/1024, "numGC", ms.NumGC, "imgs", m.imgCount())
		}
	}
}

// hashStr returns a short hex digest used to detect content/CSS changes.
func hashStr(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:8])
}
