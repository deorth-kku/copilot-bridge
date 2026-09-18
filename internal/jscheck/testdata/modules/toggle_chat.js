module.exports = () => {
  let el = null;
  try {
    el = document.querySelector('.titlebar-container a[aria-label="Toggle Chat"]')
      || document.querySelector('a[aria-label="Toggle Chat"]');
  } catch (e) {}
  if (!el) return null;
  const r = el.getBoundingClientRect();
  if (!r.width || !r.height) return null;
  return { ok: true, x: r.left + r.width / 2, y: r.top + r.height / 2 };
};
