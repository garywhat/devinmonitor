# v0.5.0 简报

**发布日期**：待发布
**一句话**：这是一个**正确性修复版本**，不是功能版本。核心是「我们报的数字一直是错的，现在修好了，并且第一次能与账单对上」。

**变更规模**：32 个文件改动，+1863 / −441，另 12 个新文件。

---

## 一、真正的新增能力（只有 2 项）

| # | 新增 | 说明 |
|---|---|---|
| 1 | **MCP dual-era 支持** | 我们的 MCP server 原本停留在 `2024-11-05` 世代。MCP 当前规范 **2026-07-28** 删除了 `initialize` 握手与 `ping`、要求 `server/discover`（MUST）、`resultType`、`ttlMs`/`cacheScope`，而官方兼容矩阵明写 **「Modern client + Legacy server → Fails」**——也就是说**现代客户端连我们会直接失败**。现在同时支持两个世代：`server/discover`、`resultType`、缓存提示、`-32022` 版本错误都已实现，legacy 路径逐字节保留 |
| 2 | **`scripts/verify-against-vendor.sh`** | 新的验收门禁：把我们的计算值与 **Devin 自己的会计口径**逐一对照。这是本次最贵的教训的防护措施——见下 |

> 诚实说明：`web` 命令改为**纯本地**（仅绑 `127.0.0.1`、移除上传/排行榜方向）属于**能力收窄**而非新增，因为它与产品自身「本地优先、无遥测」的原则冲突。

---

## 二、修复：数字正确性（本次最重要的部分）

### 2.1 所有 token/成本数字膨胀约 2.7×

**根因**：Devin 把每个请求写成 **2–3 个 `message_nodes`**，每个节点携带**完全相同的 metrics**，我们把它们全部相加。

讽刺的是代码里早有一行注释写着「Devin 把每条 assistant 消息存两次（streaming + final）」，并据此对**工具调用**做了去重——**却漏了 token 指标**。

| 会话 | 修复前 | 修复后 | Devin 官方口径 | 修复后偏差 |
|---|---|---|---|---|
| phrygian-heaven | 169,598,610 | 53,782,465 | 49,601,468 | 1.65% |
| cloudy-payment | — | 4,103,568 | 4,103,470 | **0.00%** |
| unique-freighter | — | 718,765 | 718,765 | **0.00%** |
| sincere-nutmeg | — | 332,189 | 332,189 | **0.00%** |
| thoracic-equipment | — | 345,156 | 345,156 | **0.00%** |

**6 个可比会话全部吻合，4 个精确到 0.00%。**请求数同样修正（如 23,060 → 7,446，官方 7,321）。

**⚠️ 用户须知**：升级后你的历史数字会**显著变小**。这不是数据丢失，是之前在重复计数。

### 2.2 60% 的 token 落在错误日期

`message_nodes.created_at` **不是消息时间，是批量写入时间**：48,254 行只有 **57 个不同值**，单值覆盖 16,371 行。真实时间在 `chat_message.metadata.created_at`（19,467 个不同值）。

同一会话两种口径：列口径只有 **8 个日期（其中 4 天为 0）** vs JSON 口径 **17 个日期**，而**合计完全相同**——纯粹的归错日期。**7 个整天从日/周/月报表里消失。**

### 2.3 成本被记到错误日期，产生「幽灵行」

Devin 的后台操作会批量覆盖 `last_activity_at`：真实库中 **4 个会话的该值集中在 5 秒内**，而它们的真实活动相隔数周；`clear-sale` 的该值比它最后一条消息**晚 38 天**。

合成用例（真实活动 08-05、cost=42、last_activity 被推到 09-20）：

```
│ 2026-08-05 │  1 │  3 │ free   │   ← 真实活动日成本显示 free
│ 2026-09-20 │  0 │  0 │ $42.00 │   ← 幽灵行：零会话零请求却背着全部成本
$ status → Month: $42.00              ← 8 月花费计入 9 月
$ export csv → duration=3974400.0     ← 46 天（真实 3 小时）
```

免费额度掩盖了它（成本为 0 时走另一条路径），**付费用户会先踩到**。

### 2.4 忽略厂商自己的权威口径

`sessions.metadata.response_dimensions` 存着 Devin CLI `/session-stats` 的那套数字，**就在我们已经读取的列里**，我们却只解析了其中恒为 0 的 credit/acu。

现在解析并用于交叉校验，同时发现**厂商口径排除上下文压缩**——排除后我们从 1.08–1.21× 直接对齐到 **1.000×**。因此把两者拆成独立字段分别标注，而不是二选一。

