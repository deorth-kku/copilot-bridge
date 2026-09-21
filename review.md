# 代码审查报告（仅检查，未做任何修改）

## 一、发现的 BUG

### B1. 阶段 2（CSS 追发消息）会把光标重置到输入框开头 —— `mirror.go` `refreshWindow`
`follow` 消息只填了 CSS/Rect/Scroll/Popup 等字段：
```go
follow := stateMsg{
    Type: "state", CSS: css, CSSVer: cssVer,
    Rect: ..., Scroll: ..., ScrollPath: ..., ScrollRows: ...,
    Window: ..., WindowID: ..., Windows: wins,
}
```
但 `stateMsg.CursorChar` 没有 `omitempty`（注释明确说 0 是合法值），所以 follow 消息会带上 `"cursorChar": 0`。浏览器端（`page.go`）：
```js
lastCursorChar = (typeof m.cursorChar === 'number') ? m.cursorChar : -1; // → 0
...
syncCursor(m); // → repositionCursor() 把 caret 移到第 0 个字符
```
**触发路径**：打开 popup 时 VS Code 会加内联 `<style>` → cssFP 翻转 → 阶段 2 发出 follow → 镜像光标跳到输入框开头，直到下一条 state 消息才恢复。同时 follow 里 `InputFocused` 为 false，`syncCursor` 还会顺带摘掉 `mirror-cursor-blink` 类（闪烁短暂丢失）。每次开 popup 都会复现。

### B2. 工作区页面：行进入错误态后无法再点击 —— `workspaces_page.go`
`openRow` 失败时执行 `row.className = 'row err'`，而列表的 click 委托判断是：
```js
while (row && row !== list && !(row.className === 'row')) row = row.parentElement;
```
`'row err' !== 'row'`，于是向上走到 `list` 直接 return。该行在下次 `render()`（搜索输入或新的 WS 推送）之前**永久失效**，无法重试打开。应改为 `row.classList.contains('row')` 或匹配 `^row`。

### B3. `sendFullState` 缺少 `ScrollRows` / `InputBreaks` —— `mirror.go`
新客户端（或切换窗口后的首个全量状态）拿到的消息没有 `ScrollRows`（snap 里有 `scrollRows` 但没拷贝）和 `InputBreaks`（`snapState` 根本没存这个字段）。后果：首屏时 `reflowInputLines` 直接返回（输入框保留 live 的换行块，直到下一次 html 更新才重排）、`alignToLiveViewport` 返回 null 走 distBottom 兜底（滚动对齐精度下降）。正常刷新路径（`base` 消息）两者都带，只有全量首屏缺失。

### B4. `Session.Run` 的 ctx 等待 goroutine 泄漏 —— `session.go`
```go
go func() { <-ctx.Done(); s.Stop() }()
```
`ctx` 是进程级 ctx。连接断开时读循环结束、`Run` 返回、discovery 用**新 Session 对象**重启窗口，但旧 Session 的这个 goroutine 一直存活到进程退出。每次窗口重连泄漏一个 goroutine（持有旧 session 引用）。应改为在 `Run` 内 `defer` 一个可取消的子 ctx，或把 Stop 逻辑挂到 `readDone`。

### B5. 404 模型无冷却，每次输入变化都发一次 POST —— `loader.go`
`classify` 对 `404 "File Not Found"` 返回 false → `l.last` 不写入 → 冷却不生效。服务器上没有该模型时，用户每改一次输入文本（payload 变化即新事件）就发一次 `POST /models/load` 并收到 404。README 把 404 当"正常"处理，但没给它任何负冷却，属于请求放大。建议 404 也 arm 一个（更长的）冷却。

### B6. `Page.frameNavigated` 不区分主框架 —— `session.go` `handle`
事件对**任何 frame**（含 webview/iframe 导航）都会触发一次"re-inject"（2 次 CDP Call + 日志）。IIFE 有版本守卫所以不会堆叠，功能无害，但打开 markdown 预览等 webview 时会产生无谓的 CDP 流量和日志噪音。应检查 `params.frame.parentId == null`（或 frameId 与主 frame 一致）再入队。

### B7.（轻微）`syncDrawnScrollbars` 对 `scrollPath: []` 的主滚动器不走 live 比例分支 —— `page.go`
```js
var target = lastScrollPath && lastScrollPath.length ? pathEl(lastScrollPath) : null;
```
当 pane 根自身就是滚动器（`scrollPath = []`，scrollPathJS 明确支持此情况）时 `target` 为 null，主滚动器的自绘滑条退回"用镜像自身几何"分支，而不是 live 比例。该场景少见，但与其他分支语义不一致。

### B8.（轻微）`normalizeAuthority` 用 `url.PathUnescape` —— `workspaces.go`
`PathUnescape` 会把字面量 `+` 解码成空格。当前 storage.json 把 `+` 存成 `%2B` 所以没问题，但一旦某条 remote authority 含未编码的 `+`（或将来格式变化）会被悄悄改坏。用只处理 `%XX` 的解码（或 `QueryUnescape` 的替代/手写）更稳。

---

## 二、过度防御设计

