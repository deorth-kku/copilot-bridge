package cdp

import (
	"encoding/base64"
	"encoding/json/v2"
	"errors"
)

// imageFetchExpr fetches one resource URL (blob: or vscode-file:) from
// inside the live page and returns its bytes as base64 plus the content
// type. Blob URLs are only valid inside the creating document, so the
// mirror page cannot load them directly — the server fetches the bytes
// over CDP and serves them under /img/ instead.
const imageFetchExpr = `async (url) => {
  const r = await fetch(url);
  if (!r.ok) return { err: 'http ' + r.status };
  const buf = new Uint8Array(await r.arrayBuffer());
  if (buf.length === 0) return { err: 'empty body' }; // revoked blob: 200 with no bytes
  let bin = '';
  for (let i = 0; i < buf.length; i += 0x8000) {
    bin += String.fromCharCode.apply(null, buf.subarray(i, i + 0x8000));
  }
  return { b64: btoa(bin), type: r.headers.get('content-type') || '' };
}`

// imageCanvasExpr captures the rendered bitmap of the <img> whose src is
// the given URL and re-encodes it as PNG. Fallback for revoked blob URLs:
// the blob body is gone (fetch fails), but the decoded bitmap of an image
// that is still displayed remains in the renderer and can be drawn to a
// canvas.
const imageCanvasExpr = `(url) => {
  const img = Array.from(document.images).find(im => im.src === url && im.naturalWidth > 0);
  if (!img) return { err: 'no decoded img for url' };
  const c = document.createElement('canvas');
  c.width = img.naturalWidth;
  c.height = img.naturalHeight;
  c.getContext('2d').drawImage(img, 0, 0);
  const dataUrl = c.toDataURL('image/png');
  return { b64: dataUrl.slice(dataUrl.indexOf(',') + 1), type: 'image/png' };
}`

type imageOut struct {
	Err  string `json:"err"`
	B64  string `json:"b64"`
	Type string `json:"type"`
}

// FetchImage downloads one image resource (a blob: or vscode-file: URL)
// from the live page and returns its bytes, content type, and which stage
// produced them ("fetch" or "canvas"). It first tries to fetch the URL; if
// the blob has been revoked (fetch fails) but the image is still displayed,
// it captures the rendered bitmap via canvas and re-encodes it as PNG.
func FetchImage(s *Session, url string) ([]byte, string, string, error) {
	arg, _ := json.Marshal(url)

	if raw, err := callExpr(s, imageFetchExpr, string(arg)); err == nil {
		if v, err := decodeEval[imageOut](raw); err == nil && v.Err == "" {
			data, ctype, derr := decodeImage(*v)
			if derr == nil {
				return data, ctype, "fetch", nil
			}
		}
	}
	if raw, err := callExpr(s, imageCanvasExpr, string(arg)); err == nil {
		if v, err := decodeEval[imageOut](raw); err == nil && v.Err == "" {
			data, ctype, cerr := decodeImage(*v)
			if cerr == nil {
				return data, ctype, "canvas", nil
			}
		}
	}
	return nil, "", "", errors.New("image not fetchable and not capturable")
}

func decodeImage(v imageOut) ([]byte, string, error) {
	data, err := base64.StdEncoding.DecodeString(v.B64)
	if err != nil {
		return nil, "", err
	}
	return data, v.Type, nil
}
