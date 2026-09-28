# DevinMonitor v0.5 路线图：代码分析 + 竞品调研的结论

**分析日期**：2026-09-28
**方法**：5 条并行工作流（Lead 亲测的时间戳分析 + 4 名 Agent 的数据层审计 / 命令表面审计 / 竞品调研 / 行业趋势），所有结论均附可复现命令或一手 URL。
**标注约定**：`[亲测]` = Lead 独立复现；`[Agent]` = 由 Agent 提供且附可复现命令/来源；`[规范]` = 来自一手规范原文。

---

## 0. 一句话结论

**在我们新增任何功能之前，必须先修 4 个会静默产出错误数字的缺陷；其中第一个让我们的所有 token/成本数字膨胀约 2.7 倍。**
竞品调研的结论同样明确：**广度已被商品化**（tokscale 5.5k★、codeburn 11k★ 已经在读我们的数据源），我们的护城河只能是 **Devin 深度** —— 而这恰好是最需要数字正确的地方。

---

## 1. P0：静默产出错误数字的缺陷

### 1.1 指标重复计数 —— 所有 token/成本数字膨胀约 2.7× `[亲测]`

**这是本次分析最重要的发现。**

同一个请求在 `message_nodes` 里对应 **2–3 个节点**，每个节点都带**完全相同的 `metrics` 对象**，我们把它们全部相加：

| 会话 | 我们的求和（全部节点） | 按 request_id 去重 | 厂商权威值 | 膨胀 |
|---|---|---|---|---|
| phrygian-heaven | 169,598,610 | 53,782,465 | 49,601,468 | **3.42×** |
| cloudy-payment | 10,841,871 | 4,441,876 | 4,103,470 | **2.64×** |
| thoracic-equipment | 690,312 | 345,156 | 345,156 | **2.00×** |
| sincere-nutmeg | 664,378 | 332,189 | 332,189 | **2.00×** |

**去重后与厂商自己的聚合值吻合到 1.00–1.21×**，其中两个会话**完全一致**。

证据链（全部可复现）：
```
# 1952 个带 metrics 的节点，但只有 736 个不同 request_id
sqlite3 "$DB" "select count(*), count(distinct json_extract(chat_message,'\$.metadata.request_id'))
              from message_nodes where session_id='cloudy-payment'
              and json_extract(chat_message,'\$.metadata.metrics') is not null;"
# → 1952|736        （厂商 metadata 的 agent_messages = 727，高度吻合）

# 每个 request_id 对应几个节点
# → 1 个节点 9 例 / 2 个节点 238 例 / 3 个节点 489 例  （9+476+1467 = 1952）
```

**修正方向**：在 reader 层按 `(session_id, request_id)` 去重后再聚合。`request_id` 缺失时回退到 `node_id`。数据层审计员用 `chat_message.message_id` 独立复现了同一结论（同样回到 1.00–1.21×），并指出请求数口径 `AssistantCount` 为 23,060 vs 官方 7,321（**3.15×**），影响 30+ 个消费者——两个键都可用，实现时取其一并写测试固定。
**影响面**：`sessions`、`daily/weekly/monthly`、`models`、`blocks`、`budget`、`snapshot`、`export`、MCP 工具、`trends` —— **几乎所有数字**。
**测试**：以本题材的 fixture 断言「同一 request_id 的多节点只计一次」，并把厂商 `response_dimensions` 作为交叉校验的期望值。
**旁证**：codeburn（11k★）的 `docs/providers/devin.md` 明确记录了去重键 `devin:<sid>:<step_id>` 与「dual `metrics` location」——**它去了重，我们没有**。

### 1.2 `--data-dir` 被六个命令静默忽略 `[亲测]`

```
$ devinmonitor --data-dir /tmp/emptydir top-cost      → exit 0，打印**默认库**的数据
$ devinmonitor --data-dir /tmp/emptydir sessions      → exit 1，"sessions.db not found"
```
受影响：`cost`、`budget`、`burn-rate`、`projection`、`top-cost`、`plan show`
根因：`internal/budget/cmd.go` 第 48/97/125/172/317/363 行调用 `openReader("")`（传空串）。

