(() => {
  const extract = __EXTRACT_FN__
  ;
  const push = () => {
    try { window.__BINDING__(JSON.stringify(extract())); } catch (e) {}
  };

  // versioned install: replaces any stale observer from older versions
  const VERSION = 1;
  if (window.__copilotBridgeVersion !== VERSION) {
    if (window.__copilotBridgeMO) { try { window.__copilotBridgeMO.disconnect(); } catch (e) {} }
    window.__copilotBridgeVersion = VERSION;
    let timer = null;
    window.__copilotBridgeSchedule = () => {
      if (timer) return;
      timer = setTimeout(() => { timer = null; push(); }, __DEBOUNCE_MS__);
    };
    window.__copilotBridgeMO = new MutationObserver(window.__copilotBridgeSchedule);
    window.__copilotBridgeMO.observe(document.documentElement, {
      subtree: true, childList: true, characterData: true,
      attributes: true, attributeFilter: ['aria-label']
    });
  }

  // always emit current state (re-attach / re-inject after reload)
  (window.__copilotBridgeSchedule || push)();
  return 'ok';
})()