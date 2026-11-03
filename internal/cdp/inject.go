// Package cdp implements the Chrome DevTools Protocol side: target
// discovery, per-window sessions, and the page-side JS injection.
package cdp

import (
	_ "embed"
	"strconv"
	"strings"
	"time"
)

// BindingName is the CDP runtime binding global. It must differ from the
// Python POC's binding so the two tools can coexist, but only ONE
// instance of this tool should run at a time (bindings are page globals;
// a second instance would override the first one's binding).
const BindingName = "copilotBridge"

// ProbeLibJS is the page-global probe library (js/probe_lib.js): the
// shared helpers (findRoot, measureScroll, scrollObj, inputEditor, cssFP,
// extract) that the probe expressions and the observer IIFE reference.
// Session evaluates it before the observer IIFE and again after every
// navigation (a reload wipes the globals).
//
//go:embed js/probe_lib.js
var ProbeLibJS string

// defaultDebounceMs is the page-side DOM-change debounce used when the
// -debounce flag is not given.
const defaultDebounceMs = 50

// DefaultDebounce is the exported form of defaultDebounceMs for the CLI
// flag default.
const DefaultDebounce = time.Duration(defaultDebounceMs) * time.Millisecond

// clampDebounceMs keeps the injected timer value a positive integer so a
// nonsense flag value cannot produce invalid JS or a hot loop.
func clampDebounceMs(ms int) int {
	if ms < 1 {
		return 1
	}
	return ms
}

// injectJS is the injected IIFE as a template: __BINDING__ is spliced with
// BindingName and __DEBOUNCE_MS__ with the clamped debounce value.
//
//go:embed js/inject.js
var injectJS string

// InjectJS returns the script that installs a versioned MutationObserver
// debouncing (debounceMs) DOM changes in the whole document and pushing
// the state through window.<BindingName>. It always emits the current
// state at the end, so (re)connecting or reloading yields an immediate
// snapshot.
func InjectJS(debounceMs int) string {
	return strings.NewReplacer(
		"__BINDING__", BindingName,
		"__DEBOUNCE_MS__", strconv.Itoa(clampDebounceMs(debounceMs)),
	).Replace(injectJS)
}

// MirrorBindingName is the CDP runtime binding global the mirror's change
// observer pushes through. A second binding (not an extended payload of
// BindingName) keeps the two pipelines independent: the chat-state push is
// deduped by payload in the session, which would swallow DOM changes that
// don't alter the chat state.
const MirrorBindingName = "copilotBridgeMirror"

// mirrorInjectJS is the mirror IIFE as a template: __BINDING__ is spliced
// with MirrorBindingName and __DEBOUNCE_MS__ with the clamped debounce
// value.
//
//go:embed js/mirror_inject.js
var mirrorInjectJS string

// MirrorInjectJS installs a versioned observer that wakes the mirror
// (via window.<MirrorBindingName>) whenever the page changes in a way the
// mirror's fingerprint can see:
//   - DOM mutations anywhere (childList / characterData / any attribute —
//     no attributeFilter, so class/style/width changes are covered too);
//   - scrolling in ANY scrollable descendant: scroll events don't bubble,
//     but the capture phase at the document root sees them all, so one
//     listener covers every container the mirror might measure;
//   - window resize (changes the pane's bounding box).
//
// Changes are debounced (debounceMs) so a streaming chat produces at most
// ~1000/debounceMs wakes/second. It always emits once at the end, so
// (re)connecting yields an immediate probe.
func MirrorInjectJS(debounceMs int) string {
	return strings.NewReplacer(
		"__BINDING__", MirrorBindingName,
		"__DEBOUNCE_MS__", strconv.Itoa(clampDebounceMs(debounceMs)),
	).Replace(mirrorInjectJS)
}