**危害**：`devinmonitor cost --data-dir /other/machine` 会把本机数字当作那台机器的。**静默错误数据**。

### 1.3 `last_activity_at` 不可信 —— 幽灵成本行与虚高时长 `[亲测]`

Devin 的后台操作会批量覆盖该字段。真实库中 **4 个会话的 `last_activity_at` 集中在 09-27 23:20 的 5 秒内**，而它们的真实活动相隔数周；`clear-sale` 的该值比它最后一条消息**晚 38 天**。

合成 fixture（真实活动 08-05、credit_cost=42、last_activity_at 被推到 09-20）：
```
│ 2026-08-05 │    1 │    3 │ free   │   ← 真实活动日成本显示 free
│ 2026-09-20 │    0 │    0 │ $42.00 │   ← 幽灵行：零会话零请求却背着全部成本
$ devinmonitor status → Month: $42.00        ← 8 月花费计入 9 月
$ export --format csv → duration=3974400.0   ← 46 天（真实 3 小时）
```
根因：`internal/report/report.go` 的 `buildTimeBuckets` 中，**token/请求按消息时间分桶（正确），会话级成本却按 `key(s.LastActivityAt)` 分桶**。该字段在全仓库被约 45 处使用（时长、时间归属、活跃判定）。

免费额度掩盖了成本类问题（`CreditCost == 0` 时走消息估算路径）——**付费用户会先踩到，我们的开发环境看不到**。

### 1.3.1 更严重：`message_nodes.created_at` 是批量写入时间，不是消息时间 `[亲测]`

**这是本次分析中第二重要的发现，并且它更正了 Lead 的一处中间结论。**
Lead 在时间戳分析中一度认为「token/请求按消息时间分桶是正确的」——**实测不成立**，数据层审计员用多样性计数推翻了它，Lead 已独立复核确认：

```
message_nodes.created_at                : 48,254 行 →   57 个不同值（单值覆盖 16,371 行）
chat_message.metadata.created_at        : 48,254 行 → 19,467 个不同值（RFC3339 微秒）
```

`created_at` 列记录的是**写入批次的时间**，不是消息发生的时间。真实逐条时间在 `chat_message.metadata.created_at`（覆盖率 100%）。

同一会话（phrygian-heaven）两种口径的 input token 分桶：

| 日期 | 列口径 | JSON 口径 |
|---|---|---|
| 2026-08-17 | **0** | 2,782,519 |
| 2026-08-21 | **0** | 10,671,590 |
| 2026-08-24 | **0** | 12,001,926 |
| 2026-08-25 | 41,801,238 | 34,382,771 |
| 2026-08-27 | **0** | 5,044,689 |
| **可见日期数** | **8** | **17** |
| **合计** | 169,598,610 | 169,598,610（**相同**） |

即：**总量正确，但 60.1%（129,098,960 / 214,980,096）的 token 落在错误日期，7 个整天从报表中消失**。这解释了此前 `daily` 输出里那些看起来「稀疏但合理」的日期分布——它们不是真实的。

**因此 §1.3 的修复必须扩展**：`daily`/`weekly`/`monthly` 的 token 列同样要改用 `chat_message.metadata.created_at`，而不只是成本列。
审计员同时确认 Lead 关于「活跃时间」的建议 1 仍然成立：7/7 会话的 `MAX(created_at)` 与 `MAX(JSON created_at)` 秒级一致（最后一个写入批次恰好就是最后一条消息），所以**取 max 可用，逐行取值不可用**。

### 1.4 MCP server 属 Legacy 世代，现代客户端会失败 `[规范]`

MCP 当前规范是 **2026-07-28**（`Current`，非草案），它**删除了 `initialize` 握手与 `ping`**、要求 `server/discover`（MUST）、`resultType` 必填、`ttlMs`/`cacheScope` 必填。

官方兼容矩阵原文：

