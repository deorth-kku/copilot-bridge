package main

// Mock servers for smoke-testing copilot-bridge.exe without a real
// VS Code / llama.cpp instance.
//
//	:9333  CDP endpoint  (/json/version, /json/list, /devtools/page/t1 WS)
//	:9444  llama mock   (captures POST /models/load)
//
// The mock page emits the POC-observed flow: an EMPTY-input snapshot
// right after Runtime.evaluate (must NOT trigger a load), followed by a
// real input event (MUST trigger exactly one load with the model id).

import (
	"encoding/json/v2"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/gorilla/websocket"
)

const (
	cdpPort   = "9333"
	binding   = "copilotBridge"
	modelName = "Qwen3.8 27B"
	modelID   = "Qwen3.8-27B"
)

func main() {
	// ---- llama mock -------------------------------------------------
	llama := http.NewServeMux()
	llama.HandleFunc("/models/load", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		log.Printf("LLAMA REQUEST %s %s body=%s", r.Method, r.URL.Path, string(b))
		w.WriteHeader(http.StatusOK)
	})
	go http.ListenAndServe("127.0.0.1:9444", llama)

	// ---- CDP mock ----------------------------------------------------
	up := websocket.Upgrader{}
	page := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		log.Printf("PAGE connected")
		send := func(payload string) {
			conn.WriteJSON(map[string]any{
				"method": "Runtime.bindingCalled",
				"params": map[string]any{"name": binding, "payload": payload},
			})
		}
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				continue
			}
			log.Printf("PAGE cmd %v", m["method"])
			if m["method"] == "Runtime.evaluate" {
				// startup snapshot: EMPTY input (must not trigger a load)
				send(fmt.Sprintf(`{"input":"","model":%q,"effort":null,"mode":null}`, modelName))
				// real input (must trigger one load)
				send(fmt.Sprintf(`{"input":"hello world","model":%q,"effort":"high","mode":"agent"}`, modelName))
			}
		}
	})
	cdp := http.NewServeMux()
	cdp.HandleFunc("/json/version", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"Browser":"mock"}`)
	})
	cdp.HandleFunc("/json/list", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"id":"t1","type":"page","url":"vscode-workbench://t1","title":"Mock Window","webSocketDebuggerUrl":"ws://localhost:%s/devtools/page/t1"}]`, cdpPort)
	})
	cdp.HandleFunc("/devtools/page/", page)

	log.Printf("mock listening: cdp=127.0.0.1:%s llama=127.0.0.1:9444", cdpPort)
	log.Fatal(http.ListenAndServe("127.0.0.1:"+cdpPort, cdp))
}