| # | 位置 | 说明 |
|---|------|------|
| D1 | `session.go` `Call` 超时路径 + `routeResponse` 的 `select/default` + `failPending` 的 `select/default` | 三处都在防"不可能发生"的满缓冲：pending channel 是 buf 1 且只写入一次，`routeResponse` 找到 ch 时 Call 必然还在等待（超时已 delete）。`default` 分支是纯保险丝，可保留但属于冗余层。 |
| D2 | `mirror.go` `handlePage` / `handleWorkspacesPage` 的 `if r.URL.Path != "/"` | mux 的 `"/"` 是 catch-all，`/workspaces` 等具体模式已先行匹配，这两个检查永远不会命中（除非未来加模式）。 |
| D3 | `mirror.go` `forwardMouse` 中 `m.snapMu.Lock()` 保护 `selectors := m.selectors` | `m.selectors` 是不可变字段，锁只服务于 `snaps` 读取；把 selectors 拷贝放进锁内是多余的。 |
| D4 | `mirror.go` `removeClient` 末尾 `c.conn.Close()` | 即使 `LoadAndDelete` 没赢（别人已移除）也会再 Close 一次——无害但属于"双保险"；真正需要的幂等保护已由 LoadAndDelete 完成。 |
| D5 | `page.go` `elemInfo` 中 `var underRoot = (t === root); ... if (node === root) underRoot = true;` | 第二句永远为 true 时才执行，而循环能走到 `node === root` 时 t 必然是 root 后代，第一句的初值判断是冗余的（两个分支殊途同归）。 |
| D6 | `mirror.go` `forwardMouse` 的 `e.Char != nil && *e.Char >= 0` | 浏览器只在 `ich >= 0` 时才带 `char` 字段，`>= 0` 检查是重复防御。 |
| D7 | `session.go` `handle` 的 `if len(raw) > 0 && raw[0] == '['` 批量消息分支 | CDP 不会把多条消息批成一个 JSON 数组，整个分支是为不存在的行为写的（保留无害）。 |
| D8 | `settings.go` `ProxyFunc` 的 `if s == nil` | 快照指针不可能为 nil（Store.Load 永远返回已 Store 的指针）。 |

注：以上大多是"无害的保险丝"。真正值得精简的是 D2、D3、D5——它们增加阅读负担却不覆盖真实故障模式。

---

## 三、可合并的重复代码

### Go 侧

| # | 重复点 | 位置 | 建议 |
|---|--------|------|------|
| R1 | **两个几乎逐行相同的 fsnotify 文件监视器**（`reloadDebounce` 常量、`run` 的 debounce/select 循环、事件过滤、`Watch` 建目录监视） | `watch.go` vs `watch.go` | 抽一个通用 `watchFile(ctx, path, debounce, reload func() error, log)`（或泛型 snapshot 发布器），两个 Store 只剩 reload 逻辑。这是全项目最大的一块重复。 |
| R2 | **CDP evaluate 响应的 exceptionDetails 检查 + Result.Value 解码**，完整结构体复制了 6 份 | `session.go inject`、`toggle.go ToggleChatPoint`、`extract.go EvalClickPoint / EvalCharPoint / EvalRect`（`decodeEval` 是第 6 份） | 已有 `decodeEval[T]` 处理异常，但四个 `Eval*` 函数没复用它，各自重写了 resp 结构体。可加一个 `evalPoint(raw) (x, y float64, ok bool)`（或让 `decodeEval` 直接返回 `struct{X,Y}`），四个函数各缩到 ~5 行。 |
| R3 | **popup anchor 解析块**（`c.lastAnchor.Load()` → `cdp.EvalRect` → 拷贝 Popup 设 Anchor）在阶段 1 和阶段 2 逐字重复 | `mirror.go refreshWindow` | 抽 `withPopupAnchor(msg *stateMsg, c *client, s *cdp.Session, paneRect cdp.PaneRect)`。 |
| R4 | **`defaultSettingsPath` / `defaultStoragePath`** 的 UserConfigDir→UserHomeDir 回退模式复制两份 | `main.go` | 抽 `vscodeUserDir() string`，两个函数各一行。 |
| R5 | `Session.send` 与 `Session.Call` 的 id 分配 + map 构造 + Marshal 重复 | `session.go` | `Call` 可复用 send 的构造逻辑（send 变成 `sendWithID` 的内部函数）。 |
| R6 | `jscheck.WriteFixtures` 与 `RunJSUnit` 的 modules/raw 写文件逻辑重复 | `jscheck.go` | 抽 `writeModuleFiles(dir, modules, raw)`。 |
| R7 | `FingerprintState` 与 `HTMLState` 的布局字段（Rect/Scroll/ScrollPath/ScrollRows/NestedScrolls）重复声明 | `extract.go` | 可抽一个 `layoutState` 嵌入结构体（字段顺序/JSON 兼容需注意，但能消掉 ~10 行重复声明）。 |
| R8 | `equalInts` / `equalFloats` 手写循环 | `mirror.go` | 标准库 `slices.Equal`（go.mod 是 1.27）一行替代。 |
| R9 | `main.go` 与 `session_test.go` 的 mock CDP page 驱动逻辑高度相似（bindingCalled 空快照→真实事件、id 应答） | mock + 测试 | mock 可复用同一套假页面（mock 是独立 main 包，合并成本高，属可选项）。 |