> | Modern client | **Legacy server** | **Fails.** … On stdio, clients SHOULD send `server/discover` first to fail deterministically |

我们的实现（`[亲测]`）：
- `internal/integration/mcp.go:205` 硬编码 `protocolVersion: "2024-11-05"`
- 实现了被删除的 `initialize` / `notifications/initialized` / `ping`
- **没有 `server/discover`**、无 `resultType`、无 `ttlMs`/`cacheScope`
- 未知 resource 的 `-32602` 反而已符合新规范

**同时 README 的「协议行为已被 mcp-conformance.sh 端到端锁定」锁的是已被取代的契约**——脚本断言的正是被删除的 `initialize`/`ping`（`scripts/mcp-conformance.sh`）。

---

## 2. P1：数据完整性与可信度

### 2.1 我们看不见 61% 的会话 `[亲测]`

```
~/.local/share/devin/cli/transcripts/*.json  → 17 个
sessions.db 的会话                            → 7 个
只在 transcripts 里存在                       → 11 个（abiding-style, crocus-tractor, goofy-timpani, hilarious-feet …）
只在 DB 里存在                                → 1 个
```
Devin **会修剪 sessions.db**（本次分析期间该库从 38 个会话降到 7 个），但 **transcripts 保留了 17 个**。我们用 `monthly`/`trends` 看不到的历史，其实还在磁盘上。

transcript 格式 `ATIF-v1.7`，含 `agent`（模型名、工具定义）、`final_metrics`（权威总计）、`steps`（263 条，带时间戳）。

**⚠️ 语义陷阱**：transcript 的 `total_prompt_tokens` **已包含** `total_cached_tokens`（样本中 96.6% 是缓存），而 sessions.db 的 `input_tokens` 与 `cache_read_tokens` 是**分开的**。两个源混用必然双计——`[亲测]` sessions.db 里 `prompt_tokens` 出现 **0 次**，那条「cached 含在 prompt 里」的坑属于 transcript 格式，**不适用于我们当前读的库**（我们的 token 解析本身没问题）。

### 2.2 厂商权威数据就在我们已读的列里，却被忽略 `[亲测]`

`sessions.metadata.response_dimensions` 存着 Devin CLI `/session-stats` 渲染的那套权威维度：
```
agent_messages=727   model="SWE-1.7 Max"   input_tokens=4,103,470
output_tokens=398,754   cached_input_tokens=103,911,300
```
我们只从该列解析了 `total_credit_cost` / `total_acu_cost`（`internal/reader/v1.go:108-109`），**完全忽略 `response_dimensions`**。

这同时解决了行业调研指出的**单位错配**问题：Devin 以 **ACU**（动作复杂度 + VM 时间）计费，与 token 没有定义上的换算关系。我们的 `official/estimated/unknown` 只说明「是不是估算」，**没有说明「是不是另一种单位」**——这是最便宜的信任修复。

### 2.3 `tasks` 与 `activities` 对同一数据给出矛盾结论 `[亲测]`

```
$ devinmonitor tasks       →  Debugging 5 / Planning 1
$ devinmonitor activities  →  Coding 7 (100%)
```
同一批会话、同一套分类学，两个命令互相矛盾。

### 2.4 `report` 的 Daily breakdown 忽略自己的窗口 `[Agent]`

`devinmonitor report --days 1` 打印「Window: last 1 days」却列出从 2026-08-18 起的 **13 个日期**；`--days 1/7/30` 的 breakdown **逐字节相同**。`internal/export/report.go:48` 把全部会话传给 `BuildDaily`。且 `--svg` 与文本不一致（7 根柱 vs 13 个日期）。
**`internal/export` 没有任何测试** —— 这是 export/report/backup/status 背后唯一无测试的包。

### 2.5 同一指标名，两个含义 `[Agent]`

`totalTokens` 在 `snapshot`/`blocks`/`models` 里含缓存（4,515,294,414），在 `share` 里不含（214,980,096）——差值正是 4298.5M 缓存读。同名不同义会直接误导消费者。

