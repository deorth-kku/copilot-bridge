(() => {
  const extract = () => {
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
  }
  ;
  const push = () => {
    try { window.vscodeLoadLlama(JSON.stringify(extract())); } catch (e) {}
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
})()