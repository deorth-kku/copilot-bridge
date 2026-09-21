module.exports = (args) => {
  const sels = args[0], path = args[1];
  
  const findRoot = (sels) => {
    for (const sel of sels) {
      try { const e = document.querySelector(sel); if (e) return e; } catch (err) {}
    }
    return null;
  };

  const root = findRoot(sels);
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
};
