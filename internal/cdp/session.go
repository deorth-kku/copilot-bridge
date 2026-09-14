package cdp

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Event is a snapshot of one chat input state pushed from a window.
type Event struct {
	Window string
	Input  string
	Model  string
	Effort string
	Mode   string
}

type cdpMessage struct {
	ID     *int           `json:"id"`
	Method string         `json:"method"`
	Params jsontext.Value `json:"params"`
	Result jsontext.Value `json:"result"`
	Error  *cdpError      `json:"error"`
}

// cdpError is the "error" object of a CDP command response.
type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// callResult carries the outcome of one pending Call to its waiter.
type callResult struct {
	result jsontext.Value
	err    error
}

// callTimeout bounds how long a Call waits for its response.
const callTimeout = 15 * time.Second

type pageState struct {
	Input  *string `json:"input"`
	Model  *string `json:"model"`
	Effort *string `json:"effort"`
	Mode   *string `json:"mode"`
}

func strp(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Session is one page-level CDP connection per VS Code window. Each
// session owns its own WebSocket so Runtime.addBinding globals never
// clash between windows.
type Session struct {
	ID string
	// title is atomically updated by the discovery scan (VS Code can
	// change a window's title at any time) and read lock-free from the
	// session goroutine (Event.Window) and from log lines.
	title atomic.Pointer[string]
	WSURL string

	events chan Event
	log    *slog.Logger

	// mirrorWake carries "the page changed" signals from the mirror's
	// injected observer (Runtime.bindingCalled on MirrorBindingName).
	// Buffered by 1: a pending wake already guarantees a refresh, and the
	// refresh reads the latest state, so coalescing extra wakes is safe.
	mirrorWake chan struct{}
	// nav queues Page.frameNavigated re-injection requests from the read
	// loop to the Run goroutine. Buffered by 1: one pending request is
	// enough (re-injection is idempotent), extra navigations coalesce.
	nav chan struct{}

	mu     sync.Mutex
	conn   *websocket.Conn
	nextID int
	// pending maps a command id to the channel its Call is waiting on.
	pending map[int]chan callResult

	done atomic.Bool
	last string // dedupe: last payload JSON already emitted
}

func NewSession(id, title, wsURL string, events chan Event, log *slog.Logger) *Session {
	s := &Session{
		ID:         id,
		WSURL:      wsURL,
		events:     events,
		log:        log,
		mirrorWake: make(chan struct{}, 1),
		nav:        make(chan struct{}, 1),
		pending:    make(map[int]chan callResult),
	}
	s.title.Store(&title)
	return s
}

// Title returns the current window title.
func (s *Session) Title() string {
	if p := s.title.Load(); p != nil {
		return *p
	}
	return ""
}

// SetTitle atomically updates the window title (discovery scan).
func (s *Session) SetTitle(t string) { s.title.Store(&t) }

// MirrorWake returns the channel signalled (at most one pending) when the
// page's mirror observer reports a DOM/scroll/resize change. The mirror's
// publish loop selects on it to drive event-driven refreshes.
func (s *Session) MirrorWake() <-chan struct{} { return s.mirrorWake }

// IsDone reports whether the session's run loop has exited.
func (s *Session) IsDone() bool { return s.done.Load() }

// Stop closes the underlying WebSocket, unblocking the read loop.
func (s *Session) Stop() {
	s.mu.Lock()
	if s.conn != nil {
		s.conn.Close()
	}
	s.mu.Unlock()
}

// Run dials the page WebSocket and processes messages until the
// connection drops or the session is stopped. It never panics.
func (s *Session) Run(ctx context.Context) {
	defer s.done.Store(true)

	// gorilla does not send an Origin header, which is required
	// (Chrome rejects CDP WebSocket handshakes with an Origin).
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.Dial(s.WSURL, nil)
	if err != nil {
		s.log.Debug("dial failed", "window", s.Title(), "err", err)
		return
	}
	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()
	defer conn.Close()
	// In-flight Calls must not block forever once the socket is gone.
	defer s.failPending(errors.New("connection closed"))

	s.log.Info("attached window", "title", s.Title())
	s.send("Runtime.enable", nil)
	s.send("Page.enable", nil)

	// The read loop must run BEFORE the injection Calls below: Call waits
	// for a response that only the read loop can deliver. Run blocks until
	// the loop ends so IsDone() flips only when the connection is really
	// gone (the discovery scan relies on that to restart the session).
	// frameNavigated re-injection requests are queued on s.nav and served
	// by THIS goroutine, so injection pairs never interleave with each
	// other or with the startup pair.
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		// The CSS extraction (with inlined @font-face data URIs) can be
		// several megabytes, so the read limit must be well above the
		// default 1MB.
		conn.SetReadLimit(32 << 20)
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				s.log.Debug("read loop ended", "window", s.Title(), "err", err)
				return
			}
			s.handle(raw)
		}
	}()

	go func() {
		<-ctx.Done()
		s.Stop()
	}()

	s.inject(BindingName, InjectJS)
	s.inject(MirrorBindingName, MirrorInjectJS)

	// Serve re-injection requests until the connection drops.
	for {
		select {
		case <-readDone:
			return
		case <-s.nav:
			s.inject(BindingName, InjectJS)
			s.inject(MirrorBindingName, MirrorInjectJS)
		}
	}
}

