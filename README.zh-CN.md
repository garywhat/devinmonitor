# DevinMonitor

**[Devin CLI](https://windsurf.com/devin) 的 Token 与成本监控工具。**

DevinMonitor 读取 Devin CLI 的本地会话数据库，提供实时监控、
用量报表和成本追踪——还包含其他监控工具没有的 Devin 专属指标
（TTFT、tokens/sec、finish-reason 分布、上下文增长曲线、子代理统计）。

[English](README.md)

---

## 实时面板

`live` 命令渲染一个实时 bubbletea TUI，默认每 500 毫秒轮询一次
`sessions.db`，完整展示当前会话的全景视图：

![实时面板](docs/images/live_dashboard_zh.png)

**各面板说明：**
- **Tokens** — 输入 / 输出 / 缓存读 / 缓存写总量，实时生成
  **速率**（tok/s，基于 60 秒滚动窗口汇总并发请求），以及平均值。
- **Context** — 当前上下文大小 vs. 模型窗口，填充进度条，
  平均 / 峰值 / 剩余 token 数，缓存写指示。
- **Status** — 会话成本、请求数（含子代理数）、时长、平均请求
  耗时、TTFT / 总时 p50、工具调用总数。
- **Request stream** — 最近请求的滚动列表，含 TTFT、tok/s、
  耗时和 finish reason，以及带 p50/p95 的 TTFT 趋势线。
- **Context growth** — 全会话上下文大小历史，含缓存命中率。
- **Latency** — TTFT / 总时 / tok/s 百分位和 finish-reason 分布条。
- **Tools** — 各工具调用次数及横向条形图，包含 `run_subagent`
  和 `read_subagent` 调用。

轮询是增量的：首次加载后只重新读取消息数发生变化的会话，因此即使
在拥有数万条消息的数据库上，短间隔刷新依然轻量。

**`--interval`** 以**毫秒**设置刷新间隔（默认 `500`，最小 `100`）。
未显式传入该参数时使用配置项 `refreshInterval`；显式传入的 flag
始终优先。扩展面板（`--light` / `--once` / `--theme`）使用同一间隔。

## 报表

所有报表命令渲染风格统一的表格（灰色边框 + `TOTALS` 合计行）。
列宽自适应终端——宽终端下列自动扩展，窄终端下优雅截断。

### `sessions` — 会话列表

![会话列表](docs/images/sessions_zh.png)

### `session <id>` — 会话详情

展示元数据、token 明细、子代理调用列表（含 profile、任务、
前台/后台、完成状态），以及各工具调用次数明细。

![会话详情](docs/images/session_detail.png)

### `daily` / `weekly` / `monthly` — 时间序列报表

按日 / 周 / 月汇总 token、请求、子代理和成本，并列出所用模型。
使用 `--breakdown` 可追加每模型子表。`weekly` 支持
`--start-day monday|sunday|...`。

![按日报表](docs/images/daily_zh.png)

![按周报表](docs/images/weekly.png)

![按月报表](docs/images/monthly.png)

### `models` — 模型分析

每模型的请求数、token（输入 / 输出 / 缓存读 / 缓存写）、
成本、成本占比和平均生成速度。

![模型统计](docs/images/models_zh.png)

### `model <name>` — 模型详情

首次/末次使用、活跃天数、token 总量、成本、日均和会话均值、
p50/p95 TTFT 和总时、截断率，以及该模型的各工具明细。

![模型详情](docs/images/model_detail.png)

### `projects` — 项目用量

按工作目录的项目名分组，展示会话数、请求数、token、成本和
所用模型。

![项目统计](docs/images/projects.png)

### `agents` — 子代理使用统计

按 profile 分组的详细子代理用量：总调用数、涉及会话数、
前台/后台拆分、完成数、`read_subagent` 等待数、平均/最大时长、
平均/最大任务长度、平均/最大输出长度。

![子代理统计](docs/images/agents_zh.png)

### `export [report_type]` — 机器可读报表

`export` 把任意报表以四种格式写入文件或标准输出，报表类型可省略
（默认 `sessions`）：

```bash
devinmonitor export                       # sessions，完整 JSON（schema 1）
devinmonitor export daily --format csv
devinmonitor export weekly --format markdown --output weekly.md
devinmonitor export models --format html  > models.html
devinmonitor export projects --format json
```

| 报表类型 | 内容 |
|---|---|
| `sessions`（默认） | 完整标准化文档（`export_schema: 1`），格式稳定，适合上传 Web |
| `daily` / `weekly` / `monthly` | 时间分桶，含成本、token、请求数、缓存命中率 |
| `models` | 各模型总量、成本、TTFT 与吞吐百分位 |
| `projects` | 按工作目录聚合的项目用量 |
| `agents` | 按 profile 分组的子代理用量 |

格式：`csv`、`markdown`、`html`、`json`。报表类型或格式非法时以
非零退出并给出用法提示。`--detailed` 仍会在 `sessions` 文档中内嵌
逐请求明细。

## MCP 服务

`devinmonitor mcp` 以 stdio 运行 Model Context Protocol 服务，让
MCP 客户端（Claude Desktop、Cursor 等）直接查询本地用量数据。它
同时支持两种 stdio 帧格式——规范要求的 `Content-Length` 帧与纯
换行分隔 JSON——并支持 JSON-RPC 批量请求、通知与 `ping`。

暴露的工具：

| 工具 | 用途 |
|---|---|
| `get_sessions` | 会话列表，含成本与 token 用量 |
| `get_session` | 按 ID 查询单个会话详情 |
| `get_cost_summary` | 汇总成本（今日 / 本周 / 本月 / 总计） |
| `get_alerts` | 预算阈值与空闲会话告警 |
| `get_blocks` | 按 5 小时计费窗口分组的用量 |
| `get_limits` | 当前窗口状态、节奏与预测 |
| `compare_periods` | 对比两个时间段（默认环比上月） |
| `list_reports` | 可用报表类型与导出格式 |

协议行为由 `scripts/mcp-conformance.sh` 端到端锁定：它用规范帧格式驱动真实二进制，
覆盖 27 项用例（帧正确性、通知静默、批量、resources、全部工具）。

另外提供 `devinmonitor://` 协议下的**只读 resources**，客户端无需
调用工具即可拉取摘要：

| Resource | 内容 |
|---|---|
| `devinmonitor://summary` | 成本、token、请求与会话总量，含成本来源标注 |
| `devinmonitor://models` | 各模型用量表，含 token 占比 |
| `devinmonitor://blocks` | 当前计费窗口，含燃烧率与预测 |
| `devinmonitor://alerts` | 按类别分组的当前告警 |

```json
{
  "mcpServers": {
    "devinmonitor": { "command": "devinmonitor", "args": ["mcp"] }
  }
}
```

## 安装

```bash
go install github.com/garywhat/devinmonitor@latest
```

或从 [Releases](../../releases) 下载预编译二进制。

或从源码编译：
```bash
git clone https://github.com/garywhat/devinmonitor.git
cd devinmonitor
go build -o devinmonitor .
```

## 用法

```bash
# 实时面板（需要 TTY）
devinmonitor live

# 更快刷新：200 毫秒（毫秒为单位，最小 100）
devinmonitor live --interval 200

# 会话列表
devinmonitor sessions

# 会话详情
devinmonitor session fragrant-hunter

# 按日用量，含每模型明细
devinmonitor daily --breakdown

# 按周报表，周一起始
devinmonitor weekly --start-day monday --breakdown

# 按月报表
devinmonitor monthly

# 模型分析
devinmonitor models

# 模型详情
devinmonitor model glm-5-2

# 项目用量
devinmonitor projects

# 子代理使用统计
devinmonitor agents

# Prometheus 指标端点
devinmonitor metrics --addr :9101

# 导出标准化 JSON
devinmonitor export --detailed > usage.json

# 导出指定报表类型：sessions|daily|weekly|monthly|models|projects|agents
devinmonitor export weekly --format markdown --output weekly.md

# MCP 服务（stdio，供 Claude Desktop / Cursor 使用）
devinmonitor mcp
```

### 全局参数

```
--data-dir string   Devin 数据目录（默认自动探测）
--locale string     语言 en/zh（默认从系统探测）
```

### 实时面板快捷键

```
q          退出
r          切换到下一个会话
1-4 / Tab  跳转到分区（紧凑模式）
L          切换语言（en <-> zh）
```

扩展面板（`--light` / `--theme`）额外支持：

```
s          设置面板（主题、refreshInterval、预算、货币）
?          帮助浮层
Ctrl+P     命令面板
Enter      会话详情弹窗
m          模型分解弹窗
|          分屏（列表 + 详情）
v          列表 / 卡片视图切换
t          切换时间窗口（今天 / 本周 / 本月 / 全部）
l          实时日志尾部（最近消息）
```

## 升级说明 — v0.3.0

- **`live --interval` 单位改为毫秒**（原为秒）。旧的 `--interval 3`
  表示 3 秒，请改用 `--interval 3000`。最小值为 `100`，默认 `500`。
- **`refreshInterval` 配置项现在真正生效**（毫秒），在未传入
  `--interval` 时使用；其默认值由 `3` 改为 `500`。
- **`--data-dir` 成为权威路径**：指向不含 `sessions.db` 的目录时
  会明确报错退出，不再静默回退到自动探测的数据库。
- **`compare --mode` 拒绝非法值**，不再静默当作 `custom` 处理。
- `export` 支持可选报表类型（`export weekly --format csv`）；不带
  参数运行的行为保持不变。
- 子代理时长以会话生命周期为上限，不再出现荒谬的多年数值。

## 响应式 TUI

实时面板根据终端尺寸自适应，共四个断点：

| 断点 | 尺寸 | 布局 |
|------|------|------|
| **Full** | >= 120 列, >= 28 行 | 完整 4 行面板 |
| **Compact** | 80-119 列 | 双列 + 标签视图 |
| **Mini** | < 80 列 | 单列流式（窄窗口 / termux） |
| **Tiny** | < 6 行 | 单行滚动条（tmux 分屏） |

窗口缩放通过 bubbletea 的 `WindowSizeMsg` 即时响应。

## 数据来源

DevinMonitor 从 Devin CLI 的数据目录读取 `sessions.db`：
- **Linux**: `~/.local/share/devin/cli/sessions.db`
- **macOS**: `~/Library/Application Support/devin/cli/sessions.db`
- **Windows**: `%APPDATA%\devin\cli\sessions.db`

可用 `--data-dir` 或 `DEVIN_DATA_DIR` 环境变量覆盖。

连接采用只读 + WAL + `query_only` 模式，不会阻塞 Devin CLI 的写入。

### Schema 适配

Devin CLI 的 SQLite schema 是内部实现细节，可能随版本变化。
`reader` 包将 schema 相关的 SQL/JSON 解析隔离在版本检测的适配器
之后。当 Devin CLI 变更 schema 时，只需新增一个适配器
（如 `v2.go`）——报表和 UI 不受影响。

## 成本计算

| 层级 | 来源 | 使用条件 |
|------|------|----------|
| 权威 | `sessions.metadata.total_credit_cost` / `total_acu_cost` | 非零（付费模型） |
| 用户覆盖 | `<配置目录>/pricing.json` | 已为该模型设置时（优先于内置表） |
| 估算 | 内置 token x 价格表 | credit 为零（免费模型） |

免费模型（如 `glm-5-2`）在成本列显示 `free`。

定价完全在本地解析，读取时**从不联网**。若将来引入远程价格源，它必须**写入** `pricing.json`，而不是在生成报表时被查询——这样工具离线可用，「无网络」不变式也得以保持。

### 远程价格目录（需手动开启）

`devinmonitor pricing fetch` 会下载公开的模型价格目录（默认 OpenRouter 的模型
列表）并缓存到 `<配置目录>/pricing.cache.json`，供离线使用。三个关键性质：

- **默认关闭**：除非你执行 `pricing fetch`，或设置 `config set pricingAutoFetch true`
  （最多每 24 小时刷新一次），否则不会访问网络。
- **只下载**：请求是一个获取公开价格列表的裸 `GET`，不发送任何用量数据、模型名或路径。
- **读取时绝不联网**：价格始终从本地文件解析。

优先级为 **你的 `pricing.json` > 拉取的缓存 > 内置表**，因此刷新永远不会覆盖你
手动设置的价格——缓存单独成文件正是为此。`pricing cache` 查看缓存状态，
`DEVINMONITOR_OFFLINE=1` 可完全禁止拉取。

## Prometheus 指标

`metrics` 命令启动一个 HTTP 服务（默认 `:9101`），暴露
Prometheus 格式的 gauge 指标：

- `devinmonitor_sessions_total`
- `devinmonitor_requests_total`
- `devinmonitor_input_tokens_total` / `devinmonitor_output_tokens_total`
- `devinmonitor_cache_read_tokens_total` / `devinmonitor_cache_write_tokens_total`
- `devinmonitor_cost_total`
- `devinmonitor_model_*` — 每模型明细
- `devinmonitor_project_*` — 每项目明细

用 Prometheus 抓取或直接 curl：
```bash
devinmonitor metrics &
curl http://localhost:9101/metrics
```

## 架构

```
sessions.db -> Reader（schema 适配器）-> 标准化 model 类型
                                       |-- Report（sessions/daily/weekly/monthly/models/projects/agents）
                                       |-- Live（bubbletea 面板）
                                       |-- Export（稳定 JSON，供 web 上传）
                                       |-- Metrics（Prometheus 端点）
```

导出格式（`export_schema: 1`）独立于 Devin 内部 schema，
设计为未来 web 分享（token 排行榜、烧钱速率对比等）的稳定契约。

## 平台与语言

- **多架构**: linux / darwin / windows x amd64 / arm64（单二进制，无 CGO）
- **i18n**: 英文 + 中文，从系统 `LANG` / `LC_ALL` 自动探测
- **CJK 渲染**: 通过 go-runewidth 正确处理字符宽度

## 技术栈

- **Go** — 单二进制，无运行时依赖
- **bubbletea + lipgloss** — 响应式 TUI 框架
- **modernc.org/sqlite** — 纯 Go SQLite（无 CGO，便于交叉编译）
- **cobra** — CLI 命令框架

## 许可证

MIT
