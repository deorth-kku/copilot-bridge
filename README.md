# copilot-bridge

Watches VS Code Copilot chat inputs via CDP (port 9222) and pre-loads the selected model on the local llama.cpp server.

## How it works
- Polls `http://127.0.0.1:9222/json/version` until VS Code (started with
  `--remote-debugging-port=9222`) is reachable, then rescans `/json/list`
  every 2s for workbench page targets.
- Per window: page-level CDP WebSocket + injected versioned MutationObserver
  (50ms debounce) pushes `{input, model, effort, mode}` via a runtime binding.
- On every non-empty input event: looks up the model in the VS Code user
  `settings.json` (`oaicopilot.models`, loaded at startup and hot-reloaded
  on change). Only models whose `optimization` field is `llama.cpp` are
  pre-loaded; other values (openrouter, etc.) are remote API providers
  with no `/models/load` endpoint and are skipped.
- For a loadable model, sends
  `POST {server root}/models/load` with
  `{"model": "<id>"}` — the path is relative to the server ROOT
  (baseUrl `http://abc.com/v1` → `http://abc.com/models/load`).
- 30s per-model cooldown (timestamp refreshed when the request is sent).
- Load requests respect VS Code's `http.proxy` / `http.noProxy` settings
  (noProxy entries are host suffixes); with no proxy configured the
  client connects directly.
- Response handling: `400 "model is already running"` is treated as
  normal (model already loaded); `404 "File Not Found"` means the model
  is not on the server.
- Survives VS Code fully exiting and restarting (state machine:
  waiting → monitoring → disconnected → ...).

## Build
| platform | command | output |
|---|---|---|
| Windows | `.\build.ps1` | `copilot-bridge.exe` |
| Linux / macOS | `go build -trimpath -o copilot-bridge .` | `copilot-bridge` |

On Windows the exe is a console-free GUI process (`-H windowsgui`); on
Linux/macOS it's a normal binary you run in the background
(e.g. `nohup ./copilot-bridge &`). `build.ps1` runs
`go build -trimpath -ldflags "-H windowsgui" -o copilot-bridge.exe .`.

## Setup
Enable the CDP debugging port in VS Code (one-time):
1. Open the Command Palette (`Ctrl+Shift+P`) and run
   `Preferences: Configure Runtime Arguments` — this opens `argv.json`.
2. Add `"remote-debugging-port": "9222"` to the JSON object.
3. Restart VS Code.

(Alternatively, launch VS Code with `--remote-debugging-port=9222` on the CLI.)

## Flags
| flag | default | meaning |
|---|---|---|
| `-cdp` | `127.0.0.1:9222` | CDP HTTP address |
| `-settings` | VS Code user `settings.json` (see below) | settings file (hot-reloaded on change) |
| `-cooldown` | `30s` | per-model load cooldown |
| `-debounce` | `50ms` | page-side DOM-change debounce for the injected observers |
| `-log` | `<tempdir>/copilot-bridge/app.log` | log file |
| `-verbose` | `false` | debug-level logging |
| `-stop-grace` | `10s` | grace window after a `Stop` hook event before the armed power-off fires (see **Power off at task end**) |

Default `settings.json` location per platform:

| platform | path |
|---|---|
| Windows | `%APPDATA%\Code\User\settings.json` |
| Linux | `~/.config/Code/User/settings.json` |
| macOS | `~/Library/Application Support/Code/User/settings.json` |


## Power off at task end (hook subcommand)

Optional feature: ask the machine to power off after the next agent task
finishes. Arm it from the workspaces page
(`http://<host>:9527/workspaces`, the **下次任务结束时关机** button). The
bridge then powers the machine off when:

1. a VS Code agent task ends (`Stop` hook event), and
2. no new task starts (`SessionStart`) within the grace window
   (`-stop-grace`, default 10s).

A new task start inside the grace window cancels only that stop's pending
power-off — the armed state is KEPT, so the NEXT stop (without a following
start) still powers off. Disarm the toggle any time to cancel.

### How it works
- The `hook` subcommand is registered as a VS Code agent hook for the
  `SessionStart`, `Stop`, and `PreToolUse` events. VS Code writes the hook's
  JSON payload to the subcommand's stdin; it is forwarded verbatim to the
  bridge's `POST /api/hook` endpoint (the mirror web server, default
  `127.0.0.1:9527`).
- The bridge's shutdown planner tracks the armed state plus the last
  start/stop times. When the conditions above are met it cancels the main
  context (graceful shutdown of CDP and the web server), then powers off
  the machine.