// inject installs one runtime binding and evaluates its observer script,
// logging any failure. A failing evaluate (a syntax error, or a runtime
// exception during early navigation) must show up in the log instead of
// silently killing the pipeline. (Syntax and behavior of the injected JS
// are covered by internal/jscheck: node --check plus node:test suites, so
// a syntax error should be caught by `go test ./internal/...` before it
// ever reaches here.)
func (s *Session) inject(name, js string) {
	if _, err := s.Call("Runtime.addBinding", map[string]any{"name": name}); err != nil {
		s.log.Error("addBinding failed", "binding", name, "err", err)
		return
	}
	raw, err := s.Call("Runtime.evaluate", map[string]any{"expression": js, "returnByValue": true})
	if err != nil {
		s.log.Error("injection failed", "binding", name, "err", err)
		return
	}
	// A page-side exception is NOT a CDP error: it rides inside the result
	// as exceptionDetails.
	var r struct {
		Exception struct {
			Text string `json:"text"`
			Obj  struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &r); err == nil && (r.Exception.Obj.Description != "" || r.Exception.Text != "") {
		s.log.Error("injection threw", "binding", name, "err", r.Exception.Obj.Description)
	}
}

func (s *Session) handle(raw []byte) {
	var msgs []cdpMessage
	if len(raw) > 0 && raw[0] == '[' {
		if err := json.Unmarshal(raw, &msgs); err != nil {
			return
		}
	} else {
		var m cdpMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return
		}
		msgs = []cdpMessage{m}
	}
	for _, m := range msgs {
		if m.ID != nil {
			s.routeResponse(*m.ID, m.Result, m.Error)
			continue
		}
		switch m.Method {
		case "Runtime.bindingCalled":
			var p struct {
				Name    string `json:"name"`
				Payload string `json:"payload"` // CDP uses "payload", not "value"
			}
			if err := json.Unmarshal(m.Params, &p); err != nil {
				continue
			}
			if p.Name == MirrorBindingName {
				// Mirror wake: non-blocking, at most one pending (the
				// mirror's fingerprint probe reads the latest state, so
				// coalescing bursts is safe).
				select {
				case s.mirrorWake <- struct{}{}:
				default:
				}
				continue
			}
			if p.Name != BindingName || p.Payload == s.last {
				continue
			}
			s.last = p.Payload
			var st pageState
			if err := json.Unmarshal([]byte(p.Payload), &st); err != nil {
				continue
			}
			ev := Event{
				Window: s.Title(),
				Input:  strp(st.Input),
				Model:  strp(st.Model),
				Effort: strp(st.Effort),
				Mode:   strp(st.Mode),
			}
			select {
			case s.events <- ev:
			default:
				s.log.Warn("event channel full, dropping", "window", s.Title())
			}
		case "Page.frameNavigated":
			// workbench reload: both bindings are gone, reinstall. Queue the
			// request for the Run goroutine: inject uses Call, which waits
			// for responses the read loop is the only deliverer of, so it
			// must not run here, and it must not race other injections.
			s.log.Info("frame navigated, re-injecting", "window", s.Title())
			select {
			case s.nav <- struct{}{}:
			default:
			}
		}
	}
}

func (s *Session) send(method string, params any) {
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	m := map[string]any{"id": id, "method": method}
	if params != nil {
		m["params"] = params
	}
	b, _ := json.Marshal(m)
	if s.conn != nil {
		s.conn.WriteMessage(websocket.TextMessage, b)
	}
	s.mu.Unlock()
}

// Send fires a CDP command without waiting for its response.
func (s *Session) Send(method string, params any) { s.send(method, params) }

// Call sends a CDP command and waits for its matching response (or a
// timeout / connection drop). It returns the raw "result" value.
func (s *Session) Call(method string, params any) (jsontext.Value, error) {
	s.mu.Lock()
	if s.conn == nil {
		s.mu.Unlock()
		return nil, errors.New("not connected")
	}
	s.nextID++
	id := s.nextID
	ch := make(chan callResult, 1)
	s.pending[id] = ch
	m := map[string]any{"id": id, "method": method}
	if params != nil {
		m["params"] = params
	}
	b, err := json.Marshal(m)
	if err != nil {
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, err
	}
	werr := s.conn.WriteMessage(websocket.TextMessage, b)
	s.mu.Unlock()
	if werr != nil {
		return nil, werr
	}
	select {
	case r := <-ch:
		return r.result, r.err
	case <-time.After(callTimeout):
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, fmt.Errorf("CDP call timeout: %s", method)
	}
}

// routeResponse delivers a command response to the pending Call, if any.
func (s *Session) routeResponse(id int, result jsontext.Value, cdpErr *cdpError) {
	s.mu.Lock()
	ch, ok := s.pending[id]
	if ok {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	r := callResult{result: result}
	if cdpErr != nil {
		r.err = fmt.Errorf("CDP error %d: %s", cdpErr.Code, cdpErr.Message)
	}
	select {
	case ch <- r:
	default:
	}
}

// failPending unblocks every in-flight Call with the given error.
func (s *Session) failPending(err error) {
	s.mu.Lock()
	pending := s.pending
	s.pending = make(map[int]chan callResult)
	s.mu.Unlock()
	for _, ch := range pending {
		select {
		case ch <- callResult{err: err}:
		default:
		}
	}
}
