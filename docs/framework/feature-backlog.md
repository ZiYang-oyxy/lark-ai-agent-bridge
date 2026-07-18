# Feature Backlog · 特性清单(务实近期迭代版)

> 本文档是一份**面向近期迭代的特性清单**,不是企业级完整路线图。
>
> 定位:贴合当前 `lark-ai-agent-bridge` 的实际体量(单 agent、单平台、~6.6k 行、早期收敛完成),只列**近期可落地、投入产出比高**的特性,并按「价值高 / 成本低」排序。
>
> 参考来源:
> - **主线现状**:本仓 `internal/` 各包 + `docs/framework/architecture.md`。
> - **lcab** = `reference/lark-coding-agent-bridge`(TS,飞书专精,与主线模型同构,是主要平移来源)。
> - **cc-connect** = `reference/cc-connect`(Go,多 agent 多平台标杆,提供若干轻量小件)。
> - **gist** = 一份企业级架构对比建议(PostgreSQL 事实源 / mTLS 分布式 Runner / DLP / K8s Job)。

## 结论先行

主线现状是一个**能跑通的最小可用体**:单 agent(claude)、纯内存、按 chat/topic 串行、one-shot + `--resume` 续接、CardKit 流式卡片。它**不缺骨架,缺的是「重启不丢 + 谁能用 + 能发图」这三类生产必需能力**。

gist 的企业级方案对当前体量**严重过度设计**,近期一律不进主清单,只作远期备注(见文末)。近期该做的绝大多数能从 **lcab 近乎平移**(模型同构),cc-connect 提供几个轻量补充小件。

## 主线现状盘点(作为清单的基线事实)

| 维度 | 现状 | 关键位置 |
|---|---|---|
| Agent | 仅 claude,one-shot 子进程(非常驻) | `internal/agent/agent.go:42-56`,`ParseKind` 不识别 codex `agent.go:24-30` |
| 会话 | 纯内存 `map`,重启全丢;有内部 `--resume` 续接,无面向用户的 `/resume` | `internal/session/session.go:64-70`,`agent/agent.go:52-54` |
| session key | `{Agent, ChatID, Thread}` | `internal/session/session.go:11-22` |
| 鉴权 | **完全无鉴权** + 硬编码 `--dangerously-skip-permissions` | `internal/agent/agent.go:51` |
| 去重 | 内存 map + TTL,重启丢失 | `internal/feishu/dedupe.go:9-31` |
| 并发 | 按 session key(chat/topic)串行,不同 key 并行;队列无上界 | `internal/session/session.go:98-153` |
| 卡片渲染 | 已有 reducer 中间层(`agentCardStream`):流式 append + 终态 replace | `internal/bridge/stream_card.go` |
| 附件/图片 | **不支持**,只解析 text/post | `internal/feishu/sdk_message.go:117-186` |
| model | bridge 不可控(由 wrapper profile 决定),`--effort low` 硬编码 | `internal/agent/agent.go:51` |

---

## P0 — 生产必需,先做

### P0-1 · 会话持久化 + 重启恢复 + `/resume`

- **问题**:会话态、prompt 历史、去重表全在内存(`internal/session/session.go` 纯 `map`),进程重启全丢。主线 backlog 自列的第一条。
- **方案**:JSON 快照 + 原子写(临时文件 rename)+ `--resume`,**不上 SQLite**。
  - cc-connect 的会话持久化实际也是 JSON 快照(`core/session.go` 的 `sessionSnapshot` + `AtomicWriteFile`),**未用 SQLite**——可直接照此思路。
  - session key 结构升级参考 lcab `session/catalog.ts`:`scopeId + agentId + cwdRealpath + policyFingerprint`,避免换目录/换权限后错误复用旧会话。
  - 面向用户的 `/resume` 参考 lcab `commands/index.ts` `handleResume` + 一次性 nonce 选择机制(10 分钟有效,绑 catalog identity)。
- **平移索引**:cc-connect `core/session.go`(JSON 快照/原子写/`PastAgentSessionIDs` 归属追踪);lcab `session/catalog.ts`、`session/store.ts`、`commands/index.ts:handleResume`。
- **成本**:中低。主线已有内部 `ClaudeSessionID` 续接,补的是「落盘 + 恢复 + 列表选择」。

