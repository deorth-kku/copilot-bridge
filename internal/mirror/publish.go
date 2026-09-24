package mirror

import (
	"context"
	"slices"
	"strings"
	"time"

	"copilot-bridge/internal/cdp"
)

// publishLoop keeps every watched window in sync with its live page.
// Clients are grouped by the window each one mirrors (its own status-bar
// selection, falling back to the -window flag or the first window); each
// distinct window is refreshed once per cycle and its state is sent only to
// the clients watching it, so several browser tabs can mirror different
// VS Code windows at once.
//
// The loop is event-driven: the page's injected observer (cdp.MirrorInjectJS)
// pushes a wake through the session's CDP binding when the DOM, scroll
// position, or window size changes, and the hub fans every watched
// session's wake into one channel. A slow fallback ticker covers what the
// observer cannot see (CSSOM-only edits, dropped wakes) and re-picks the
// session after a window switch or reload.
func (m *Mirror) publishLoop(ctx context.Context) {
	ticker := time.NewTicker(m.fallback)
	defer ticker.Stop()
	// pageHealth samples each live page's DOM/JS-heap size once a minute so
	// the log can correlate refresh latency with renderer growth over long
	// sessions (the suspected cause of the slow-mirror-over-time symptom).
	healthTicker := time.NewTicker(60 * time.Second)
	defer healthTicker.Stop()
	// subs tracks which sessions the hub is subscribed to. Only the
	// publish-loop goroutine touches it, so no lock is needed.
	subs := make(map[*cdp.Session]struct{})
	for {
		wins := m.disc.Windows()
		groups := m.groupClients(wins)
		// Subscribe the hub to exactly the sessions of the watched windows
		// (the default window is always watched, even with no clients).
		watched := make(map[*cdp.Session]struct{}, len(groups))
		for winID := range groups {
			if s := m.disc.SessionForID(winID); s != nil {
				watched[s] = struct{}{}
			}
		}
		for s := range subs {
			if _, ok := watched[s]; !ok {
				m.hub.remove(s.MirrorWake())
				delete(subs, s)
			}
		}
		for s := range watched {
			if _, ok := subs[s]; !ok {
				m.hub.add(s.MirrorWake())
				subs[s] = struct{}{}
			}
		}
		select {
		case <-ctx.Done():
			// Stop the hub's forwarding goroutines for the remaining
			// subscriptions (mirrorWake is never closed, so they would
			// otherwise leak past shutdown).
			for s := range subs {
				m.hub.remove(s.MirrorWake())
			}
			return
		case <-m.hub.out:
			m.log.Debug("mirror: refresh (wake)")
			m.refreshAll()
		case <-m.refreshNow:
			m.log.Debug("mirror: refresh (client event)")
			m.refreshAll()
		case <-m.wsRefresh:
			// The known workspace list changed (storage.json reload);
			// recompute the CDP-overlaid list for the workspaces page.
			m.log.Debug("mirror: workspaces refresh (known list changed)")
			m.refreshWorkspaces()
		case <-m.disc.WindowsChanged():
			// A window opened or closed: the open-workspace set (the
			// workspaces page's green dots) may have changed.
			if m.wsPageClients() > 0 {
				m.log.Debug("mirror: workspaces refresh (window set changed)")
				m.refreshWorkspaces()
			}
		case <-ticker.C:
			m.log.Debug("mirror: refresh (fallback)")
			m.refreshAll()
			// Re-probe the live windows: a window can change its folder
			// (File > Open Folder) without the window set changing, and a
			// just-opened window's CDP session may not have been dialled
			// when the window-set signal fired.
			if m.wsPageClients() > 0 {
				m.refreshWorkspaces()
			}
		case <-healthTicker.C:
			for winID := range groups {
				s := m.disc.SessionForID(winID)
				if s == nil {
					continue
				}
				hStart := time.Now()
				h, err := cdp.Health(s, m.selectors)
				if err != nil {
					m.log.Debug("page health probe failed", "window", winID, "err", err)
					continue
				}
				heapMB := 0.0
				if h.Heap != nil {
					heapMB = h.Heap.Used / 1024 / 1024
				}
				paneNodes := 0
				if h.PaneNodes != nil {
					paneNodes = *h.PaneNodes
				}
				m.log.Debug("page health", "window", winID, "ms", time.Since(hStart).Milliseconds(),
					"nodes", h.Nodes, "paneNodes", paneNodes, "sheets", h.Sheets,
					"heapMB", float64(int(heapMB*10))/10)
			}
		}
	}
}