---

## 三、修复：静默错误（说了但没做 / 做了但不说）

| # | 缺陷 | 现象 |
|---|---|---|
| 1 | **`--data-dir` 被 6 个命令忽略** | `cost`/`budget`/`burn-rate`/`projection`/`top-cost`/`plan show` 指定目录却**静默读取默认库并 exit 0** → 会把本机数字当成另一台机器的 |
| 2 | **`config set locale` 完全无效** | 值被持久化但从不读取。修复过程中**又引入一个回归**（任何 `config set` 都会把中文用户切成英文），已一并修复并加反向验证 |
| 3 | **`status --compact` 是 no-op** | 与默认输出逐字节相同 |
| 4 | **`sessions --save` 是 no-op** | 无 `--sort` 时静默什么都不做 |
| 5 | **`compare` 裸跑必然失败** | `--mode` 默认为需要参数的模式 |
| 6 | **`report` 的 breakdown 忽略窗口** | `--days 1` 却列出 13 个日期；`--days 1/7/30` 逐字节相同；**SVG 与文本行数不一致** |
| 7 | **`tasks` 与 `activities` 互相矛盾** | 同一批数据：`Debugging 5 / Planning 1` vs `Coding 7 (100%)`。旧命令用标题关键词分类，实测**退化成 100% 的默认值**，零信息量 |
| 8 | **ANSI 无条件输出到管道** | 12 个命令污染 `| grep`、`> file`；`NO_COLOR`、`TERM=dumb` 均被忽略 |
| 9 | **`totalTokens` 同名不同义** | `snapshot`/`blocks` 含缓存，`share` 不含 |

---

## 四、修复：死代码

| 项 | 数量 | 说明 |
|---|---|---|
| 未注册的死命令 | 2 | `cmdSessions()`、`cmdProjects()`——**从未被任何路径调用**，是同命令的并行实现（上一轮已删掉同类的 `cmdDaily`） |
| 零调用的 SQL 方法 | 6 | 含读 `prompt_history`（**817 行真实提示词**）与 `tool_call_state`（**7,931 行，含 224 次失败状态**）——数据很有价值，读取它的代码却是死的 |
| `main.go` 净减少 | −133 行 | |

---

## 五、文档如实化

1. **`web` 转纯本地**：移除「可上传」「`export_schema: 1` 供 web 上传」「排行榜」等表述；现在只绑 `127.0.0.1`（`lsof` 实证、外部 IP 连接被拒）
2. **「sessions.db 是唯一输入」已证伪**：`transcripts/*.json`（ATIF-v1.7，17 个文件）真实存在，且**11 个会话只存在于其中**（已被 sessions.db 修剪）。改为如实表述「今日唯一摄入的来源」，并点名 transcripts 为未摄入来源
3. **命令数 58 → 60**（原表漏了 `errors` 与 `share`）
4. **新增竞品对比章节**（中英双语）：对照 ccusage / tokscale / codeburn / sniffly / CCUM / devin-usage，每个声明附 URL，并**如实承认我们没有的**（多 Agent 广度、transcript 摄入、桌面界面、服务端配额真值）
5. **修正一处失效引用**：`DESIGN.md` 引用的 `ui.PctColor` 零调用

---

## 六、验证状态

```
build ✅   vet ✅   gofmt 全仓库干净 ✅
go test ./...  → 20 个包全绿
MCP conformance 45/45（legacy 28 + modern 17）
厂商交叉校验 6/6 吻合（4 个精确到 0.00%）
```

**新增测试**：12 个文件。其中值得一提的：
- `internal/export/report_test.go` —— 该包此前**零测试**；新测试在**修复前的代码上会失败**（已用 stash 验证）
- `internal/reader/messages_dedup_test.go` —— 去重键退化后精确复现 3× 膨胀（`1050 vs 350`）
- `internal/config/locale_default_test.go` —— seed 加回后精确复现语言回归
- `internal/ui/color_test.go`、`internal/integration/mcp_modern_test.go`

---

## 七、已知残留（不阻塞，未修）

1. `live --once` 仍有 28 行硬编码 ANSI，未走新的颜色收口点
2. `--watch` 的清屏序列仍无条件输出到管道

## 八、本版本**明确不做**的事（有据的拒绝）

