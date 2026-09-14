// Package cdp implements the Chrome DevTools Protocol side: target
// discovery, per-window sessions, and the page-side JS injection.
package cdp

// BindingName is the CDP runtime binding global. It must differ from the
// Python POC's binding so the two tools can coexist, but only ONE
// instance of this tool should run at a time (bindings are page globals;
// a second instance would override the first one's binding).
const BindingName = "vscodeLoadLlama"

// extractFn is an arrow function (NOT invoked) that reads the current
// chat input state. It must stay a function: InjectJS assigns it to a
// variable and calls it.
const extractFn = `() => {
    const q = sel => document.querySelector(sel);
    // Normalized boolean: a missing element must yield false (not null),
    // so the payload's *Visible fields are always JSON booleans.
    const vis = el => !!(el && (el.offsetWidth || el.offsetHeight));

    const inputEditor = q('.chat-input-container .interactive-input-editor .monaco-editor')
                     || q('.interactive-input-editor .monaco-editor');
    const inputText = inputEditor ? (inputEditor.innerText || '').replace(/\n+$/, '') : null;

    const modelEl = q('a.model-picker-name');
    // aria-label is the stable key; fall back to the visible text when the
    // attribute is ABSENT (null). The empty-string coalesce must come
    // AFTER the textContent fallback: (null || '') would swallow the null
    // and the fallback would never run.
    const model = modelEl
      ? ((modelEl.getAttribute('aria-label') || modelEl.textContent) || '').replace(/^Models,\s*/, '').trim()
      : null;
    const effortEl = q('.model-picker-config');
    const effort = effortEl
      ? ((effortEl.getAttribute('aria-label') || effortEl.textContent) || '').replace(/^Reasoning Effort:\s*/i, '').trim()
      : null;
    const modeEl = q('.chat-input-picker-item .chat-input-picker-label');
    const mode = modeEl ? modeEl.textContent.trim() : null;

    return {
      input: inputText,
      model: model,
      effort: effort,
      mode: mode,
      inputVisible: vis(inputEditor),
      modelVisible: vis(modelEl)
    };
  }`

// InjectJS installs a versioned MutationObserver that debounces (50ms)
// DOM changes in the whole document and pushes the state through
// window.<BindingName>. It always emits the current state at the end,
// so (re)connecting or reloading yields an immediate snapshot.
const InjectJS = `(() => {
  const extract = ` + extractFn + `
  ;
  const push = () => {
    try { window.` + BindingName + `(JSON.stringify(extract())); } catch (e) {}
  };

  // versioned install: replaces any stale observer from older versions
  const VERSION = 1;
  if (window.__loadLlamaVersion !== VERSION) {
    if (window.__loadLlamaMO) { try { window.__loadLlamaMO.disconnect(); } catch (e) {} }
    window.__loadLlamaVersion = VERSION;
    let timer = null;
    window.__loadLlamaSchedule = () => {
      if (timer) return;
      timer = setTimeout(() => { timer = null; push(); }, 50);
    };
    window.__loadLlamaMO = new MutationObserver(window.__loadLlamaSchedule);
    window.__loadLlamaMO.observe(document.documentElement, {
      subtree: true, childList: true, characterData: true,
      attributes: true, attributeFilter: ['aria-label']
    });
  }

  // always emit current state (re-attach / re-inject after reload)
  (window.__loadLlamaSchedule || push)();
  return 'ok';
})()`

// MirrorBindingName is the CDP runtime binding global the mirror's change
// observer pushes through. A second binding (not an extended payload of
// BindingName) keeps the two pipelines independent: the chat-state push is
// deduped by payload in the session, which would swallow DOM changes that
// don't alter the chat state.
const MirrorBindingName = "vscodeLoadLlamaMirror"

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
// Changes are debounced (50ms) so a streaming chat produces at most ~20
// wakes/second. It always emits once at the end, so (re)connecting yields
// an immediate probe.
const MirrorInjectJS = `(() => {
  const VERSION = 1;
  const wake = () => { try { window.` + MirrorBindingName + `('1'); } catch (e) {} };
  if (window.__mirrorVersion !== VERSION) {
    if (window.__mirrorMO) { try { window.__mirrorMO.disconnect(); } catch (e) {} }
    // Drop the previous install's scroll/resize listeners as well, so a
    // VERSION bump does not stack another pair on top of the old ones.
    if (window.__mirrorScrollH) document.removeEventListener('scroll', window.__mirrorScrollH, true);
    if (window.__mirrorResizeH) window.removeEventListener('resize', window.__mirrorResizeH);
    window.__mirrorVersion = VERSION;
    let timer = null;
    const schedule = () => {
      if (timer) return;
      timer = setTimeout(() => { timer = null; wake(); }, 50);
    };
    window.__mirrorSchedule = schedule;
    window.__mirrorMO = new MutationObserver(schedule);
    window.__mirrorMO.observe(document.documentElement, {
      subtree: true, childList: true, characterData: true, attributes: true,
    });
    window.__mirrorScrollH = schedule;
    document.addEventListener('scroll', schedule, true);
    window.__mirrorResizeH = schedule;
    window.addEventListener('resize', schedule);
  }
  (window.__mirrorSchedule || wake)();
  return 'ok';
})()`
