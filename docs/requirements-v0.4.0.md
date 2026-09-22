# DevinMonitor v0.4.0 需求规格

> 基于同类产品**源码级对照**生成的需求文档。
> 范围决策：**专注 Devin，不做多源适配**（用户已明确）。

## 0. 参考仓库与源码坐标

| 仓库 | ★ | 许可 | 借鉴方向 | 关键源码位置 |
|---|---|---|---|---|
| [ccusage/ccusage](https://github.com/ccusage/ccusage) | 18.7k | MIT | **限额窗口** | `rust/crates/ccusage/src/blocks.rs`（618 行）、`docs/guide/blocks-reports.md` |
| [Maciek-roboblog/Claude-Code-Usage-Monitor](https://github.com/Maciek-roboblog/Claude-Code-Usage-Monitor) | 8.7k | MIT | **退出码 / provenance / 状态栏 / 仓库** | `src/claude_monitor/output/{snapshots,official,state}.py` |
| [Piebald-AI/splitrail](https://github.com/Piebald-AI/splitrail) | 223 | MIT | **MCP resources 与工具面** | `src/mcp/server.rs`、`src/mcp/types.rs` |
| [chiphuyen/sniffly](https://github.com/chiphuyen/sniffly) | 1.27k | — | **错误分析 / 分享脱敏** | `sniffly/core/constants.py`、`sniffly/core/stats.py`、`sniffly/share.py` |
| [junhoyeo/tokscale](https://github.com/junhoyeo/tokscale) | 5.5k | MIT | `DESIGN.md` 模板 | `DESIGN.md`（108 行） |

三份参考均 MIT，可直接借鉴实现思路（注明出处）。

### 我们已有的基础（避免重复造轮子）

- `internal/reader/extensions.go` 已读取 Devin 库**全部**表：`prompt_history`(903)、`tool_call_state`(8545)、`rendered_commits`、`app_state`
- **provenance 雏形已存在**：`internal/integration/cmd.go:40 provenanceLabel()`、`warehouse.go:277 provenanceTag()`（official=credit/ACU 记账 vs estimated=token 定价推算）
- 计费单位已有 **ACU** 概念：`config.ACURate`、`config.PlanACULimit`、`model.ACUCost`
- 已有 `snapshot`（状态卡片）、`burn-rate`（小时/日/周/月）、`warehouse`、`pricing` 命令

> ⚠️ 注意：本地库实测 `total_acu_cost` / `total_credit_cost` 均为 `0.0`（免费套餐），因此 **provenance 在本地实践中多为 `estimated`**；限额窗口的"官方口径"需要可插拔的数据源，不能假设一定有。

---

## 1. 限额窗口（Rate-limit Blocks）

**目标**：从"按自然日/周聚合"升级为**按计费窗口（block）聚合**，回答"我这一轮窗口烧了多少、还能撑多久"。

### 1.1 数据模型

新增包 `internal/limit`：

```go
// 一个计费窗口（block）。参考 ccusage blocks.rs:23 SessionBlock。
type Block struct {
    ID            string     // RFC3339 起始时刻，gap 块为 "gap-<t>"
    StartTime     time.Time  // 窗口起点（已 floor 到整点）
    EndTime       time.Time  // StartTime + WindowDuration
    ActualEndTime time.Time  // 最后一条消息时刻；gap 块为零值
    IsActive      bool       // 是否当前活跃窗口
    IsGap         bool       // 是否为空闲间隔（非真实窗口）
    Messages      []model.Message
    Tokens        TokenCounts   // input/output/cacheRead/cacheWrite/total
    ACU           float64       // 本窗口 ACU 消耗（Devin 计费单位）
    Cost          float64       // 本窗口成本（USD）
    Models        []string      // 去重后的模型名
    LimitResetAt  time.Time     // 上游给的额度重置时刻（可为零）
}
```

### 1.2 分块算法（逐条落地 ccusage `identify_session_blocks`）

```
输入：全部消息（按时间升序）、窗口时长 D（默认 5h）、now
输出：[]Block（含 gap 块）

currentStart = nil; currentEntries = []
for entry in entries:
    if currentStart == nil:
        currentStart = floorToHour(entry.Time)
    else:
        sinceStart = entry.Time - currentStart
        sinceLast  = entry.Time - last(currentEntries).Time
        if sinceStart > D || sinceLast > D:
            blocks += createBlock(currentStart, currentEntries, now, D)
            if sinceLast > D:
                blocks += createGapBlock(last(currentEntries).Time, entry.Time, D)
            currentStart = floorToHour(entry.Time)
            currentEntries = []
    currentEntries += entry
if currentStart != nil && len(currentEntries) > 0:
    blocks += createBlock(currentStart, currentEntries, now, D)
```

**边界规则（必须逐条实现）**

- `floorToHour(t)`：窗口起点 floor 到整点
- 新窗口触发条件：`sinceStart > D` **或** `sinceLast > D`
- 仅当 `sinceLast > D` 时才额外插入 **gap 块**
- `end = start + D`；`actualEnd = 最后一条消息时刻`
- `isActive = (now - actualEnd) < D && now < end`
- `isGap = true` 的块不参与燃烧率/预测

### 1.3 燃烧率与预测

```
// 参考 ccusage blocks.rs:567 calculate_burn_rate
if len(entries) == 0 || isGap: return nil
durationMin = (last.Time - first.Time) / 1min
if durationMin <= 0: return nil
tokensPerMin        = totalTokens / durationMin
tokensPerMinDisplay = (input + output) / durationMin   // 非缓存口径，用于指示器
costPerHour         = cost / durationMin * 60

// 参考 blocks.rs:586 project_block_usage（仅活跃块）
if !isActive || isGap: return nil
remainingMin  = round((end - now) / 1min)
projectedTok  = totalTokens + tokensPerMin * remainingMin
projectedCost = cost + costPerHour/60 * remainingMin
```

### 1.4 节奏（pace）与用尽预测

参考 Maciek `snapshots.py`：

```
PaceTolerancePoints = 10.0     // 百分点
usedPct    = 窗口内消耗 / 窗口限额 * 100
elapsedPct = (now - (resetAt - D)) / D * 100
delta      = usedPct - elapsedPct

delta >  +10 → "slow down"（用得快）
delta <  -10 → "speed up"（用得慢）
否则         → "on track"
```

- **用尽预测**：`exhaustedAt = now + (1 - usedRatio) / burnRatio`；展示为 `今天 14:30 (estimated)`，次日为 `明天 …`
- `(estimated)` 后缀**必须**在 confidence 非 official 时出现（见 §2.2）

### 1.5 窗口时长与限额配置

Devin 未公开固定 5 小时窗口，因此：

- 窗口时长**必须可配置**：`--window` flag + `config.limitWindow`（默认 `5h`）
- 限额**可配置**：`config.limitTokens` / `config.limitACU`（`PlanACULimit` 已有，复用）
- 未配置限额时：只输出用量/燃烧率，**不输出 pace 与达标百分比**（避免假精度）

### 1.6 CLI 接口

```
devinmonitor blocks                      # 全部窗口表（含 gap）
devinmonitor blocks --active             # 仅当前活跃窗口 + 详细预测
devinmonitor blocks --window 5h           # 覆盖窗口时长
devinmonitor blocks --limit-tokens 200000  # 覆盖 token 限额
devinmonitor blocks --limit-acu 500        # 覆盖 ACU 限额
devinmonitor blocks --json                # 机器可读（见 §2.7）
devinmonitor blocks --since/--until       # 日期过滤（复用现有 filter 语义）
```

表格列（宽度 < 120 列时降级，参考 ccusage `BLOCKS_COMPACT_WIDTH_THRESHOLD`）：
`窗口起点 | 模型 | 输入 | 输出 | 缓存读 | 缓存写 | 总 token | ACU | 成本 | 状态`

状态列复用现有 `i18n`：`⏰ 活跃 (剩 2h15m)` / `✅ 已完成 (3h42m)` / `⌛ 空闲`；活跃块附 `🔥 速率 2.1k/min`、`📊 预测 450k`、`🎯 节奏 on track`。

### 1.7 验收标准

- [ ] 构造边界用例：恰好 `sinceStart == D`、`sinceLast == D`（**不**切块，因为条件是严格 `>`）
- [ ] gap 块仅在 `sinceLast > D` 时出现，且不计入燃烧率
- [ ] 活跃判定在 `now` 跨过 `end` 后立即变为非活跃
- [ ] 单条消息的窗口不 panic（`durationMin <= 0` 时返回 nil 而非除零）
- [ ] 未配置限额时不输出 pace/百分比
- [ ] 表格在 80 列终端下不溢出

---

## 2. 自动化协议（退出码 + provenance）

**目标**：让 `devinmonitor` 能被脚本/状态栏/CI **可靠消费**。

### 2.1 语义化退出码

参考 Maciek `snapshots.py:167 _status()`：

| 退出码 | 含义 | 触发条件 |
|---|---|---|
| `0` | ok | 用量正常 |
| `10` | near_limit | `usedPct >= warningThreshold * 100`（默认阈值 0.8） |
| `11` | limit_hit | `usedPct >= 100` 或上游显式标记超额 |
| `20` | indeterminate / no_active_session | 无法判定用量；或无活跃窗口 |
| `30` | no data / config error | 库不存在、读失败、配置非法 |

- 阈值常量：`LimitWarningThreshold = 0.8`（参考 ccusage `BLOCKS_WARNING_THRESHOLD`）
- 判定顺序必须严格：`usedPct == nil` → 20；`>=100` → 11；`>= 阈值*100` → 10；否则 0
- **用法**：`snapshot --once --exit-code` 或 `blocks --active --exit-code` 时启用语义退出码；**默认行为不变**（避免破坏现有脚本）

### 2.2 confidence / source 分级

扩展我们已有的 `provenanceLabel`，统一为三元组：

```go
type Confidence string
const (
    ConfidenceOfficial  Confidence = "official"        // 来自 Devin 记账（credit/ACU）
    ConfidenceEstimate  Confidence = "estimated"       // 由 token 定价推算
    ConfidenceUnknown   Confidence = "unknown"
)

type Source struct {
    Kind string `json:"kind"` // "devin_db" | "statusline" | "config"
}
```

- 每个数值块携带 `{source, confidence}`（参考 Maciek `_external_block`）
- 展示层：confidence ≠ official 时**必须**加 `(estimated)` 后缀（现状 `[mixed]` 标签升级为逐块标注）
- 取值优先级：**official > 新鲜的 statusline/上游 > local estimate**

### 2.3 过期判定

- 常量 `OfficialTTLSeconds = 600`（参考 Maciek `OFFICIAL_TTL_SECONDS`）
- 上游口径数据超过 TTL → 标记 `stale: true`，并**降级**为 local estimate（而不是一直沿用）

### 2.4 防御性消毒（必须有）

参考 Maciek 对上游 bug 的防御（`_clean_pct`）：

- 非有限值（NaN / ±Inf）→ 丢弃为 `nil`，**绝不渲染成百分比**
- 略大于 100 的值视为舍入 → clamp 到 100
- epoch 量级的"百分比"（已知上游把 `resets_at` 写进 `used_percentage` 的 bug）→ 丢弃
- 已过期窗口的值 → 丢弃

### 2.5 单行 / 紧凑输出

```
devinmonitor snapshot --once --compact   # 单行，供 shell prompt / tmux
devinmonitor snapshot --once --output json
```

### 2.6 JSON schema（机器可读）

```jsonc
{
  "schemaVersion": 1,
  "generatedAt": "2026-09-22T02:00:00Z",
  "status": { "code": 0, "label": "ok" },
  "limits": {
    "five_hour": {
      "usedPercentage": 42.5,
      "tokensUsed": 183000, "tokenLimit": 430000,
      "acuUsed": 12.3, "acuLimit": 50,
      "resetsAt": "2026-09-22T05:00:00Z", "resetsAtEpoch": 1790000000,
      "source": { "kind": "devin_db" },
      "confidence": "estimated",
      "stale": false,
      "pace": { "label": "on track", "usedPercentage": 42.5, "elapsedPercentage": 38.0 },
      "forecast": { "exhaustedAt": null, "display": null }
    },
    "seven_day": { }
  },
  "breakdown": { "byModel": [ ], "acu": true, "costProvenance": "estimated" }
}
```

- `schemaVersion` 必须存在且在破坏性变更时递增（我们已有 `export_schema: 1` 的先例，保持一致风格）

### 2.7 验收标准

- [ ] 退出码矩阵逐条可测：构造近限额/超额/无数据/无活跃窗口四种状态
- [ ] 默认（不带 `--exit-code`）退出码行为**不变**（0/1）
- [ ] JSON 中每个数值块都有 `source` + `confidence`
- [ ] 注入 NaN / 1e18 / 3.0e9 的伪上游值，均被丢弃且不渲染
- [ ] 超过 TTL 的上游数据自动降级并置 `stale: true`

---

## 3. 状态栏集成

**目标**：把用量嵌进终端状态栏，这是整个生态最热门的集成点（ccusage Beta、Maciek `--statusline`，另有多个 ★1k–3.5k 的 statusline 项目）。

### 3.1 `--write-state`：原子写状态文件

参考 Maciek `output/state.py`（**关键：并发读永不看到半写文件**）：

```go
// 默认路径：~/.devinmonitor/state/latest.json（受 DEVINMONITOR_CONFIG_DIR 影响）
func writeStateFile(snapshot Snapshot, path string) error {
    os.MkdirAll(filepath.Dir(path), 0o755)
    tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())  // pid 唯一临时名
    os.WriteFile(tmp, jsonBytes, 0o644)
    return os.Rename(tmp, path)                          // 原子替换
}
```

```
devinmonitor snapshot --write-state
devinmonitor snapshot --write-state --state-file ~/.devinmonitor/state/latest.json
```

### 3.2 `--statusline`：单行输出 + 上游数据捕获

```
devinmonitor snapshot --statusline        # 单行，适合 shell/tmux statusline
```

- 若 stdin 提供了上游 payload（如 Devin 未来的 statusline JSON），**捕获其中的额度字段**并落盘：
  `~/.devinmonitor/statusline/latest.json`，带 `capturedAt`
- 捕获的数据受 §2.3 TTL 约束；过期即降级
- 捕获时同样执行 §2.4 消毒

### 3.3 验收标准

- [ ] 并发跑 20 个 `--write-state`，读者永远能解析出完整 JSON（原子性验证）
- [ ] 默认路径随 `DEVINMONITOR_CONFIG_DIR` 变化（保持可测试/沙箱友好）
- [ ] 父目录不存在时自动创建
- [ ] `--statusline` 输出严格单行、无 ANSI（除非显式要求颜色）、宽度可限

---

## 4. MCP 补齐（resources + 工具）

**目标**：对齐 splitrail 的 MCP 面——我们目前 **4 个工具、0 个 resources**。

### 4.1 Resources

参考 splitrail `src/mcp/server.rs:22`（URI 常量）+ `:416 list_resources` + `:436 read_resource`：

| URI | 名称 | 内容 |
|---|---|---|
| `devinmonitor://summary` | 用量摘要 | 今日/本周/本月成本、token、请求数、活跃会话数 |
| `devinmonitor://models` | 模型分解 | 各模型 token/成本/占比 |
| `devinmonitor://blocks` | 当前窗口 | 活跃 block 的用量、燃烧率、剩余时间、pace |
| `devinmonitor://alerts` | 告警 | 现有 `detectAlerts` 的结果 |

- `list_resources` 返回 `{uri, name}` 列表
- `read_resource` 按 URI 分派，返回**文本内容**（splitrail 用 `ResourceContents::text`，非 JSON）
- 在 `initialize` 的 `capabilities` 中声明 `resources: {}`

### 4.2 新增工具

| 工具 | 说明 |
|---|---|
| `get_blocks` | 限额窗口列表 / 活跃窗口详情（§1） |
| `get_limits` | 当前额度状态与 pace（§2.6 的 `limits` 段） |
| `compare_periods` | 两个时间段对比（复用现有 `compare`） |
| `list_reports` | 列出可用报表类型与格式（对齐 splitrail `list_analyzers`） |

### 4.3 initialize 元信息

参考 splitrail `:395`：`serverInfo` 增加 `instructions` 字段（给 LLM 的一句话说明）；协议版本保持 `2024-11-05` 兼容。

### 4.4 验收标准

- [ ] `tools/list` 返回 8 个工具；`resources/list` 返回 4 个 resources
- [ ] `resources/read` 对每个 URI 返回非空文本；未知 URI 返回规范错误
- [ ] 标准 `Content-Length` 帧下全部可用（v0.3.0 已修复，需回归）
- [ ] 通知仍不产生响应（回归）
- [ ] 新增能力的 `initialize` 响应含 `resources` capability 与 `instructions`

---

## 5. 价格覆盖 + JSON Schema

**目标**：让用户不改代码就能修正/补充模型定价（Devin 新模型、私有定价、免费额度）。

### 5.1 覆盖文件

- 路径：`~/.devinmonitor/pricing.json`（受 `DEVINMONITOR_CONFIG_DIR` 影响）
- 按**原始模型名**覆盖，字段与内置表一致：

```jsonc
{
  "$schema": "https://raw.githubusercontent.com/garywhat/devinmonitor/main/docs/pricing.schema.json",
  "models": {
    "swe-1-7": { "inputPerM": 3.0, "outputPerM": 15.0, "cacheReadPerM": 0.3, "cacheWritePerM": 3.75, "free": false },
    "my-internal-model": { "inputPerM": 0, "outputPerM": 0, "free": true }
  }
}
```

### 5.2 合并顺序

`用户覆盖 > 内置表`；覆盖项需记录来源，使 §2.2 的 confidence 能区分"内置估算"与"用户声明"。

### 5.3 CLI

```
devinmonitor pricing list                  # 内置表（现状）
devinmonitor pricing overrides             # 显示用户覆盖
devinmonitor pricing set <model> --input 3 --output 15   # 写入覆盖文件
devinmonitor pricing remove <model>
devinmonitor pricing validate              # 校验覆盖文件（含 $schema 与值域）
devinmonitor pricing schema                # 打印 JSON Schema 路径/内容
```

### 5.4 JSON Schema

- 产物：`docs/pricing.schema.json`（供 IDE 补全 + 校验，参考 ccusage 的配置 schema 体验）
- 必填：`models` 对象；每项数值 `>= 0`

### 5.5 验收标准

- [ ] 覆盖后 `models`/`daily` 等报表成本随之变化
- [ ] 覆盖文件非法（负值/类型错）时 `pricing validate` 报错并给出位置
- [ ] `$schema` 字段出现在写入的文件中
- [ ] 无覆盖文件时行为与现状**完全一致**

---

## 6. `DESIGN.md`

参考 tokscale `DESIGN.md`（108 行）的章节骨架，产出我们自己的设计文档：

```
# Design
## Source of truth        # sessions.db 为唯一事实源；本地优先、无网络
## Brand                  # 名称、语气、终端美学（灰边框 + 自适应）
## Product goals          # 本地优先 / 零依赖 / 实时 / 可机器消费
## Personas and jobs      # 个人开发者 / 团队负责人 / 自动化脚本
## Information architecture  # 命令分层：live / 报表 / 导出 / 集成 / 配置
## Design principles      # 单一二进制、无遥测、估算必标注、窄终端可用
## Visual language        # 主题、边框、颜色语义（沿用 internal/ui）
## Components             # 表格 / 卡片 / 徽标 / 弹窗
## Accessibility          # 色盲友好、--no-color、宽字符（go-runewidth）
## Responsive behavior    # 四个断点（已有实现，需文档化）
## Interaction states     # loading / error / empty / stale
## Content voice          # i18n 文案规范
## Implementation constraints  # Go 1.26 / pure-Go sqlite / CGO_ENABLED=0
## Open questions
```

**验收**：文档与实现一致（尤其响应式断点、空状态、`(estimated)` 标注）。

---

## 7. 错误分析与可分享报告

### 7.1 错误分类表

参考 sniffly `core/constants.py:29 ERROR_PATTERNS`——**有序 pattern 表，首个命中即归类**，大小写不敏感正则：

| 类别 | 匹配示例（正则片段） |
|---|---|
| 用户中断 | `user doesn't want to proceed`、`\[Request interrupted` |
| 命令超时 | `Command timed out` |
| 文件未读 | `File has not been read yet` |
| 文件已变更 | `File has been modified since read` |
| 内容未找到 | `String to replace not found`、`No such file or directory`、`No module named` |
| 无改动 | `No changes to make` |
| 权限错误 | `Permission denied`、`(?=.*cd to)(?=.*was blocked)` |
| 工具缺失 | `command not found` |
| 其他 | 未命中任何类别 |

要点：
- **顺序敏感**（如"用户中断"必须排在通用错误之前）
- 需要**多条件 lookahead** 的类别用单条正则表达（`(?=.*A)(?=.*B)`）
- 归类后统计各**类别计数 + 占比**，并保留 `timestamp / session_id / model` 明细

### 7.2 错误率

参考 sniffly `core/stats.py:493`：工具错误位于 **user 消息**上，因此需**回看前一条 assistant 消息**来归因；错误率 = `错误数 / assistant 消息数`。

### 7.3 CLI

```
devinmonitor errors                       # 错误分类表 + 占比 + 错误率
devinmonitor errors --session <id>        # 单会话
devinmonitor errors --json
devinmonitor errors --examples 3          # 每类附带 N 条样例
```

### 7.4 可分享报告（脱敏）

参考 sniffly `share.py::_sanitize_statistics`：

- 导出**聚合结果**（分类计数、占比、错误率、时间分布），**不含**原始消息正文
- 必须剥离：本地绝对路径（`log_directory` 类）、会话 ID（可选保留哈希）、用户名、主机名
- 提供 `devinmonitor share --output share.json`（或复用 `export` + 脱敏开关），并输出**脱敏清单**供用户核对
- 无遥测原则：**绝不自动上传**

### 7.5 验收标准

- [ ] 每个类别都有可复现的正则用例
- [ ] 多条件类别（权限/`cd to` + `was blocked`）单条件时不误判
- [ ] 错误率与 assistant 消息数口径一致，有单测
- [ ] 脱敏后报告中不含任何本地绝对路径与会话 ID
- [ ] 默认不联网（可通过静态检查或运行时断言保证）

---

## 8. 落地映射（我们代码里的改动点）

| 需求 | 新增 | 修改 |
|---|---|---|
| §1 限额窗口 | `internal/limit/{blocks.go,burnrate.go,pace.go,cmd.go}` | `internal/model`（Block 类型）、`main.go`（注册命令）、`i18n/{en,zh}.toml` |
| §2 自动化协议 | `internal/status`（或扩展 `internal/analytics`） | `internal/integration/cmd.go`（provenanceLabel 升级）、`internal/integration/{snapshot,status}.go`、`internal/config` |
| §3 状态栏 | `internal/state/statefile.go` | `internal/integration/snapshot.go`（`--write-state`/`--statusline`）、`internal/config`（默认路径） |
| §4 MCP | — | `internal/integration/mcp.go`（resources capability + 4 工具 + instructions） |
| §5 价格覆盖 | `docs/pricing.schema.json` | `internal/model/pricing.go`（覆盖加载/合并）、`internal/integration/settings.go`（pricing 子命令） |
| §6 DESIGN.md | `DESIGN.md` | — |
| §7 错误分析 | `internal/errors/{patterns.go,categorize.go,cmd.go}`、`internal/share/{sanitize.go,cmd.go}` | `main.go`、`i18n/{en,zh}.toml` |

**通用要求**

- 每个新命令都要在 `main.go` 的 `buildOrderedCommands()` 里按现有分组注册（核心命令直接创建，特性命令经 `cli.Register()`/`cli.Get()`）
- 所有面向用户的字符串走 `i18n`，`en.toml` 与 `zh.toml` 同步
- 长驻/交互命令不得在 `bash` 工具里挂起（参考现有 `live` 模式）
- 新增文件需 `gofmt` 干净；不得引入 CGO（保持 `CGO_ENABLED=0`）

---

## 9. 优先级与里程碑

| 里程碑 | 内容 | 依据 |
|---|---|---|
| **M1 自动化基建** | §2 退出码 + provenance + §3 状态栏原子写 | 便宜、见效快，是所有集成的底座 |
| **M2 限额窗口** | §1 全部 + `blocks` 命令 + §2.6 JSON 扩展 | 核心新能力，依赖 M1 的 confidence 体系 |
| **M3 MCP 补齐** | §4 resources + 4 工具 | v0.3.0 刚修好帧兼容，顺势补齐 |
| **M4 定价与文档** | §5 覆盖文件 + schema、§6 DESIGN.md | 低风险打磨 |
| **M5 错误分析** | §7 分类 + 脱敏分享 | 独立价值，可并行 |

## 10. 附：本次对照得到的关键设计约束（一句话版）

1. **窗口切分用严格 `>`**，`sinceStart`/`sinceLast` 任超时即切，且仅在 `sinceLast` 超时时插 gap 块。
2. **原子写必须 pid 唯一临时文件 + rename**，否则并发读者会读到半截 JSON。
3. **估算必须自曝**：非 official 数据一律带 `confidence` 且显示 `(estimated)`。
4. **上游数据一律消毒**（NaN/Inf/超范围/epoch 泄漏），宁可丢弃不可误报。
5. **退出码是接口**，但**必须 opt-in**，不能破坏既有脚本。
6. **错误分类表顺序敏感**，首个命中即归类。
7. **分享必须脱敏且绝不自动上传**（本地优先是我们的品牌承诺）。