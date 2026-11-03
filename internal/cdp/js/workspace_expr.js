(async () => {
  try {
    const c = await window.vscode.context.resolveConfiguration();
    const w = c && c.workspace;
    if (!w) return null;
    const pick = (u) => u ? (u._formatted || (u.scheme + '://' + (u.authority || '') + u.path)) : null;
    return w.uri ? pick(w.uri) : pick(w.configPath);
  } catch (e) { return null; }
})()