- When `-ntfy-topic` is set, two events also push an ntfy.sh notification,
  each capped at 1500 characters and carrying the mirror URL returned by
  `/api/hook` as the notification's `Click` target, so tapping the phone
  notification opens the mirror of the workspace the task ran in. The push
  is independent of the bridge (a missing bridge still notifies, without
  the Click link):
  - `Stop`: the agent's last message (parsed from the session transcript,
    best effort) as the body.
  - `PreToolUse` of the ask-questions tool (`tool_name`
    `vscode_askQuestions`): the questions and their options as the body,
    so a phone notification arrives while the agent waits for an answer.
    `PreToolUse` fires for every tool; only this tool notifies, and the
    hook never writes to stdout (no `hookSpecificOutput`), so the tool
    call itself is unaffected.
- Power-off is Windows-only (`ExitWindowsEx(EWX_POWEROFF)`); on Linux /
  macOS it is a no-op.
- The subcommand always exits 0 (a missing bridge must never disrupt the
  agent); failures go to stderr and the app log.

### Install the hook
Create `shutdown.json` in the user-level hook folder with the ABSOLUTE
path to `copilot-bridge.exe`:

```json
{
  "hooks": {
    "SessionStart": [
      { "type": "command", "command": "C:\\Users\\deort\\vscode-load-llama\\copilot-bridge.exe hook" }
    ],
    "Stop": [
      { "type": "command", "command": "C:\\Users\\deort\\vscode-load-llama\\copilot-bridge.exe hook" }
    ],
    "PreToolUse": [
      { "type": "command", "command": "C:\\Users\\deort\\vscode-load-llama\\copilot-bridge.exe hook" }
    ]
  }
}
```

- Windows user-level hook folder: `%USERPROFILE%\.copilot\hooks\`.
- The command must be an absolute path — hooks run through a shell whose
  PATH may not include the exe's directory.
- The bridge must be running with the web server enabled (default
  `0.0.0.0:9527`) for hook events to reach it.
- Hook subcommand flags: `-bridge` (default `http://127.0.0.1:9527`) —
  the bridge HTTP address to forward events to; `-ntfy-topic` (default
  empty = no notification) — the ntfy.sh topic that receives the
  task-finished and question notifications (with the mirror URL as their
  Click link).

## Testing
`go test ./internal/...` runs everything. The injected JavaScript is
covered in two layers (both skip cleanly when node is not on PATH):

- **Syntax** — every injected JS constant is checked with `node --check`
  (`TestInjectedJSCompiles`, `TestPageHTMLInlineScriptsCompiles`).
- **Behavior** — 77 node:test unit tests run the real JS against a minimal
  DOM stub (`internal/jscheck/testdata/domstub.js`):
    - `internal/jscheck/testdata/cdp/*.test.js` (55): extraction probes,
      the two injected IIFEs (initial snapshot, 50ms debounce, no stacking
      on re-injection, capture-phase scroll wakes), font data-URI
      rewriting, and image fetching.
    - `internal/jscheck/testdata/mirror/mirror_js.test.js` (22): the
      mirror page's inline client IIFE — WebSocket connect/reconnect,
      window-picker sync, versioned CSS/theme updates, incremental DOM
      patching (node identity preserved), popup placement (anchor-relative
      + viewport clamp), and mouse/keyboard/wheel input capture.

The unit tests are also runnable directly with node (node 21+), with no
Go prerequisite — the module fixtures
(`internal/jscheck/testdata/{modules,raw}/`) are committed alongside the
suites:

```sh
node --test internal/jscheck/testdata/cdp/*.test.js
node --test internal/jscheck/testdata/mirror/mirror_js.test.js
```

`TestJSFixturesCurrent` and `TestMirrorJSFixturesCurrent` (part of
`go test ./internal/...`) fail when the committed fixtures drift from the
JS constants. After editing an injected JS constant, regenerate them:

```sh
go test -run TestJSUnitStandalone ./internal/cdp -gen
go test -run TestMirrorJSFixturesCurrent ./internal/mirror -gen
```

When adding a new injected JS constant, add it to
`jsUnitModules`/`jsUnitRaw` in `internal/cdp/jsunit_test.go` and to
`TestInjectedJSCompiles`, then regenerate the fixtures. The mirror page's
inline script is extracted automatically by `pageScripts()` in
`internal/mirror/jsunit_test.go`.

## Caveats
- **Single instance only.** The CDP binding is a page global; a second
  running instance overrides the first one's binding.
- VS Code must expose the CDP port (see **Setup**); without it the tool
  stays in the waiting state.
- `settings.json` is hot-reloaded via fsnotify (debounced 150ms); a failed
  reload (e.g. invalid JSON) keeps the previous snapshot. No restart needed
  after editing it.
