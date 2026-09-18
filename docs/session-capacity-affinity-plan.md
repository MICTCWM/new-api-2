# OpenCode / Command 会话容量亲和均衡 — 实施计划

## 1. 背景与问题

OpenCode（type 61）/ Command（type 62）渠道实现了"会话亲和"：同一对话（session）的请求固定路由到同一上游渠道账号，以保持上游会话连续性。

当前问题（用户实测）：

- 同一对话的**同一批请求**全部识别为同一 session，全部复用到同一渠道账号。
- 该账号并发（默认 3）被打满后，后续请求仍全部积压排队到该账号（`selectQueuedAffinityCandidate` 只查配额、不查并发），**其他账号空闲**，没有负载均衡。
- 根因：分发时"瞬时并发"与"会话数量"两个维度混用；会话亲和命中即直返渠道，绕过了 least-loaded 均衡。

## 2. 需求

### 2.1 核心模型

把**并发上限**同时作为**会话容量**：一个渠道最多承载 `GetOpencodeConcurrencyLimit()`（默认 3）个活跃会话。

- 新 session 分配（"门"）：优先选"活跃 session 数最少且未满"的渠道，均匀摊开。
- 已有 session 复用：亲和命中 → 回原渠道，保持上游会话连续性（排队也不换，除非配额耗尽走现有兜底）。
- 全部渠道满员：选并发负载最低的硬塞（宁排队不失败），与现有 least-loaded 兜底一致。

### 2.2 Session 生命周期（含用户补充的续期需求）

| 状态 | 含义 | 占用名额 | 进入/退出条件 |
|---|---|---|---|
| **活跃 Active** | 正在使用，占渠道名额 | 是 | 新 session 分配或保留池命中时进入；每来请求刷新 lastSeen（15s 窗口滑动续期）；15s 无请求 → 降级为保留 |
| **保留 Retained** | 暂时空闲，仅记"会话→渠道"映射 | 否 | 从活跃降级进入；30min 内同对话新请求 → **命中 → 提升回活跃**（复用原渠道 + 重新占用名额 + 续期）；30min 无请求 → 过期删除 |
| **过期 Expired** | 已删除 | 否 | 保留池 TTL 到期；之后同对话请求视为新 session，重新走"门"分配 |

关键规则（用户补充确认）：

1. **命中保留池 → 提升为活跃**：同对话请求在保留期内再来，复用原渠道并重新计入活跃占用。
2. **续期**：session 活跃期间每来一个请求都重置过期时间（15s 窗口与保留池 30min 均滑动顺延），不会因为"快过期"而中途丢失。
3. 保留池里堆积的旧会话不占名额，不影响均衡；命中才恢复占用。

### 2.3 复用时的"搬移 + 排队"机制（用户补充）

保留池命中（复用）时，**先检查原渠道活跃占用是否已满**：

| 原渠道状态 | 行为 |
|---|---|
| 活跃未满 | 正常复用：回原渠道，提升为活跃，续期 |
| 活跃已满 | **不强行加入**。扫描同分组、同模型的其他启用渠道： |
| — 其他渠道有未满 | **搬移**：把该 session 从保留池解绑原渠道，迁到未满渠道，作为活跃会话（完成一次"交替"；上游 session 因渠道变更重新生成） |
| — 全部渠道已满 | **排队**：该 session 提取出来进入队列等待；有渠道释放活跃名额后，按 FIFO 顺序放入新渠道作为活跃 |

设计要点：

- 搬移是一次完整切换：原渠道活跃占用 -1（原 session 保留池记录删除），新渠道活跃占用 +1（写入新 session），保留池记录指向新渠道。
- 排队是有界的（避免无状态 HTTP 网关无限挂起）：等待上限（建议 5~10s），等待期间渠道释放名额则按序放入；**超时则回退到 least-loaded 硬塞（宁排队不失败）——已确认采用此策略，不返回限流错误**。
- 排队队列按（group, model）分桶 FIFO；多实例部署时用 Redis 队列，单实例可用内存队列 + 唤醒通知。

### 2.5 Session 生命周期日志（用户补充）

在**常规日志**分类下新增一类 **Session 日志**，完整记录会话生命周期事件，供管理员追溯"创建/提取/交换/排队"全过程。

**记录事件类型：**