---

## 3. P2：表面卫生（低风险、可批量做）

| 问题 | 证据 | 影响 |
|---|---|---|
| `status --compact` 是 no-op | `filterexport/cmd.go:288-291` 两个分支调用同一函数；`diff` 输出完全相同 | 用户以为换了格式 |
| `config set locale` 完全无效 | `cfg.Locale` 只写不读；`SetLocale` 仅由 `main.go` 的 `--locale` 调用 | 设置被持久化但从不生效 |
| `sessions --save` 无 `--sort` 时是 no-op | `shortcuts.go:160` `if save && sortKey != ""` | 帮助文案说「保存当前 flags」（复数） |
| `compare` 裸跑必然失败 | `--mode` 默认 `custom`，而 `custom` 需要的 `--current/--previous` 无默认值 | 默认路径不可用 |
| ANSI 无条件输出到管道 | 全仓库无 isatty / `NO_COLOR` / `TERM=dumb` 处理 | 破坏 `| grep`、`> file` |
| 并发下间歇 `database is locked (261)` | 8 次并行运行 1 次失败；快速连续 3 次失败 | 脚本场景 |
| 44/60 命令无文档；仅 9/60 有 `--json` | README 有 16 个命令示例 | 可发现性；整个 Cost & Budget 组无 `--json` |
| `pricing --output` 是「输出价」而 `--output` 在别处是「目标文件」 | 最严重的 flag 同名不同义 | 危险 |
| `replay`/`timeline` 重新声明了全局 `--data-dir` | 遮蔽 root persistent flag | 行为不一致 |

**冗余与合并**（有证据支撑）：
- **删除**：`activities`（与 `tasks` 矛盾）、`cmdSessions()`(`main.go:409`)、`cmdProjects()`(`main.go:1065`) —— 后两个**从未注册**，是同命令的并行死实现（与我在 v0.4.4 删掉的 `cmdDaily` 同类）
- **合并**：`sessions`→`filter`（同批行，且 `project`/`status` 键含义冲突）；`status`→`snapshot`（`status` 是子集且写**不兼容**的状态文件 schema）；`burn-rate`→`projection`；`top-cost`→`sessions --sort cost --limit N`；`plan`/`currency`→`config` 子命令
- **保留**（经证据反驳了重叠猜想）：`models` vs `tools`（分组维度不同）、`daily --today` vs `24h`（日历日 token 表 vs 滚动 24h 消息图）

**死代码**（`[Agent]` + Lead 抽查确认）：
- 16 个「有 SQL 但方法零调用」：`prompt_history`(**817 行真实提示词**，其中 **8 条 `/btw` 在 message_nodes 里完全不存在**，且 `timestamp` 是唯一可用的逐条提示时钟)、`tool_call_state`(**7,931 行**，带 `status`——**completed 7,707 / failed 224**、`kind`、`inferenceToolName` 20 种；join 键 7,931/7,931 100% 对齐)、`rendered_commits`(0 行)、`app_state`
- **`tool_call_state` 的失败状态我们完全看不到**：当前工具计数从 message_nodes 重算，25,844 元素 vs 7,661 去重 id = **3.37× 虚高**，且 224 次真实工具失败不可见
- **`sessions.cogs_json`**（7/7 非空，27–29KB）：**129 条 cog 条目 / 36 种**，含实际激活的 skill（`skill/ego-browser`…）+ 104 条权限授权，是「本会话真正用了哪些 skill」的唯一来源
- 18 个零引用导出 symbol；`internal/ui/ui.go` 8 个零引用导出 var
- **文档谎言**：`DESIGN.md:129` 引用 `ui.PctColor`，而它零引用

---

## 4. 明确**不应该做**的事（调研结论，附理由）

