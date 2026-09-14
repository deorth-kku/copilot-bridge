(() => {
  const VERSION = 1;
  const wake = () => { try { window.vscodeLoadLlamaMirror('1'); } catch (e) {} };
  if (window.__mirrorVersion !== VERSION) {
    if (window.__mirrorMO) { try { window.__mirrorMO.disconnect(); } catch (e) {} }
    // Drop the previous install's scroll/resize listeners as well, so a
    // VERSION bump does not stack another pair on top of the old ones.
    if (window.__mirrorScrollH) document.removeEventListener('scroll', window.__mirrorScrollH, true);
    if (window.__mirrorResizeH) window.removeEventListener('resize', window.__mirrorResizeH);
    window.__mirrorVersion = VERSION;
    let timer = null;
    const schedule = () => {
      if (timer) return;
      timer = setTimeout(() => { timer = null; wake(); }, 50);
    };
    window.__mirrorSchedule = schedule;
    window.__mirrorMO = new MutationObserver(schedule);
    window.__mirrorMO.observe(document.documentElement, {
      subtree: true, childList: true, characterData: true, attributes: true,
    });
    window.__mirrorScrollH = schedule;
    document.addEventListener('scroll', schedule, true);
    window.__mirrorResizeH = schedule;
    window.addEventListener('resize', schedule);
  }
  (window.__mirrorSchedule || wake)();
  return 'ok';
})()