### JS 侧（注入脚本与页面脚本）

| # | 重复点 | 位置 | 建议 |
|---|--------|------|------|
| R10 | **cssFP 计算块**在 `htmlExpr` 与 `fpExpr` 中逐字重复（本可像 `scrollContainerJS` 一样拼成共享 const） | `extract.go` | 拼一个 `cssFPJS` 常量。 |
| R11 | **scroll 对象字面量**（`sc ? {...} : {...}`）在两个 expr 中重复 | `extract.go` | 拼一个 `scrollObjJS`。 |
| R12 | **"遍历候选 selectors 找 pane 根"的 for 循环**出现 5 次（htmlExpr、fpExpr、healthExpr、clickPointExpr、rectExpr、charPointExpr） | `extract.go` | 拼一个 `findRootJS(selectors)` 片段。 |
| R13 | **`resolvePath` 与 `pathEl`** 两个函数做同一件事（DOM 路径→元素） | `page.go` | 合并为一个（语义差异只有空路径/无根时的返回值，可用参数或约定统一）。 |
| R14 | **输入编辑器定位前导**（`box = querySelector('.chat-input-container')` → `ed = ...monaco-editor` → 判空）在 `fitInputEditor`、`reflowInputLines`、`repositionCursor`、`syncCursor`、`inputCharAt` 中重复 5 次 | `page.go` | 抽 `chatInputEditor()` 返回 `ed` 或 null。 |
| R15 | **文本节点深度遍历 IIFE**（`(function w(node){...})`）在 `repositionCursor`、`inputCharAt`、`charPointExpr` 中重复 3 次 | `page.go` + `extract.go` | 各自包内抽 `collectTextNodes(root)`。 |
| R16 | `page.go` 三个独立的 `window.addEventListener('resize', ...)` 监听（syncDrawnScrollbars / positionPopup / repositionCursor） | `page.go` | 合并为一个 handler 顺序调用三者。 |
| R17 | `workspaces_page.go` 中 `#q` 选择器块声明两次（基础块 + 紧跟的 order/flex 块） | `workspaces_page.go` | 合并为一个规则块（纯外观）。 |

---

## 四、其他观察（非 BUG，供参考）

1. **`-web` 默认 `0.0.0.0:9527`**：镜像页无认证地暴露在局域网（README 也宣传 LAN 地址）。作为本地工具是有意设计，但 `handleWorkspaceOpen` 允许任意 LAN 客户端触发本机 `code` CLI 启动——如果机器在共享网络里值得加个绑定地址提醒或 token。
2. **`refreshWorkspaces` 的 CDP 探测在 publish loop 里同步执行**：每个 workspaces 页客户端打开期间，每秒对每个 live 窗口做一次 `resolveConfiguration` 探测，且阻塞 publish loop（会短暂推迟镜像 tab 的 state 推送）。当前窗口数少时无感，窗口多时值得改成异步或加节流。
3. **`handleWorkspaceList`（GET /api/workspaces）每次请求都触发全窗口 CDP 探测**：与上条同源，HTTP 轮询该端点会放大 CDP 流量。
4. **WS 断线重连会丢失窗口选择**：`selID` 存在每连接的 `client` 上，浏览器重连（网络抖动）后新 client 的 `selID` 为 nil → 回落到默认窗口。`localStorage` 的 `mirrorWin` 只覆盖"去工作区页再回来"的场景，不覆盖重连。属 UX 缺口。
5. **`refreshAll` 只在 `len(wins) > 0` 时清理 `snaps`/`toggleTimes`**：VS Code 全关后旧快照会一直保留到下次开窗口（有界，非泄漏，但可顺手在全关时也清理）。
6. **`refreshWindow` 日志字段 `marshalMs`** 实际从 phase 1 前计到函数末尾（含 phase 2 的 CSS 提取），名字与内容不符，诊断时容易误读。
7. **`Settings.ProxyFunc` 每次请求重新解析 proxy URL、重建 noProxy 列表**：正确但可把解析结果缓存进快照（快照本就不可变）。
8. **`sendWorkspaces` 在 `currentWorkspaces` 失败时静默返回**：workspaces 页会永远停在 "loading…"，没有任何错误反馈（与 `refreshWorkspaces` 的 warn 日志不对称）。

---

## 总结

- **BUG**：8 个，其中 B1（follow 消息光标归零）、B2（错误行不可点）、B3（首屏缺 ScrollRows/InputBreaks）是用户可感知的实际缺陷；B4 是真实 goroutine 泄漏；B5–B8 为轻微/边缘。
- **过度防御**：8 处，多数无害，D2/D3/D5 最值得删。
- **重复代码**：17 组，收益最大的是 R1（两个 fsnotify 监视器合一）、R2（6 份 exceptionDetails 解码合一）、R3（anchor 块）、R13/R14（页面 JS 的两个重复路径解析 + 5 份输入编辑器前导）。

按你的要求，以上均未做任何修改。

 