| 候选 | 结论 | 理由 |
|---|---|---|
| **OTLP / OTel 导出器** | **不做** | `[规范]` OTel GenAI / agent / MCP / session 语义约定**全部是 `Development`**——官方定义是「SHOULD NOT be used in production… MAY be removed without prior notice」，且新仓库的 Schema URL 章节内容就是 `TODO`。Anthropic 自己也用**厂商前缀** `claude_code.*` 而非 `gen_ai.*` |
| **团队治理 / 配额强制** | **不做** | 上游已解决且必须在服务端：Cognition 已有按用户 ACU 上限、IdP→tier 映射、审批流。LiteLLM 文档证明无数据库的预算**强制**会 **fail open**。本地只读工具**无法强制**——我们的 `budget`/`alerts` 措辞必须始终是**警告/阈值**，绝不能是「上限」 |
| **多 Agent 广度**（30–50 个工具） | **不做** | 已被商品化：tokscale 5,563★ 已列出 Devin CLI 的 sessions.db，codeburn 11,263★ 有专门的 `docs/providers/devin.md`。追广度是拿劣势打别人的优势 |
| **状态栏 / tmux / VS Code 钩子** | **不做** | 那些工具的钩子建立在 Claude Code 的开放配置上；**Devin CLI 是闭源的，没有钩子证据**——类比在这里断裂 |
| **原生 GUI / 菜单栏 / 托盘** | **不做** | 与「单二进制、零依赖」定位冲突；且我们真正的差异化在机器可读接口 |
| **代理/网关式采集** | **不做** | ccflare/ccNexus 那套依赖流量经过本地代理；**Devin 在服务端运行**，模型不适用 |
| **托管分享链接 / 公开排行榜** | **不做** | 与我们「本地优先、无遥测」的原则直接冲突（见 §5 分歧） |

---

## 5. 需要你裁决的两个方向性冲突

### 5.1 MCP：升级为 dual-era，还是冻结并声明不支持？

- **升级**（Agent 建议，我同意）：实现 `server/discover` + 同时保留 `initialize` 分支 = dual-era，官方矩阵下两种客户端都能工作。工作量中等，收益是「现代客户端能用」。
- **冻结**：在 README 明确声明只支持 `initialize` 世代。零工作量，但等于放弃 MCP 集成这个卖点，且我们 README 现有的「已锁定」表述是**不准确的**。

### 5.2 `web` 命令 + `export_schema: 1`「可上传」方向

这是产品**唯一一处与自身「本地优先、无遥测」原则矛盾**的地方，且同时与厂商的导出能力、以及第三方本地看板重叠。
建议：要么降级为明确标注的**非目标**，要么改为**纯本地 HTML**（我们已有 `share --format html`）。

---

## 6. 护城河：我们该守住什么 `[Agent，附来源]`

调研确认，以下能力**厂商自己的维度不暴露**，也是我们唯一值得加深的地方：

- **延迟与吞吐分布**：TTFT / tokens-per-sec 的 p50 / p95（Devin CLI 的用量维度与 Claude Code 的 `claude_code.*` 指标**都不含**延迟或吞吐）
- **finish reason 分布**、**上下文增长与缓存命中时间序列**
- **500ms 增量轮询**与四层自适应 TUI
- **离线可用**（断网完全可用）
- **机器可读接线面**：Prometheus / `snapshot --json` / MCP resources（我们 8 工具 4 资源，领先 splitrail 的 6、codeburn 的 2）
- **可审计的脱敏分享**（比 sniffly 的托管公开画廊更安全——Devin 会话含源码路径与提示词）

**定位威胁（须记录）**：codeburn 用 **transcripts 作主源、sessions.db 仅作补充**，与我们的架构相反——而 §2.1 证明**它是对的**（我们看不见 61% 的会话）。第三方 `Meltemi-Q/devin-usage` 已逆向出 Devin 的云端配额接口（`codeium.com GetUserStatus`、`api.devin.ai/v3` 的 `acus_consumed`）。

---

## 7. 建议的执行顺序