### P0-2 · 访问控制(owner / allowlist / invite)

- **问题**:当前**完全无鉴权 + 硬编码 skip-permissions**,任何能 @bot 的人都能在本机执行命令。安全红线。
- **方案**:纯函数决策 + owner 定期刷新 + fail-closed。
  - lcab `policy/access.ts` 几乎可平移:`isCreator / canUseDm / canUseGroup / canRunAdminCommand`,名单来自 `profile.access.{allowedUsers, admins, allowedChats}`。
  - `owner.ts`:定期(30 分钟)拉 bot owner,**`unknown` 时 `isCreator` 一律 false(fail-closed)**。
  - invite 体系:lcab `/invite user|admin|group`、`/remove`(`commands/index.ts:handleInvite`)。
- **校验触点**:入站 intake、卡片回调、命令三处都要加(参考 lcab `bot/channel.ts` intake、`card/dispatcher.ts`、`commands/index.ts` 的 `ADMIN_COMMANDS`)。
- **平移索引**:lcab `policy/access.ts`、`policy/owner.ts`、`commands/index.ts`。
- **成本**:中。

### P0-3 · 文件 / 图片输入

- **状态(2026-07-18)**:✅ 个人版已完成并通过真实飞书 E2E。支持 JPEG/PNG/WebP/GIF 与 `.txt/.md/.json/.csv`;拒绝 PDF/DOCX/audio/未知二进制、内容伪装和超限文件。缓存使用流式 hard limit、SHA-256 内容寻址、`0600/0700` 权限、TTL/quota GC。
- **真实平台边界**:飞书 `post` 支持内嵌 `img`,但不接受 `{tag:file}`。群聊图片测试使用 `@bot + img` post;文件消息必须在用户与 bot 的 P2P chat 中以原生 `file` 消息发送。文件资源下载的 transport MIME 可能是 `application/octet-stream`,CSV 实测为 `application/x-xls`;bridge 只在扩展名已进入 allowlist 时接受这些声明,随后仍强制执行内容 sniff。
- **证据**:`.cache/evidence/0bde2af/media-real/summary.md` 的 `media_attachment_only`、`media_images`、`media_text_files`、`media_partial`、`media_rejected` 全部通过;该目录为 gitignored 本地证据。

- **问题**:只解析文本(`internal/feishu/sdk_message.go`),图片/文件消息直接丢。高频需求。
- **方案**:流式下载 + 内容级缓存 + 白名单过滤。
  - lcab `media/cache.ts`:`downloadResourceToFile` 流式落盘、sha256 内容 hash 命名、LRU(`enforceCacheMaxBytes`)。
  - lcab `media/attachment.ts`:数量/大小/mime 白名单(默认 maxCount 10、单文件 25MB、单次 100MB)。
  - 传给 CLI:claude 侧图片走 prompt 路径引用,codex 侧走 `--image`(见 P2-1)。
- **平移索引**:lcab `media/cache.ts`、`media/attachment.ts`、`bot/run-flow.ts:152-158`。
- **成本**:中。需接飞书 `im.v1.messageResource.get` 下载 + 本地缓存管理。

---

## P1 — 明显提升质量,随后做

### P1-1 · 去重/防重放持久化 + 有界队列背压

- **问题**:去重(`internal/feishu/dedupe.go`)纯内存,重启后可能重放未 ack 的旧消息;队列无上界。
- **方案**:两个 cc-connect 自包含小件直接抄:
  - `StartTime` 防重放:启动前 ~2s 的消息丢弃(cc-connect `IsOldMessage()`)。
  - 有界队列:`maxQueued` 上限,busy 时消息合并进下一批、**不中途写 stdin**(cc-connect `defaultMaxQueuedMessages`、`pendingMessages`)。
  - 可选:lcab per-scope debounce(`bot/pending-queue.ts`,p2p 250ms / 带附件 600ms)。