| 事件 | 触发时机 | 关键字段 |
|---|---|---|
| create（创建） | 新 session 分配渠道并落活跃占用 | session_id、conversation_key、渠道、模型、分组 |
| hit（提取/命中） | 保留池命中 → 复用原渠道（提升活跃） | session_id、conversation_key、渠道、cache_hit |
| move（交换/搬移） | 原渠道活跃满 → 迁移到未满渠道 | session_id、原渠道 → 新渠道、模型、分组 |
| queue（排队） | 所有渠道活跃满 → 进入等待队列 | session_id、模型、分组、等待时长、结果（放行/超时硬塞） |
| expire（过期） | 保留池 TTL 到期删除 | session_id、conversation_key、原渠道 |

**展示要求：**

- 日志页"常规"分类下新增 Session 日志条目，可按事件类型/渠道/模型筛选。
- 每条展示：时间、事件类型（含 i18n 中文/英文文案）、session_id、渠道、模型、分组，交换类事件展示"原渠道 → 新渠道"。
- 复用现有日志体系（表/接口/前端页面结构），参考现有 `admin_info.opencode_session_affinity` 的展示方式。

**实现要点：**

- 后端在 `opencode_session.go` / `channel_select.go` 的 session 生命周期各分支（create/hit/move/queue/expire）写入日志；排队超时硬塞在 queue 事件中标注结果。
- 复用现有日志写入链路（`service/log_info_generate.go` 相关），或新增独立日志表/接口，由实现时按现有体系最小侵入决定。
- 前端在现有 usage-logs 页面（`web/src/routes/_authenticated/usage-logs/$section.tsx`）的"常规"分类下接入，复用 `usage-logs-table.tsx` 的筛选/展示能力。

### 2.6 容量 / 窗口参数

| 参数 | 值 | 来源 |
|---|---|---|
| 会话容量 | `OpencodeConcurrencyLimit`（默认 3，范围 1..1000） | 现有配置，Command/OpenCode 共用 |
| 活跃窗口 | 15 秒（滑动） | 常量（建议进设置项，本期先常量） |
| 保留池 TTL | 30 分钟（滑动），原 24h | 设置项化，默认 30min |

## 3. 现状分析（关键代码）

- **保留池现状**：`service/opencode_session.go` 亲和缓存 key = `channelID:sha256(conversationKey)`，value = 32 位随机 session，TTL 24h（`opencodeSessionDefaultTTLSeconds`）。**key 含渠道前缀 → 只能按"已知渠道"查，无法按"对话"查渠道**。这是核心改造点。
- **分发现状**：`service/channel_select.go` `CacheGetRandomSatisfiedChannel` OpencodePool 分支：排队亲和 → 普通亲和 → least-loaded。新 session 实际走 least-loaded（按瞬时并发）。
- **并发计数**：`model/opencode_usage.go` `ConcurrencyCount`（DB）+ `GetCodeProviderConcurrencyUsage`，是瞬时在途请求数。
- **前端图表**：`@visactor/vchart`；现有 `balance-query-dialog.tsx` / `code-provider-usage-override-dialog.tsx` 展示配额/并发数值，无会话占用图。

## 4. 技术方案

### 4.1 数据模型（三块独立存储）

```
① 保留池（改造现有亲和缓存，去渠道前缀）
   key:   new-api:opencode_session:v1:{sha256(conversationKey)}
   value: { channel_id: int, session: "32hex" }
   TTL:   30min（滑动续期，命中即重置）

② 活跃占用（新增）
   key:   new-api:opencode_active_sessions:{channelID}
   value: { sessionHash: lastSeenUnix }   // map，容量 ≤ 并发上限，极小
   TTL:   15s（每次命中刷新），读取时惰性过滤 lastSeen >= now-15s
   → 活跃数 = 过滤后条目数；容量 = GetOpencodeConcurrencyLimit()

③ 会话容量（复用现有）
   operation_setting.OpencodeConcurrencyLimit
```

存储选型：沿用 `cachex.HybridCache`（Redis 开启时落 Redis，保证多实例一致；内存层 LRU）。活跃占用集合值小（≤容量），无内存压力。

### 4.2 分配逻辑（"门"）

`CacheGetRandomSatisfiedChannel` OpencodePool 分支改造，新增独立函数 `selectOpencodeSessionChannel`：

