
  let inputEditor = null, inputFocused = false, cursorChar = -1, inputBreaks = null, inputFP = '';
  try {
    inputEditor = el.querySelector('.chat-input-container .interactive-input-editor .monaco-editor')
                 || el.querySelector('.interactive-input-editor .monaco-editor');
    if (inputEditor) {
      // Walk up from the active element (contains() is not spliced in).
      let n = document.activeElement;
      while (n) { if (n === inputEditor) { inputFocused = true; break; } n = n.parentElement; }
      const cur = inputEditor.querySelector('.cursor');
      const vlines = inputEditor.querySelector('.view-lines');
      let lines = null;
      if (vlines) {
        lines = vlines.querySelectorAll('.view-line');
        // inputFP: the editor width plus each line's character count. A
        // live re-wrap (window resize) changes these WITHOUT changing the
        // pane's innerText, yet it changes the break structure below, so
        // both must be part of the fingerprint.
        try {
          inputFP = Math.round(inputEditor.getBoundingClientRect().width) + ':';
          for (let i = 0; i < lines.length; i++) {
            inputFP += lines[i].textContent.length;
            if (i < lines.length - 1) inputFP += ',';
          }
        } catch (e) {}
        // inputBreaks: one entry per boundary between consecutive view
        // lines; true = a hard newline (a text-line end), false = a soft
        // wrap. The mirror re-groups the live view lines across the soft
        // boundaries so the text re-flows continuously at the mirror
        // width. A hard line can never exceed the live wrap width, and a
        // wrapped line's non-final segments always reach it, so a line
        // whose text falls short of the widest line by more than one
        // character was not wrapped: the boundary after it is hard. A
        // hard line ending exactly at the wrap column is indistinguishable
        // from a wrapped segment in the DOM (the rare miss).
        if (lines.length > 1) {
          const widths = [];
          let maxW = 0, wi = 0;
          for (let i = 0; i < lines.length; i++) {
            const range = document.createRange();
            range.selectNodeContents(lines[i]);
            const rr = range.getBoundingClientRect();
            widths.push(rr.width);
            if (rr.width > maxW) { maxW = rr.width; wi = i; }
          }
          if (maxW > 0) {
            // One character's width, from the widest line's first
            // character (exact for the chat input's monospace font).
            let wmax = 0;
            let fn = null;
            (function w(node) {
              if (fn) return;
              if (node.nodeType === 3) { if (node.textContent.length) { fn = node; return; } return; }
              for (let i = 0; i < node.childNodes.length; i++) w(node.childNodes[i]);
            })(lines[wi]);
            if (fn) {
              const cr = document.createRange();
              cr.setStart(fn, 0);
              cr.setEnd(fn, 1);
              wmax = cr.getBoundingClientRect().width;
            }
            const thr = maxW - wmax;
            inputBreaks = [];
            for (let i = 0; i < lines.length - 1; i++) inputBreaks.push(widths[i] <= thr);
          }
        }
      }
      if (cur && vlines && lines) {
        if (lines.length) {
          const cTop = parseFloat(cur.style.top) || 0;
          const cLeft = parseFloat(cur.style.left) || 0;
          // Line metrics from the first view-line: its inline top is the
          // content top padding, its inline height the line height.
          const f = lines[0];
          const padTop = parseFloat(f.style.top) || 0;
          const lineH = parseFloat(f.style.height) || 20;
          let li = Math.round((cTop - padTop) / lineH);
          if (li < 0) li = 0;
          if (li > lines.length) li = lines.length;
          let idx = 0;
          for (let i = 0; i < li; i++) idx += lines[i].textContent.length;
          if (li < lines.length) {
            // Characters of the cursor's own line before the caret: each
            // direct child span is one text run (absolute left + width in
            // the live layout). A caret inside a run is located by its
            // per-character Range boundaries, NOT proportionally: plain
            // input text is one span per line, and CJK glyphs fall back
            // to a ~2x-wide font, so a uniform-width (proportional)
            // estimate skews the index on mixed CJK/Latin text.
            const line = lines[li];
            const lr = line.getBoundingClientRect();
            for (const s of line.children) {
              if (s.nodeType !== 1) continue;
              const t = s.textContent;
              if (!t.length) continue;
              const sr = s.getBoundingClientRect();
              if (!sr.width) continue;
              const sl = sr.left - lr.left;
              if (cLeft >= sl + sr.width) idx += t.length;
              else if (cLeft > sl) {
                // The caret is inside this run. leftOf(i) is the left edge
                // of character i (i == length: the run's right edge); the
                // boundaries are monotonic, so binary-search the largest
                // one at/before cLeft and compare it with its successor.
                const tn = (function findText(node) {
                  if (node.nodeType === 3) return node;
                  for (let i = 0; i < node.childNodes.length; i++) {
                    const r = findText(node.childNodes[i]);
                    if (r) return r;
                  }
                  return null;
                })(s);
                if (tn) {
                  const txt = tn.textContent;
                  // Line-relative: the Range rect is in viewport coordinates,
                  // but cLeft (the cursor's inline left) is relative to the
                  // editor's content origin, i.e. the line's left edge.
                  const leftOf = (i) => {
                    if (i >= txt.length) return sl + sr.width;
                    const cr = document.createRange();
                    cr.setStart(tn, i);
                    cr.setEnd(tn, i + 1);
                    return cr.getBoundingClientRect().left - lr.left;
                  };
                  let lo = 0, hi = txt.length;
                  while (lo < hi) {
                    const mid = (lo + hi + 1) >> 1;
                    if (leftOf(mid) <= cLeft + 0.5) lo = mid;
                    else hi = mid - 1;
                  }
                  let k = lo;
                  if (lo < txt.length &&
                      Math.abs(leftOf(lo + 1) - cLeft) < Math.abs(leftOf(lo) - cLeft)) k = lo + 1;
                  idx += k;
                }
              }
              else break;
            }
          }
          cursorChar = idx;
        }
      }
    }
  } catch (e) {}