// refreshAll refreshes every watched window once and sends each client the
// state of the window it is mirroring. The groups are resolved fresh here:
// a client may have connected or changed its picker selection while the
// publish loop was waiting.
func (m *Mirror) refreshAll() {
	wins := m.disc.Windows()
	groups := m.groupClients(wins)
	// Drop snapshots (and auto-open bookkeeping) of windows that closed
	// since the last cycle so the maps cannot grow unbounded.
	if len(wins) > 0 {
		m.snapMu.Lock()
		for id := range m.snaps {
			if !windowIn(wins, id) {
				delete(m.snaps, id)
			}
		}
		m.snapMu.Unlock()
		for id := range m.toggleTimes {
			if !windowIn(wins, id) {
				delete(m.toggleTimes, id)
			}
		}
	}
	for winID, group := range groups {
		m.refreshWindow(winID, wins, group)
	}
}

// refreshWindow probes one window's pane (cheap fingerprint) and, when
// something changed, does a full extract and sends state messages to the
// clients watching that window in two phases: phase 1 carries the pane HTML
// and the popup (styled by the cached CSS) so a popup becomes visible
// without waiting for a CSS re-extract; phase 2 follows with the re-extracted
// CSS when its fingerprint changed (opening a context view makes VS Code add
// an inline <style>, which flips the fingerprint on the same refresh).
func (m *Mirror) refreshWindow(winID string, wins []cdp.Window, group []*client) {
	totalStart := time.Now()
	s := m.disc.SessionForID(winID)
	if s == nil {
		m.sendErrToGroup(group, "waiting for VS Code window (is it running with the CDP port open?)")
		return
	}
	fpStart := time.Now()
	fpst, err := cdp.Fingerprint(s, m.selectors)
	fpMs := time.Since(fpStart).Milliseconds()
	if err != nil {
		m.sendErrToGroup(group, err.Error())
		return
	}
	if fpst.Err != "" {
		// A missing pane is usually a closed chat view: click the window's
		// title-bar "Toggle Chat" button to re-open it (throttled).
		if fpst.Err == "pane not found" {
			m.autoOpenChat(s, winID)
		}
		m.sendErrToGroup(group, fpst.Err)
		return
	}

	m.snapMu.Lock()
	prev := m.snaps[winID]
	rectChanged := prev == nil || prev.rect != fpst.Rect
	scrollChanged := prev == nil || prev.scroll != fpst.Scroll || !slices.Equal(prev.scrollPath, fpst.ScrollPath) ||
		!slices.Equal(prev.scrollRows, fpst.ScrollRows)
	fpChanged := prev == nil || prev.fp != fpst.FP
	cssFPChanged := prev == nil || prev.cssFP != fpst.CSSFP
	// Nested scrollers (reasoning-trace lists) scroll without any DOM change:
	// while the trace streams, VS Code pins the list's scrollTop to the
	// bottom on every content chunk, and the user can scroll a finished box.
	// Neither changes the content fingerprint, so compare them explicitly.
	nestedChanged := prev == nil || !equalNested(prev.nestedScrolls, fpst.NestedScrolls)
	var prevCSS, prevCSSVer, prevThemeVer string
	if prev != nil {
		prevCSS, prevCSSVer, prevThemeVer = prev.css, prev.cssVer, prev.themeVer
	}
	m.snapMu.Unlock()

	// A client that has not yet rendered this window (just connected, or
	// just switched to it via the picker) needs a full HTML/CSS send even
	// when nothing changed in this window since the last refresh —
	// otherwise its pane would keep showing the previous window until the
	// new one happens to change.
	anyNeedsFull := false
	for _, c := range group {
		if p := c.lastWin.Load(); p == nil || *p != winID {
			anyNeedsFull = true
			break
		}
	}

	if !(rectChanged || scrollChanged || fpChanged || cssFPChanged || nestedChanged || anyNeedsFull) {
		m.log.Debug("mirror: refresh", "window", winID, "totalMs", time.Since(totalStart).Milliseconds(), "fpMs", fpMs, "changed", "-")
		return
	}
	var changedParts []string
	if rectChanged {
		changedParts = append(changedParts, "rect")
	}
	if scrollChanged {
		changedParts = append(changedParts, "scroll")
	}
	if fpChanged {
		changedParts = append(changedParts, "fp")
	}
	if cssFPChanged {
		changedParts = append(changedParts, "css")
	}
	if nestedChanged {
		changedParts = append(changedParts, "nested")
	}
	if anyNeedsFull {
		changedParts = append(changedParts, "full")
	}

	// Full HTML extract only when the content fingerprint changed.
	var html, rootStyle, themeVars, themeBg string
	var newPopup *cdp.PopupState
	var htmlMs int64
	// The nested-scroll state shipped with a message must be measured at the
	// SAME instant as the HTML it accompanies. The fingerprint (taken first,
	// to decide whether to extract) can predate a content chunk that the
	// extract (taken second) already includes; shipping the fingerprint's
	// nestedScrolls with the newer HTML would leave the mirror's nested list
	// (the reasoning-trace / streaming spinner) one chunk behind its own
	// content, clipping the spinner at the box bottom until the next message.
	nestedScrolls := fpst.NestedScrolls
	if fpChanged {
		exStart := time.Now()
		hs, err := cdp.ExtractHTML(s, m.selectors)
		if err != nil {
			m.sendErrToGroup(group, err.Error())
			return
		}
		if hs.Err != "" {
			m.sendErrToGroup(group, hs.Err)
			return
		}
		html, rootStyle, themeVars, themeBg = hs.HTML, hs.RootStyle, hs.ThemeVars, hs.ThemeBg
		if hs.NestedScrolls != nil {
			nestedScrolls = hs.NestedScrolls
		}
		if hs.Popup != nil {
			newPopup = hs.Popup
			// Popups can carry images too (e.g. the attached-image hover
			// preview renders its full-size blob img inside the hover's
			// .context-view); rewrite them just like the pane's.
			newPopup.HTML = m.rewriteImages(s, newPopup.HTML)
		}
		html = m.rewriteImages(s, html)
		htmlMs = time.Since(exStart).Milliseconds()
	} else if prev != nil {
		html, rootStyle, themeVars, themeBg = prev.html, prev.rootStyle, prev.themeVars, prev.themeBg
	}
	themeVer := hashStr(themeVars)
	sendTheme := themeVars != "" && themeVer != prevThemeVer

	// The CSS re-extract (with inlined fonts) is deferred to a follow-up
	// phase (phase 2, below) when the CSS fingerprint changed: opening a
	// context view makes VS Code add an inline <style> element, which flips
	// the CSS fingerprint on exactly the refresh that opens the popup.
	// Extracting (and re-sending ~4MB of) CSS before the popup would make
	// the mirror show the menu only after the live open animation has
	// finished. Until phase 2 lands, the cached copy is authoritative.
	css, cssVer := prevCSS, prevCSSVer
	var cssMs int64

	ns := &snapState{
		html: html, css: css, cssVer: cssVer, cssFP: fpst.CSSFP,
		rootStyle: rootStyle, themeVars: themeVars, themeVer: themeVer, themeBg: themeBg,
		rect: fpst.Rect, scroll: fpst.Scroll, scrollPath: fpst.ScrollPath, scrollRows: fpst.ScrollRows,
		nestedScrolls: nestedScrolls,
		window:        s.Title(), windowID: winID, fp: fpst.FP, inputFocused: fpst.InputFocused,
		cursorChar: fpst.CursorChar, inputBreaks: fpst.InputBreaks,
	}
	if newPopup != nil {
		ns.popupHTML = newPopup.HTML
		ns.popupFP = hashStr(newPopup.HTML)
		ns.popupLeft = newPopup.Left
		ns.popupTop = newPopup.Top
		ns.popupW = newPopup.Width
		ns.popupH = newPopup.Height
	}
	m.snapMu.Lock()
	m.snaps[winID] = ns
	m.snapMu.Unlock()

	base := stateMsg{
		Type: "state", CSSVer: cssVer, RootStyle: rootStyle, ThemeVer: themeVer,
		NestedScrolls: nestedScrolls, InputFocused: fpst.InputFocused,
		CursorChar: fpst.CursorChar, InputBreaks: fpst.InputBreaks,
		Rect: fpst.Rect, Scroll: fpst.Scroll, ScrollPath: fpst.ScrollPath, ScrollRows: fpst.ScrollRows,
		Window: s.Title(), WindowID: winID, Windows: wins,
	}
	if fpChanged {
		base.HTML = html
	}
	// The popup is part of the content fingerprint, so it only changes when
	// fpChanged. Always advertise its position while it is open (the browser
	// keeps its DOM and just re-positions); re-send the HTML only when it
	// actually changed.
	if ns.popupFP != "" {
		base.Popup = &cdp.PopupState{Left: ns.popupLeft, Top: ns.popupTop, Width: ns.popupW, Height: ns.popupH}
		if prev == nil || prev.popupFP != ns.popupFP {
			base.Popup.HTML = ns.popupHTML
		}
	}
	if sendTheme {
		base.ThemeVars = themeVars
		base.ThemeBg = themeBg
	}
	// The popup's anchor depends on each client's OWN last-clicked control
	// (several tabs can mirror this window at once), so resolve it per
	// client on every refresh while a popup is open: one cheap eval per
	// client, only while visible. Marshal a copy of the message per client
	// — never mutate the shared Popup pointer.
	marshalStart := time.Now()
	// Phase 1: the pane HTML and the popup, styled by the CACHED css. This is
	// what makes the popup visible in the mirror, so it must not wait for a
	// CSS re-extract. A client that has not rendered this window yet (just
	// connected, or just switched via the picker) gets the cached CSS here
	// and the fresh extract in phase 2.
	phase1Ms := int64(0)
	if fpChanged || rectChanged || scrollChanged || nestedChanged || anyNeedsFull {
		for _, c := range group {
			msg := base
			if p := c.lastWin.Load(); p == nil || *p != winID {
				// First state of this window for this client: force a full
				// send (HTML + cached CSS + theme) so the pane re-renders
				// even though the window has not changed since the last
				// refresh.
				msg.HTML = html
				msg.CSS = prevCSS
				msg.ThemeVars = themeVars
				msg.ThemeBg = themeBg
				c.lastWin.Store(&winID)
			}
			if ns.popupFP != "" && msg.Popup != nil {
				m.withPopupAnchor(&msg, c, s, fpst.Rect)
			}
			c.sendTo(msg)
		}
		phase1Ms = time.Since(totalStart).Milliseconds()
	}

	// Phase 2: the CSS re-extract (only when the fingerprint changed), sent
	// as a follow-up so phase 1 was not delayed. The follow-up carries the
	// popup position WITHOUT html: a null popup would make the browser drop
	// the menu that phase 1 just rendered. The group is re-resolved because
	// a client may have connected during the extract.
	if cssFPChanged {
		csStart := time.Now()
		if cs, err := cdp.ExtractCSS(s); err == nil {
			css, cssVer = cs.CSS, hashStr(cs.CSS)
			m.snapMu.Lock()
			if cur := m.snaps[winID]; cur == ns {
				ns.css = css
				ns.cssVer = cssVer
			}
			m.snapMu.Unlock()
			// Carry the input state too: CursorChar has no omitempty (0 is a
			// valid caret position), so a follow without it would reset the
			// mirror's caret to the start of the input, and a missing
			// InputFocused would drop the blink class.
			follow := stateMsg{
				Type: "state", CSS: css, CSSVer: cssVer,
				InputFocused: fpst.InputFocused, CursorChar: fpst.CursorChar, InputBreaks: fpst.InputBreaks,
				Rect: fpst.Rect, Scroll: fpst.Scroll, ScrollPath: fpst.ScrollPath, ScrollRows: fpst.ScrollRows,
				Window: s.Title(), WindowID: winID, Windows: wins,
			}
			if ns.popupFP != "" {
				follow.Popup = &cdp.PopupState{Left: ns.popupLeft, Top: ns.popupTop, Width: ns.popupW, Height: ns.popupH}
			}
			for _, c := range m.groupClients(m.disc.Windows())[winID] {
				fm := follow
				if fm.Popup != nil {
					m.withPopupAnchor(&fm, c, s, fpst.Rect)
				}
				c.sendTo(fm)
			}
		} else {
			m.log.Debug("css extract failed", "err", err)
		}
		cssMs = time.Since(csStart).Milliseconds()
	}
	m.log.Debug("mirror: refresh",
		"window", winID,
		"totalMs", time.Since(totalStart).Milliseconds(),
		"fpMs", fpMs, "htmlMs", htmlMs, "cssMs", cssMs,
		"phase1Ms", phase1Ms,
		"marshalMs", time.Since(marshalStart).Milliseconds(),
		"changed", strings.Join(changedParts, ","), "clients", len(group),
		"htmlBytes", len(html), "cssBytes", len(css))
}

