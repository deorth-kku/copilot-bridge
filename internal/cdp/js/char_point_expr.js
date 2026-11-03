(args) => {
  const sels = args[0], char = args[1];
  __FIND_ROOT__
  const root = findRoot(sels);
  if (!root) return null;
  const ed = root.querySelector('.chat-input-container .interactive-input-editor .monaco-editor')
             || root.querySelector('.interactive-input-editor .monaco-editor');
  if (!ed) return null;
  const er = ed.getBoundingClientRect();
  const vlines = ed.querySelector('.view-lines');
  if (!vlines) return null;
  const nodes = [];
  (function w(n) { for (let i = 0; i < n.childNodes.length; i++) { const c = n.childNodes[i]; if (c.nodeType === 3) nodes.push(c); else if (c.nodeType === 1) w(c); } })(vlines);
  if (!nodes.length) {
    // Empty input: the first line's start (content top padding).
    const f = vlines.querySelector('.view-line');
    const padTop = f ? (parseFloat(f.style.top) || 0) : 0;
    const lineH = f ? (parseFloat(f.style.height) || 20) : 20;
    return { x: er.left + 4, y: er.top + padTop + lineH / 2 };
  }
  let total = 0;
  for (const n of nodes) total += n.length;
  if (char < 0) char = 0;
  if (char > total) char = total;
  // The caret sits at boundary char: click the left edge of the first
  // character for char 0, otherwise the right edge of character char-1
  // (the boundary between char-1 and char).
  let ci = char - 1;
  if (ci < 0) ci = 0;
  if (ci >= total) ci = total - 1;
  let acc = 0, tn = null, off = 0;
  for (const n of nodes) {
    const l = n.length;
    if (ci >= acc && ci < acc + l) { tn = n; off = ci - acc; break; }
    acc += l;
  }
  if (!tn) return null;
  const range = document.createRange();
  range.setStart(tn, off);
  range.setEnd(tn, off + 1);
  const r = range.getBoundingClientRect();
  if (!r.width && !r.height) return null;
  return { x: (char === 0) ? r.left : r.right, y: r.top + r.height / 2 };
}