```
1. 解析 conversationKey（现有 deriveOpencodeConversationKey 逻辑）
2. 查保留池①：
   - 命中 → 复用检查（见 2.3）：
     a. 原渠道活跃数② < 容量③ → 复用原渠道（提升活跃，续期）
     b. 原渠道已满 → 扫描同分组同模型其他启用渠道：
        - 有活跃未满的 → 搬移：解绑原渠道，新渠道创建 session，写①指向新渠道 + ② +1，原渠道② -1
        - 全部满 → 进入排队队列（按 group+model 分桶 FIFO，等待上限内轮询②；有释放按序放入新渠道，超时走 least-loaded 硬塞兜底）
   - 未命中（新 session）→ 进"门"：
     a. 候选 = 支持该模型/分组的启用渠道（现有 GetCodeProviderCandidates）
     b. 过滤掉 活跃数② ≥ 容量③ 的渠道
     c. 未满渠道里选 活跃数② 最少的（均匀摊开）
     d. 全部满 → fallback 现有 least-loaded 硬塞
     e. 选中后：在该渠道创建 session，写①（channel_id+session）与②（lastSeen）
3. 已有 session 请求若原渠道配额/状态异常：走现有兜底（清亲和 → least-loaded）
```

### 4.3 改动清单

**后端**
- `service/opencode_session.go`：
  - 保留池 key 去掉渠道前缀，value 改为 `{channel_id, session}`（影响 `ResolveOpencodeSession` 的调用方——调用时机需在渠道确定后，见 4.4）
  - TTL 从常量改为读设置项（默认 30min，新增 `setting/operation_setting` 键，如 `OpencodeSessionRetentionSeconds`）
  - 新增活跃占用读写：`touchOpencodeActiveSession(channelID, sessionHash)`、`countOpencodeActiveSessions(channelID)`
  - 新增"按对话查渠道"：`lookupOpencodeSessionChannel(conversationKey)`
- `service/channel_select.go`：OpencodePool 分支插入"门"逻辑 `selectOpencodeSessionChannel`；全部满员 fallback least-loaded
- `controller/` + `model/`：新增 API 返回各 opencode/command 渠道 `{channelID, name, activeSessions, capacity}` 供前端画图（如 `GET /api/channel/code-provider/session-usage`）
- 新增排队队列：按（group, model）分桶 FIFO（Redis List / 内存队列），含等待超时与唤醒释放逻辑
- 测试（`model/` 或 `service/`）：活跃 15s 过期、命中保留池提升、续期滑动、新 session 均匀分配、满员硬塞、保留池命中回原渠道、**复用搬移（原渠道满→迁他渠道）**、**全满排队按序释放**

**前端**
- 渠道列表/详情用 vchart 新增"会话占用"饼状图/环形图：每渠道 `活跃 session / 容量`
- 新 API 对接 + i18n 文案 + vitest 测试

### 4.4 调用时序注意（实现时必须处理）

`ResolveOpencodeSession` 依赖 `info.ChannelMeta.ChannelId` 已确定。改造后：

- **新 session**：必须先走"门"选渠道 → 再创建 session（会话键 = 新渠道）
- **保留池命中**：由"门"直接返回渠道，后续 `ResolveOpencodeSession` 用该渠道 ID 组装
- 需确认 relay 侧调用顺序：渠道选择（`CacheGetRandomSatisfiedChannel`）是否先于 `ResolveOpencodeSession`；若否，需把会话解析前移或透传已选渠道

### 4.5 兼容与回滚

- 旧缓存 key（含渠道前缀）读不到时按未命中处理，自然过期，无需迁移
- 保留池 TTL 设置项默认 30min，可调回 24h 等价行为
- 改动仅影响 OpenCode/Command 渠道，其余渠道分发路径不动

## 5. 测试计划

| 用例 | 断言 |
|---|---|
| 新 session 均匀分配 | 5 渠道 × 并发 3，10 个新会话 → 每渠道 ≤ 2（活跃数最少的优先） |
| 满员硬塞 | 所有渠道活跃数 = 容量时，新会话仍分配到并发最低渠道，不报错 |
| 保留池命中复用 | 同对话 30min 内再请求 → 回原渠道，且活跃占用 +1 |
| 活跃 15s 过期 | 15s 无请求后活跃数回落，名额释放，新会话可进该渠道 |
| 续期滑动 | 活跃期间持续请求 → 活跃数不因窗口边界骤降 |
| 保留池 TTL 到期 | 30min 后同对话视为新 session，重新走"门" |
| 复用搬移 | 原渠道活跃已满、其他渠道有未满 → session 迁到未满渠道，原渠道 -1 新渠道 +1 |
| 全满排队 | 所有渠道活跃满 → 请求进入 FIFO 队列；渠道释放名额后按序放入；等待超时走 least-loaded 兜底 |
| 前端图 | 会话占用图正确渲染 活跃/容量 占比 |

## 6. 验证与部署

- 后端：`go test ./...`；前端：`bun run typecheck` + `bun run test`
- 子代理 code-reviewer 审查后合并
- 按项目约定构建 Docker 镜像推送阿里云 ACR，tag `v1.0.0-rc.XX` 递增 + `latest`