// withPopupAnchor resolves the client's last-clicked control to a live rect
// and attaches it as the popup's anchor, mutating the (per-client COPY of
// the) message in place. The mirror applies the live (popup - anchor)
// offset to its own copy of the control, which is size-independent. No-op
// when the message carries no popup or the client has no anchor yet.
func (m *Mirror) withPopupAnchor(msg *stateMsg, c *client, s *cdp.Session, paneRect cdp.PaneRect) {
	if msg.Popup == nil {
		return
	}
	ap := c.lastAnchor.Load()
	if ap == nil {
		return
	}
	ar, ok := cdp.EvalRect(s, m.selectors, *ap)
	if !ok {
		return
	}
	p := *msg.Popup
	p.Anchor = &cdp.PaneRect{
		Left: ar.Left - paneRect.Left, Top: ar.Top - paneRect.Top,
		Width: ar.Width, Height: ar.Height,
	}
	msg.Popup = &p
}

// windowIn reports whether id is in the (title-sorted) window set.
func windowIn(wins []cdp.Window, id string) bool {
	for _, w := range wins {
		if w.ID == id {
			return true
		}
	}
	return false
}

// sendErrToGroup sends an error state message to one group of clients.
// The live window set travels with the error so the browser's picker
// stays populated: a missing pane in the selected window must not hide
// the other windows from the picker.
func (m *Mirror) sendErrToGroup(group []*client, err string) {
	wins := m.disc.Windows()
	for _, c := range group {
		c.sendTo(stateMsg{Type: "state", Err: err, Windows: wins})
	}
}

