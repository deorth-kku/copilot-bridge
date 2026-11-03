
  let cssFP = '';
  try {
    cssFP = document.styleSheets.length + ':' +
      (document.documentElement.getAttribute('style') || '').length + ':' +
      (document.body ? (document.body.getAttribute('style') || '').length : 0);
  } catch (e) {}
