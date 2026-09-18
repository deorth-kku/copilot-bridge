package cdp

import (
	"encoding/json/v2"
)

// toggleChatExpr resolves the title-bar "Toggle Chat" button to the center
// of its bounding box. The attribute match is EXACT, not a substring scan:
// chat-message accessibility labels carry arbitrary conversation text and
// can legitimately contain "Toggle Chat", so a substring match could
// resolve to a message row instead of the button.
const toggleChatExpr = `() => {
  let el = null;
  try {
    el = document.querySelector('.titlebar-container a[aria-label="Toggle Chat"]')
      || document.querySelector('a[aria-label="Toggle Chat"]');
  } catch (e) {}
  if (!el) return null;
  const r = el.getBoundingClientRect();
  if (!r.width || !r.height) return null;
  return { ok: true, x: r.left + r.width / 2, y: r.top + r.height / 2 };
}`

// ToggleChatPoint returns the viewport coordinates of the title-bar
// "Toggle Chat" button's center. ok is false when the button is absent or
// not visible.
func ToggleChatPoint(s *Session) (float64, float64, bool) {
	raw, err := callExpr(s, toggleChatExpr, "")
	if err != nil {
		return 0, 0, false
	}
	var resp struct {
		Result struct {
			Value struct {
				OK bool    `json:"ok"`
				X  float64 `json:"x"`
				Y  float64 `json:"y"`
			} `json:"value"`
		} `json:"result"`
		Exception struct {
			Text string `json:"text"`
			Obj  struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return 0, 0, false
	}
	if resp.Exception.Obj.Description != "" || resp.Exception.Text != "" {
		return 0, 0, false
	}
	if !resp.Result.Value.OK {
		return 0, 0, false
	}
	return resp.Result.Value.X, resp.Result.Value.Y, true
}
