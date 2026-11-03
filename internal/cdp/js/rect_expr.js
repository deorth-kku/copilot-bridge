(args) => {
  const P = window.__copilotBridgeProbes;
  const sels = args[0], path = args[1];
  const root = P.findRoot(sels);
  if (!root) return null;
  let el = root;
  if (Array.isArray(path) && path.length) {
    for (const i of path) {
      if (!el.children[i]) return null;
      el = el.children[i];
    }
  }
  const r = el.getBoundingClientRect();
  return { ok: true, left: r.left, top: r.top, width: r.width, height: r.height };
}