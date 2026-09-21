module.exports = (args) => {
  const sels = args[0], path = args[1], rx = args[2], ry = args[3];
  
  const findRoot = (sels) => {
    for (const sel of sels) {
      try { const e = document.querySelector(sel); if (e) return e; } catch (err) {}
    }
    return null;
  };

  let root = null;
  let p = path;
  // A path starting with -1 is rooted at the live context view (popup)
  // instead of the pane root; the remaining indices are relative to it.
  // There can be several .context-view elements (the hidden menu one plus
  // hover popups such as the context-usage widget); use the visible one.
  if (Array.isArray(p) && p.length > 0 && p[0] === -1) {
    const cvs = document.querySelectorAll('.context-view');
    for (const c of cvs) {
      const cr = c.getBoundingClientRect();
      if (cr.width > 0 && cr.height > 0) root = c;
    }
    if (root) p = p.slice(1);
  } else {
    root = findRoot(sels);
  }
  if (!root) return null;
  let el = root;
  if (Array.isArray(p)) {
    for (const i of p) {
      if (!el.children[i]) { el = null; break; }
      el = el.children[i];
    }
  }
  if (!el) el = root;
  const r = el.getBoundingClientRect();
  return { x: r.left + rx * r.width, y: r.top + ry * r.height };
};