| 序 | 项目 | 类型 | 理由 |
|---|---|---|---|
| 1 | **按 request_id 去重** | P0 修复 | 所有数字都错 2.7×，且改动集中（reader 聚合层） |
| 2 | **六个命令的 `--data-dir`** | P0 修复 | 一行级修复 ×6，静默读错库 |
| 3 | **时间归属全部改用 `chat_message.metadata.created_at`** | P0 修复 | 同时消除幽灵成本行（§1.3）与 60% token 错位（§1.3.1）；一并修正约 45 处时长 |
| 4 | **暴露 `response_dimensions` + 标注 ACU/token 单位差异** | P1 功能 | 数据已在已读列里；直接解决「与账单不符」的信任问题 |
| 5 | **MCP dual-era**（待裁决） | P1 兼容 | 现代客户端当前必然失败 |
| 6 | **transcript 摄入**（作为**补充**，不混算） | P1 功能 ✅ **已完成** | 补回 61% 的会话；须先解决 §2.1 的语义陷阱 |
| 7 | 表面卫生批量修（§3 表） | P2 | 低风险，可一次提交 |
| 8 | 删除死代码 + 补文档（45 个命令） | P2 ✅ **已完成** | 降低维护面 |
| 9 | 竞品对比章节 + 修正不准确的文档表述 | P2 文档 | **已完成**：「sessions.db 是唯一输入」原句在 `DESIGN.md:9`（非 README），已改为「今日唯一摄入的来源」并点名 transcripts 为未摄入来源 |

**每一步的验收都必须包含一条「与厂商 `response_dimensions` 交叉校验」的断言**——这次分析最贵的教训是：**我们没有任何一处把自己的数字和上游的权威数字对过**，所以膨胀 2.7 倍能活到今天。

---

## 8. 多 Agent 协作的流程教训（本轮实际踩到）

### 8.1 Lead 的编辑落在所有声明的写入范围之外

本轮两名成员**先后独立**报告 `README.md` / `README.zh-CN.md` 被「越界写入」，并互相怀疑。
**两次的编辑者都是 Lead 本人**：在发现新构建下 export 已带 `cost_basis`、而 README 那句
「export 只保留分离分量」已经失真之后，Lead 直接改了这两个文件。

这暴露了任务分派里的一个结构性盲点：**写入范围是给成员划的，Lead 自己不遵守也就没人拦**。
当时 docs-web 正处在两次编辑之间（task-10 已完成、正在对齐字段契约），**只差一秒就会撞上**。
后果会表现为 `FS_STALE_VERSION` 或静默丢失一方的内容。

**下次应遵守的规则**：Lead 的勘误也走同一个单写者规则——要么在分派时给文档所有者保留
一个「Lead 勘误」的通道并告知，要么由 Lead 等待所有者停止后再改。本轮的教训不是
「内容错了」（内容是核验过的），而是**流程在事实成立前就已经依赖运气**。

### 8.2 成员会基于旧二进制下结论

两名成员各自报告了一个「缺口」，核验后**两个都是工具假象、不是缺陷**：

| 报告 | 真相 |
|---|---|
| 「README 只覆盖 59/60 个命令」 | 提取脚本的正则要求首字母为小写，漏掉了数字开头的 `24h`；实际 60/60 |
| 「export 没有 costBasis」 | 检查的是**旧二进制**；重新构建后 `cost_basis` 正常输出（18 行全部带标签） |

**下次应遵守的规则**：任何「某字段/某功能缺失」的结论，必须**先重新构建再验证**，
并在报告里附上二进制的时间戳或构建命令。用旧产物得出的缺失结论会浪费一轮排查。

### 8.3 静默跳过是第二数据源消失的原因

转录摄入的实现里，坏文件被设计为「跳过而非致命」——这是对的（一个未写完的文件不该
毁掉整份报告）。但**跳过的计数当时没有任何出口**，于是当 17/17 个文件因 `step_id` 类型
不符全部解析失败时，工具看起来完全正常，第二数据源**整体消失**。

**下次应遵守的规则**：任何「跳过并继续」的分支都必须有可观测的出口（计数、警告或
状态字段），否则它就是「静默失效」的又一个入口。