// chatToggleCooldown bounds how often the mirror clicks the title-bar
// "Toggle Chat" button to re-open a missing chat pane. After a click the
// pane takes a moment to render, and windows without the button (or with a
// hidden title bar) must not be re-probed on every refresh.
const chatToggleCooldown = 5 * time.Second

// autoOpenChat re-opens a missing chat pane by clicking the window's
// title-bar "Toggle Chat" button (a real CDP input event at the button's
// center, like forwarded mirror clicks). Throttled per window.
func (m *Mirror) autoOpenChat(s *cdp.Session, winID string) {
	now := time.Now()
	if since := now.Sub(m.toggleTimes[winID]); since < chatToggleCooldown {
		return
	}
	x, y, ok := cdp.ToggleChatPoint(s)
	if !ok {
		m.toggleTimes[winID] = now
		m.log.Debug("auto-open: Toggle Chat button not found", "window", winID)
		return
	}
	m.toggleTimes[winID] = now
	m.log.Info("auto-open: clicking Toggle Chat", "window", winID, "x", x, "y", y)
	s.Send("Input.dispatchMouseEvent", map[string]any{
		"type": "mousePressed", "x": x, "y": y, "button": "left", "buttons": 1, "clickCount": 1,
	})
	s.Send("Input.dispatchMouseEvent", map[string]any{
		"type": "mouseReleased", "x": x, "y": y, "button": "left", "buttons": 0, "clickCount": 1,
	})
}

func equalNested(a, b []cdp.NestedScroll) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Top != b[i].Top || a[i].ScrollH != b[i].ScrollH || a[i].ClientH != b[i].ClientH ||
			!slices.Equal(a[i].Path, b[i].Path) {
			return false
		}
	}
	return true
}
