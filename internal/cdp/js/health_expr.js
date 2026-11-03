(selectors) => {
  const P = window.__copilotBridgeProbes;
  let heap = null;
  try { if (performance.memory) heap = { used: performance.memory.usedJSHeapSize, total: performance.memory.totalJSHeapSize }; } catch (e) {}
  const el = P.findRoot(selectors);
  return {
    nodes: document.getElementsByTagName('*').length,
    paneNodes: el ? el.getElementsByTagName('*').length : null,
    sheets: document.styleSheets.length,
    heap: heap,
  };
}