# v0.5.1

补丁版本：修复 v0.5.0 里两处仍然无条件输出终端转义序列的地方。

v0.5.0 引入了统一的颜色收口点（`NO_COLOR`、`TERM=dumb`、管道检测），但有两个渲染路径**没有走这个收口点**，因此那套规则对它们无效。

## 修复

### `live --once` 把颜色转义写进了管道

仪表盘面板的边框是**手写转义序列**而非经 lipgloss 渲染，原因是：lipgloss 在 stdout 非 TTY 时会剥掉纯符号字符串的 ANSI，边框会掉色。**这个 workaround 恰恰就是泄漏的原因**——无条件发射等于绕过了整个颜色决策。

```
live --once | cat          修复前 146 字节转义  →  修复后 0
NO_COLOR=1 live --once     修复前无效（仍 146） →  修复后 0
```

### `sessions --watch` 把清屏序列写进了管道

`sessions --watch > out` 的开头有两个没人要的转义序列。

注意这里是 **TTY 问题、不是颜色问题**：用 `NO_COLOR` 的终端**仍然需要清屏**，而管道两者都不需要。把两者混为一谈，无论往哪边倒都会弄坏一个——所以它查的是「是否终端」而非「是否上色」。

```
sessions --watch | cat     修复前 2 次清屏  →  修复后 0
```

## 终端上的行为完全未变

这是本次修复的硬性验收项——**修复不能以牺牲真终端为代价**：

| 场景 | 修复前 | 修复后 |
|---|---|---|
| 真 pty（`TERM=xterm-256color`）`live --once` | 360 字节转义 | **360 字节**（完全一致） |
| 真 pty `sessions --watch` | 2 次清屏 | **2 次**（完全一致） |
| `DEVINMONITOR_COLOR=always`（管道强制上色） | — | 146 字节（逃生阀保留） |

另外验证了一条**布局不变性**：把彩色面板的转义剥掉后，与无色版本**逐字节相同**——所以关闭颜色只是「少了些字节」，不会移动任何东西。

## 测试

新增 `internal/live/panel_color_test.go`（4 个测试）与 `internal/integration/watch_clear_test.go`（2 个测试）。两者都做了 **red-green 反向验证**：去掉判断后测试精确失败，还原后通过——确认断言真的承重。

`watchLoop` 是无限循环、无法单测，因此把清屏判定提取为 `clearScreenForRefresh(w, isTTY)`，让行为可被测试覆盖。

## 验证

```
go build ./... ✅    go vet ./... ✅    gofmt 干净 ✅
go test ./... → 20 个包通过，0 失败
MCP conformance ✅（legacy 28 + modern 17）
scripts/verify-against-vendor.sh ✅（6 个可比会话与 Devin 官方口径吻合）
```

## 完整变更

- `internal/live/live.go` — 面板边框转义按 `ui.ColorEnabled()` 收口
- `internal/ui/ui.go` — 新增 `ui.StdoutIsTTY()`（清屏需要终端，这与「是否有权上色」是两个问题）
- `internal/integration/cmd.go` — 清屏按 TTY 判定；提取可测的 `clearScreenForRefresh`
- `internal/live/panel_color_test.go`（新）、`internal/integration/watch_clear_test.go`（新）