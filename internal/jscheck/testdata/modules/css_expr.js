module.exports = async () => {
  const toB64 = (buf) => {
    let s = '';
    const bytes = new Uint8Array(buf);
    for (let i = 0; i < bytes.length; i += 0x8000) {
      s += String.fromCharCode.apply(null, bytes.subarray(i, i + 0x8000));
    }
    return btoa(s);
  };
  const mime = (u) => {
    const m = u.toLowerCase();
    if (m.includes('.woff2')) return 'font/woff2';
    if (m.includes('.woff')) return 'font/woff';
    if (m.includes('.ttf')) return 'font/ttf';
    if (m.includes('.otf')) return 'font/otf';
    if (m.includes('.eot')) return 'application/vnd.ms-fontobject';
    return 'application/octet-stream';
  };
  let css = '';
  for (const sheet of document.styleSheets) {
    let rules;
    try { rules = Array.from(sheet.cssRules); } catch (e) { continue; }
    let text = rules.map(rule => rule.cssText).join('\n');
    const base = sheet.href || location.href;
    for (const m of text.matchAll(/url\(([^)]+)\)/g)) {
      const raw = m[1].replace(/^["']|["']$/g, '').split(/[?#]/)[0];
      if (!/\.(woff2?|ttf|otf|eot)([?#]|$)/i.test(raw)) continue;
      let abs;
      try { abs = new URL(raw, base).href; } catch (e) { continue; }
      let b64;
      try {
        const resp = await fetch(abs);
        if (!resp.ok) continue;
        b64 = toB64(await resp.arrayBuffer());
      } catch (e) { continue; }
      if (!b64) continue;
      const dataURI = 'data:' + mime(abs) + ';base64,' + b64;
      // Re-wrap in the ORIGINAL quotes. m[1] may carry a ?query/#hash tail
      // that was stripped from raw; splicing raw into m[1] would leave
      // that tail AFTER the base64 payload and corrupt the data URI.
      const q = /^["']/.test(m[1]) ? m[1].charAt(0) : '';
      text = text.split(m[1]).join(q + dataURI + q);
    }
    css += text + '\n';
  }
  return { css: css };
};
