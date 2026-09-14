module.exports = async (url) => {
  const r = await fetch(url);
  if (!r.ok) return { err: 'http ' + r.status };
  const buf = new Uint8Array(await r.arrayBuffer());
  if (buf.length === 0) return { err: 'empty body' }; // revoked blob: 200 with no bytes
  let bin = '';
  for (let i = 0; i < buf.length; i += 0x8000) {
    bin += String.fromCharCode.apply(null, buf.subarray(i, i + 0x8000));
  }
  return { b64: btoa(bin), type: r.headers.get('content-type') || '' };
};
