(args) => {
  const sels = args[0], path = args[1], rx = args[2], ry = args[3];
  __FIND_ROOT__
  let root = null;
  let p = path;
  // A path starting with a negative value is rooted OUTSIDE the pane root:
  // -2 is the .part.editor.modal-editor-part (the modal editor overlay,
  // e.g. the image carousel); any other negative value -(id+1) is the
  // .context-view (popup) container with that stable id — its index in the
  // FULL (including hidden) .context-view list. The remaining indices are
  // relative to the resolved root.
  if (Array.isArray(p) && p.length > 0 && p[0] < 0) {
    if (p[0] === -2) {
      const me = document.querySelector('.part.editor.modal-editor-part');
      if (me) {
        const mr = me.getBoundingClientRect();
        if (mr.width > 0 && mr.height > 0) { root = me; p = p.slice(1); }
      }
    } else {
      const id = -p[0] - 1; // .context-view index in the full list
      const c = document.querySelectorAll('.context-view')[id];
      if (c) {
        const cr = c.getBoundingClientRect();
        if (cr.width > 0 && cr.height > 0) { root = c; p = p.slice(1); }
      }
    }
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
}