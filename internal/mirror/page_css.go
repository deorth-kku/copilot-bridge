package mirror

// pageCSS is the mirror's own responsive/patch CSS, spliced into pageHTML.
const pageCSS = `  html, body {
    margin: 0 !important; padding: 0 !important;
    /* --mirror-bg is set by the theme message (the live pane root's
       effective background); #1e1e1e is the pre-theme fallback. */
    background: var(--mirror-bg, #1e1e1e) !important;
    overflow: auto !important; height: auto !important;
  }
  /* The status bar is a window picker: one option per live VS Code window,
     so several open windows can be switched between. The native select
     clips long titles on its own. */
  #status {
    position: fixed !important; top: 0 !important; left: 0 !important; right: 0 !important;
    height: 26px !important; z-index: 9999 !important;
    background: #323233 !important; color: #d7d7d7 !important;
    font: 12px monospace !important; padding: 0 8px !important;
    box-sizing: border-box !important; border: none !important;
    border-bottom: 1px solid #444 !important;
    cursor: pointer !important;
    /* Clicking the picker focuses the native select and Chromium paints its
       UA focus ring (orange on desktop) around the whole bar. The mirror
       needs no visible focus indicator here, so suppress it (same as #pane). */
    outline: none !important;
  }
  /* Responsive: the mirror fills the browser viewport (below the status bar)
     instead of the live pane's pixel size. The extracted subtree was laid out
     for the live pane, so re-fit it: the pane root fills #pane, the chat list
     (sized by an inline height in the live DOM) becomes a flex child filling
     the remaining space, and monaco-list rows (absolutely positioned with
     live-computed offsets/heights) reflow at the mirror's width. */
  #pane {
    position: relative; margin-top: 26px;
    width: 100%; height: calc(100vh - 26px);
    box-sizing: border-box; overflow: hidden;
  }
  /* Clicking the chat area (anything outside the chat input) moves the
     browser's focus to #pane itself (tabindex=0), and Chromium then paints
     its UA default focus ring (outline: auto) around the whole pane —
     VS Code's orange focusBorder color on desktop, white on mobile Chrome.
     Only the top edge is visible; the other edges sit at the viewport
     boundary and are clipped. Suppress the ring: the mirror needs no
     visible focus indicator. */
  #pane:focus, #pane:focus-visible { outline: none !important; }
  /* The popup (.context-view) is a sibling of the pane root inside #pane;
     it keeps its live pixel size (the re-fit rule must not stretch it). */
  #pane > :not(.context-view) { width: 100% !important; height: 100% !important; min-height: 0 !important; }
  /* VS Code adds full-viewport transparent overlays (.context-view-pointerBlock,
     .context-view-block) inside the context view for "click outside to close".
     They sit on top of the popup content, so a real click's hit-test returns the
     overlay instead of the menu row; the forwarded click is then computed from the
     overlay's full-viewport rect and lands at the wrong live position. Make them
     transparent to pointer events so clicks reach the actual menu rows. Clicks
     outside the popup still pass through to the pane and forward to live, which
     closes the live popup as before. */
  #pane .context-view .context-view-pointerBlock,
  #pane .context-view .context-view-block { pointer-events: none !important; }
  #pane .interactive-list { height: auto !important; flex: 1 1 0% !important; min-height: 0 !important; }
  /* The session-selection view has the same re-fit problem as the chat list,
     but its list container is .agent-sessions-control-container (NOT
     .interactive-list), so the rule above does not reach it: it keeps the
     live pane's inline list height, which is taller than the (shorter)
     mirror viewport. The flex chain then squashes the sibling input
     container (.chat-controls-container, flex-basis 0) to zero and the
     input renders below the visible area. Re-fit the chain: the list takes
     the remaining space, the input keeps its natural size. */
  #pane .agent-sessions-container { flex: 1 1 0% !important; min-height: 0 !important; }
  #pane .agent-sessions-container > .agent-sessions-control-container {
    display: flex !important; flex-direction: column !important;
    height: auto !important; flex: 1 1 0% !important; min-height: 0 !important;
  }
  #pane .agent-sessions-container .agent-sessions-viewer { display: flex !important; flex-direction: column !important; flex: 1 1 0% !important; min-height: 0 !important; }
  #pane .agent-sessions-container .agent-sessions-viewer > .monaco-list { height: auto !important; flex: 1 1 0% !important; min-height: 0 !important; }
  /* The controls container has no workbench rule (default flex 0 1 auto), so
     which behavior it needs depends on the active view, detected by the
     sibling .agent-sessions-container's inline style: in the CHAT view it is
     hidden (inline display: none) and the controls container must FILL the
     wrapper — its list is content-sized in the mirror (live keeps a fixed
     inline list height), so an auto-height container would hug a short
     history and leave the input floating above the bottom. In the SESSION
     view it keeps its natural (content) size so the sessions list can take
     the remaining space. */
  #pane .agent-sessions-container[style*="display: none"] ~ .chat-controls-container { flex: 1 1 0% !important; min-height: 0 !important; }
  #pane .agent-sessions-container:not([style*="display: none"]) ~ .chat-controls-container { flex: 0 1 auto !important; height: auto !important; min-height: 0 !important; }
  /* The welcome view ("Build with Agent" row) is collapsed by an inline
     height: 0px in the session view, where live clips its content (overflow
     hidden). In the mirror the input container is sized to its content, and
     the welcome view's ~24px content still contributes to that auto height
     (even when the container itself is 0), making the input area taller
     than live. Hide the content exactly when live collapsed the container;
     in the chat getting-started view live sets a real inline height and the
     content stays visible. */
  #pane .chat-welcome-view-container[style*="height: 0px"] .chat-welcome-view { display: none !important; }
  #pane .monaco-list-rows { position: static !important; transform: none !important; top: auto !important; left: auto !important; height: auto !important; overflow: visible !important; contain: none !important; }
  #pane .monaco-list-row { position: static !important; top: auto !important; }
  /* The live chat pins the LAST user message to the top of the list viewport
     (.monaco-tree-sticky-container, absolute at top:0 of the scroller). The
     mirror's scroller scrolls on its own, so replicating the pin fights the
     mirror's flow layout and causes artifacts; the message is already in the
     list, so the mirror drops the effect. */
  #pane .monaco-tree-sticky-container { display: none !important; }
  /* Rows OUTSIDE popups reflow to their content height (the responsive
     mirror width wraps text differently than the live pane). */
  #pane .monaco-list-row:not(.context-view .monaco-list-row) { height: auto !important; }
  /* Popup (.context-view) rows keep their FIXED live height and clip
     overflowing content, exactly like the live: some rows carry a 48px-tall
     .description span (sized for two-line items) that only shows one line,
     and the live row is fixed at 24px with overflow:hidden. Forcing
     height:auto (as the non-popup rule does) would stretch the mirror row to
     48px — the Local/permission menus looked doubled in height. */
  #pane .context-view .monaco-list-row { overflow: hidden !important; }
  /* Live separator rows carry margin: 4px 6px, but the live list positions
     rows by CUMULATIVE HEIGHT (absolute top = previous tops + heights),
     ignoring margins — so the row after a separator starts at
     separator-top + 8px, overlapping the separator's bottom margin by 4px.
     The mirror's flow layout instead adds the margins: margin-top pushes the
     line down 4px (correct, matches live) and margin-bottom pushes the next
     row down another 4px — leaving 8px more space under the line than live.
     Cancelling the bottom margin makes the separator contribute exactly its
     8px height to the flow, matching the live layout. */
  #pane .context-view .action-widget .monaco-list-row.separator { margin-bottom: -4px !important; }
  /* The live todo-list header renders the clear button INSIDE the title row
     (a flex item on the right). The mirror's DOM places the button container
     as a sibling of the title row under the block-level .todo-list-expand, so
     it wraps onto its own line. Re-flow the expand as a flex row so the title
     and the button sit on one line, like live. */
  #pane .todo-list-expand { display: flex !important; flex-direction: row !important; align-items: center !important; }
  #pane .todo-list-expand > a.monaco-button { flex: 1 1 auto !important; min-width: 0 !important; }
  #pane .todo-clear-button-container { flex: 0 0 auto !important; width: auto !important; }
  /* The chat input's Monaco editor carries LIVE inline widths/heights on
     the editor root and every inner layer, and live-computed absolute
     positions on the .view-line elements — so the text wraps at the LIVE
     width and the box height follows the live line count, not the mirror's
     own width. Re-fit it like the monaco-list rows: stretch the layers to
     the mirror width, put the content layers back in flow (they are
     position:absolute with live-computed sizes, which would otherwise
     collapse the auto heights), and let the live line breaks re-wrap as
     plain text runs at the mirror width. The editor keeps position:relative
     (its absolutely positioned layers — cursors-layer, minimap, … — need
     it as their containing block). */
  #pane .chat-input-container .interactive-input-editor .monaco-editor {
    width: 100% !important; height: auto !important;
  }
  #pane .chat-input-container .interactive-input-editor .monaco-editor .overflow-guard {
    width: 100% !important; height: auto !important;
  }
  #pane .chat-input-container .interactive-input-editor .monaco-editor .editor-scrollable,
  #pane .chat-input-container .interactive-input-editor .monaco-editor .lines-content {
    position: static !important; width: 100% !important; height: auto !important;
    /* lines-content carries contain:strict (Monaco perf containment): with
       size containment, height:auto is computed WITHOUT regard to content,
       which would collapse the auto-height chain to 0. */
    contain: none !important;
  }
  #pane .chat-input-container .interactive-input-editor .monaco-editor .view-lines {
    position: static !important; width: auto !important; height: auto !important;
    white-space: normal !important;
  }
  #pane .chat-input-container .interactive-input-editor .monaco-editor .view-line {
    position: static !important; width: auto !important; height: auto !important;
    white-space: normal !important;
  }
  #pane .chat-input-container .interactive-input-editor .monaco-editor .view-line > span {
    position: static !important;
  }
    /* Safety net: the chat find widget is hidden in the live page (visibility:hidden)
     by a rule scoped to .monaco-workbench. The #pane.monaco-workbench class below
     normally makes that rule apply, but keep an explicit hide in case it is not. */
  #pane .simple-find-part-wrapper, #pane .monaco-findInput, #pane .simple-find-part { display: none !important; }
  /* Shimmer text labels render via an animated gradient: background-clip:
     text plus a transparent text fill plus a background shorthand whose
     stops are theme vars. The live CSSOM serializes that shorthand without
     the gradient (a browser quirk for shorthand+var gradients), so the
     extracted mirror CSS carries no gradient and the clipped text paints
     nothing. Affected selectors:
     - .chat-thinking-spinner-item .chat-thinking-spinner-label: the
       "Thinking/Considering/Analyzing" row that tails the streaming list
       (only the leading dot glyph was visible).
     - .chat-thinking-title-shimmer: the collapsed active-title shimmer.
     - .progress-container.shimmer-progress .rendered-markdown.progress-step
       > p (and .chat-progress-shimmer-text): the DOT-LESS status labels
       ("Evaluating", "Working", …) on the progress row.
     - .chat-confirmation-widget.shimmer-progress …title-inner > p: the
       shimmer title of collapsible tool widgets.
     Re-apply the gradient from the same theme vars the workbench rule uses. */
  #pane .chat-thinking-spinner-item .chat-thinking-spinner-label,
  #pane .chat-thinking-title-shimmer,
  #pane .progress-container.shimmer-progress .rendered-markdown.progress-step > p,
  #pane .progress-container.shimmer-progress .rendered-markdown.progress-step .chat-progress-shimmer-text,
  #pane .chat-confirmation-widget.shimmer-progress .chat-confirmation-widget-title-inner > .rendered-markdown > p {
    background-image: linear-gradient(90deg,
      var(--vscode-descriptionForeground) 0%,
      var(--vscode-descriptionForeground) 30%,
      var(--vscode-chat-thinkingShimmer) 50%,
      var(--vscode-descriptionForeground) 70%,
      var(--vscode-descriptionForeground) 100%) !important;
  }
  /* The live chat-input cursor blinks via a 500ms JS timer that toggles the
     cursor element's inline visibility (Monaco ViewCursors, default 'blink'
     style) — a static HTML snapshot cannot reproduce that, and may even
     capture the hidden phase. syncCursor applies this animation when the
     state message reports the input as focused; step-end gives the hard
     500ms/500ms toggle, matching ViewCursors.BLINK_INTERVAL. */
  #pane .mirror-cursor-blink { animation: mirror-cursor-blink 1s step-end infinite; }
  @keyframes mirror-cursor-blink { 0% { visibility: visible; } 50% { visibility: hidden; } }
  /* Dropdown popup open/close animation. Live scopes these rules to the
     workbench root's .modern-ui.monaco-enable-motion classes, which the
     extracted subtree (and #pane) do not carry, so the rules in the
     extracted CSS never match in the mirror even though the @keyframes are
     present in it. Re-scope the SAME animations (same keyframes, timings,
     easing, transform origins) onto the mirror's popup: the .context-view
     sibling of the pane root holds the .action-widget.action-widget-dropdown
     element. The open animation plays when the widget element enters the
     DOM (fresh insert, or a re-insert after close); updatePopup restarts it
     explicitly when a reopen patches an existing widget in place. */
  #pane .context-view .action-widget.action-widget-dropdown {
    animation: action-widget-dropdown-open .25s cubic-bezier(.22, 1, .36, 1) both;
    transform-origin: bottom left;
    will-change: transform, opacity;
  }
  /* Popup opened ABOVE its anchor (the .context-view.bottom class marks
     that alignment in live): scale from the top edge, flush with the anchor. */
  #pane .context-view.bottom .action-widget.action-widget-dropdown {
    transform-origin: top left;
  }
  #pane .context-view .action-widget.action-widget-dropdown.action-widget-dropdown-closing {
    animation: action-widget-dropdown-close .15s cubic-bezier(.22, 1, .36, 1) both;
    pointer-events: none;
  }
`