- **平移索引**:cc-connect `core/dedup.go`、`core/engine.go`(watermark / 队列);lcab `bot/pending-queue.ts`。
- **成本**:低。均为 <100 行小件。

### P1-2 · 可选 model / effort 指定

- **问题**:model 完全由 wrapper profile 决定、bridge 不可控(AGENTS.md 已记为已知风险);`--effort low` 硬编码在 `agent.go:51`。
- **方案**:给 `BuildOneShotCommand` 加可选 `--model` / `--effort`,`default` 表示不传;经 `/config` 卡片选择。
- **平移索引**:lcab `agent/models.ts`(`supportedModels` / `resolveModelArg`)、`card/config-card.ts`。
- **成本**:低。

### P1-3 · 回复展示模式可配 + typing/reaction 反馈

- **问题**:当前卡片策略固定。
- **方案**:
  - lcab `ReplyDisplayMode`:`append` / `latest-card` / `append-clean-card`(`config/schema.ts:71`,分发在 `bot/channel.ts`)。
  - cc-connect preview 双门限节流:interval + minDelta(`core/streaming.go`),freeze/unfreeze/discard/finish 降级完备。
  - reaction 表示「处理中」:lcab `bot/message-work-reactions.ts`。
- **平移索引**:lcab `card/run-state.ts`(主线已有对应的 `stream_card.go` reducer,扩展策略即可)、cc-connect `core/streaming.go`。
- **成本**:低中。主线已有 reducer 层,扩展即可。

---

## P2 — 按需再做

### P2-1 · Codex 适配

- lcab `agent/codex/adapter.ts` 全套:`buildCodexArgs`(`exec --json --sandbox … resume <threadId> --image …`)+ `CodexJsonlTranslator`(codex jsonl → 统一 AgentEvent)+ `CODEX_HOME` 处理。
- 主线当前 `ParseKind` 不识别 codex(`agent.go:24-38`)。
- **成本中,优先级取决于是否真要用 codex。**

### P2-2 · 人机审批闭环(收紧 skip-permissions)

- 让危险工具走审批卡片,替代当前全量 `--dangerously-skip-permissions`。
- cc-connect 方案:stdio `control_request/response` 协议(`--permission-prompt-tool stdio` + `--input-format stream-json`)+ 三档审批(单次 / 本会话全放 `/yolo` / 工具白名单 `/allow <tool>`)+ `sync.Once` resolve channel + 陈旧回调防护。
- **平移索引**:cc-connect `core/engine.go`(`pendingPermission`)、`agent/claudecode/session.go`(`RespondPermission`)。
- **成本中高**:依赖把 agent 调用从 one-shot 改造为 stdin 常驻,是较大的模型变更。

### P2-3 · metrics / 可观测

- 主线已有 `internal/audit`,补 metrics(run 时长、并发、失败率、token/cost)。
- **成本低,价值取决于是否上线运营。**

---

## 明确不做(近期)

| gist 建议 | 为何近期不做 |
|---|---|
| PostgreSQL 作企业事实源 | 单机主线用 JSON 快照足够,上 PG 是数量级的复杂度 |
| mTLS 分布式 Runner / 集中控制面 | 单机单平台无此拓扑,纯负担 |
| Runner 容器/VM 隔离、DLP、Secret 动态下发 | 面向多租户不可信代码,主线场景用不上 |
| cc-connect 插件化内核(registry + build tag) | 单 agent 单平台无 N×M 组合,过度抽象 |
| Capability Manifest 握手协议 | cc-connect 实际也只是 Go optional interface 探测,无完整握手 |

> **模糊地带**:cc-connect 的 runas `sudo -n -iu` 降权片段(`core/runas.go`),仅当主线未来要跑不可信代码/多租户才值得移植,否则暂缓;其泄漏审计探针(`core/runas_audit.go`)近期不做。

## 推荐落地顺序

`P0-1(持久化)→ P0-2(鉴权)→ P0-3(图片)` 是一条自洽主线:先「重启不丢」,再「谁能用」,再「能发图」。P1 三项均为低成本增量,可穿插。P2 视 codex / 上线运营 / 收紧权限的实际需求再定。
