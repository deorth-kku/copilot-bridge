(selectors) => {
  __FIND_ROOT__
  const el = findRoot(selectors);
  if (!el) return { err: 'pane not found' };
  const r = el.getBoundingClientRect();
__SCROLL_STATE____INPUT_EDITOR____CSS_FP____SCROLL_OBJ__
  const model = (el.querySelector('.model-picker-name') || { textContent: '' }).textContent;
  // Include a cheap theme indicator so a theme change (which does not change
  // text content) still triggers a full re-extract of themeVars.
  let themeInd = '';
  try { themeInd = getComputedStyle(el).getPropertyValue('--vscode-foreground').trim(); } catch (e) {}
  // The context views (popup menus) sit OUTSIDE the pane root, so the pane's
  // own content never changes when a popup opens, closes, or moves — fold a
  // cheap popup fingerprint into fp so any of those triggers a re-extract.
  // There can be several visible at once (the action-list menu plus a hover
  // tooltip); fingerprint each so any open/close/move triggers a re-extract.
  // Each part is prefixed with the container's stable id (its index in the
  // FULL list) so a shift in visible order is not mistaken for a content
  // change of the surviving popup.
  let popFP = '';
  try {
    let parts = [];
    const cvs = document.querySelectorAll('.context-view');
    for (let i = 0; i < cvs.length; i++) {
      const c = cvs[i];
      const cr = c.getBoundingClientRect();
      if (cr.width > 0 && cr.height > 0) {
        parts.push(i + ':' + c.innerText.length + ':' + c.childElementCount + ':' +
          Math.round(cr.left) + ':' + Math.round(cr.top));
      }
    }
    if (parts.length > 0) popFP = parts.join('|');
  } catch (e) {}
  // The modal editor (image carousel, Markdown preview, ...) sits OUTSIDE
  // the pane root, so the pane's own content never changes when it opens,
  // closes, or resizes — fold a cheap modal fingerprint into fp so any of
  // those triggers a re-extract.
  let modFP = '';
  try {
    const me = document.querySelector('.part.editor.modal-editor-part');
    if (me) {
      const mr = me.getBoundingClientRect();
      if (mr.width > 0 && mr.height > 0) {
        modFP = me.innerText.length + ':' + me.childElementCount + ':' +
          Math.round(mr.width) + ':' + Math.round(mr.height);
      }
    }
  } catch (e) {}
  // Chat-input cursor state, folded into the fp so caret moves and focus
  // changes trigger a full re-extract: neither alters innerText or the
  // child count (the cursor's position is an inline style, and focus is a
  // class + activeElement). The cursor's visibility is deliberately NOT
  // included: live toggles it every 500ms (the JS-driven blink), and it
  // would force a full extract twice a second.
  let curFP = '';
  try {
    if (inputEditor) {
      const cur = inputEditor.querySelector('.cursor');
      curFP = (cur ? (cur.style.top || '') + '|' + (cur.style.left || '') + '|' + (cur.style.height || '') : 'x') +
        '|' + inputEditor.querySelectorAll('.cursor').length +
        '|' + (inputFocused ? 1 : 0);
    }
  } catch (e) {}
  // The askQuestion custom-answer box is a plain <textarea> whose value is a
  // property, not text content: fold its value into the fp so typing in it
  // triggers a re-extract (the chat input's text is already in innerText, but
  // the freeform box's is not — without this the mirror's copy stays empty).
  let valFP = '';
  try {
    const qtas = el.querySelectorAll('.chat-question-freeform-textarea');
    for (const q of qtas) valFP += (q.value || '') + '\u0001';
  } catch (e) {}
  const fp = el.innerText.length + ':' + el.childElementCount + ':' + model.length + ':' + themeInd + ':' + popFP + ':' + curFP + ':' + inputFP + ':' + valFP + ':' + modFP;
  return {
    fp: fp,
    cssFP: cssFP,
    inputFocused: inputFocused,
    cursorChar: cursorChar,
    inputBreaks: inputBreaks,
    rect: { left: r.left, top: r.top, width: r.width, height: r.height },
    scroll: scroll,
    scrollPath: scrollPath,
    nestedScrolls: nestedScrolls,
    scrollRows: scrollRows,
  };
}