| 不做 | 理由 |
|---|---|
| OTLP / OTel 导出器 | OTel 的 GenAI / agent / MCP / session 语义约定**全部是 `Development`**——官方定义「SHOULD NOT be used in production… MAY be removed without prior notice」，且新仓库的 Schema URL 章节内容就是 `TODO` |
| 团队配额**强制** | 上游已在服务端解决；LiteLLM 文档证明无数据库的预算强制会 **fail open**。本地只读工具无法强制，因此 `budget`/`alerts` 的措辞必须始终是**警告** |
| 多 Agent 广度 | 已被商品化：tokscale(5,563★) 与 codeburn(11,263★) **已经在读我们的同一个数据源** |
| 状态栏 / GUI / 代理采集 / 托管分享 | 与「单二进制、本地优先」定位冲突；Devin 在服务端运行，代理模型不适用 |

---

## 九、路线图逐条对账（9 项全部完成）

路线图 `docs/roadmap-v0.5.md` 列了 9 项执行清单，现已全部落地：

| 路线图 # | 项目 | 状态 |
|---|---|---|
| 1 | 按 request_id 去重 | ✅ 6 个可比会话与 Devin 官方口径吻合，4 个精确到 0.00% |
| 2 | 六命令 `--data-dir` | ✅ 全部从「静默读错库」改为 exit 1 正确报错 |
| 3 | 时间归属改 `chat_message.metadata.created_at` | ✅ 消除 60% token 错位与幽灵成本行 |
| 4 | 暴露 `response_dimensions` + **标注 ACU/token 单位差异** | ✅ **本轮补齐**：新增 `costBasis`（`acu` / `token_estimate` / `mixed` / `unavailable`），每个成本数字旁印单位标签，`CostBasis.Legend()` **始终**附带「ACU 与 token 是不同单位、不可换算」的警告，混合时显式标 `MIXED UNITS`。`snapshot`/`status`/`share` 的 JSON 与 panel/report 全部覆盖 |
| 5 | MCP dual-era | ✅ conformance 45/45（legacy 28 + modern 17） |
| 6 | **transcript 摄入** | ✅ **本轮补齐**：可见会话 **7 → 18**（11 个从转录补回，它们已不在 sessions.db 里）；`daily` 日期行 17 → 21 |
| 7 | 表面卫生批量修 | ✅ 8/8，另修一个本轮引入的语言回归 |
| 8 | 删除死代码 + **补命令文档** | ✅ **本轮补齐**：README 覆盖度 **23/60 → 60/60**（中英双版），含 12 张分组表 + 10 个重点命令小节 |
| 9 | 竞品对比 + 文档修正 | ✅ 双语对比章节，每个声明附 URL |

### #6 的三个实现要点（都来自实测，不是推断）

1. **`step_id` 在 ATIF 里是数字不是字符串**。声明为 `string` 会让 **17/17 个转录文件 Unmarshal 失败**；因为坏文件被当作「跳过」，**整个第二数据源会静默消失**而工具看起来一切正常。已修 + 回归测试 + 反向验证。
2. **`prompt_tokens` 包含 `cached_tokens`**。`{prompt:98110, cached:97354}` 实际只发了 **756** 个非缓存 token；不做减法，每个转录会话看起来都会把整个缓存上下文当新输入重发一次。而 sessions.db 的 `input_tokens`/`cache_read_tokens` 是分开的两列——**两个数据源的语义不同，这也是不能混算的根本原因**。
3. **粒度不同**：同一会话在 DB 里是 736 个请求，在转录里是 252 个 step（一个 step 包住整个工具循环）。因此架构是 **DB 优先、转录只补 DB 已经没有的会话**，绝不把两者的数字相加。

CLI 会如实告知来源：`11 session(s) recovered from Devin's transcripts (no longer in sessions.db).`

### #4 如何让读者不把不可比的数字拿来比

三层防护，使「忘记标注」无法发生：

1. **数字自带单位**：`Today: $0.00 [token est]`、`[acu]`、`[mixed units]`、`[no basis]`，由共享类型 `CostBasis.Tag()` 生成——**一个消费面不可能在不知情的情况下打印裸的金额**。
2. **含义（含警告）始终可见**：`CostBasis.Legend()` 的每个分支都以不可换算条款结尾，有测试强制。
3. **混合即显式**：ACU 计费与 token 估算被相加时计算为 `mixed`，一行式输出附 `MIXED UNITS: acu and token-estimated costs are not comparable`，`status`/panel/report 打印整段说明；**逐模型行各自带 basis**——模型表正是人们互相比较的地方。

另外 `[estimated] [token est]` 会同时出现：**「这是我们估的」与「这不是 ACU 口径」是两个不同的事实**，现在都可见。
