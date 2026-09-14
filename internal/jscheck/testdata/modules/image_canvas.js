module.exports = (url) => {
  const img = Array.from(document.images).find(im => im.src === url && im.naturalWidth > 0);
  if (!img) return { err: 'no decoded img for url' };
  const c = document.createElement('canvas');
  c.width = img.naturalWidth;
  c.height = img.naturalHeight;
  c.getContext('2d').drawImage(img, 0, 0);
  const dataUrl = c.toDataURL('image/png');
  return { b64: dataUrl.slice(dataUrl.indexOf(',') + 1), type: 'image/png' };
};
