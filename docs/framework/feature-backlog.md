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

主线已具备个人使用所需的完整 P0/P1 能力:单 agent(claude)、JSON 会话快照、按 chat/topic 串行、one-shot + `--resume` 续接、附件输入、持久化运行偏好、三种回复展示模式、预览节流和 reaction 生命周期。P0/P1 个人版已于 2026-07-18 收敛。

gist 的企业级方案对当前体量**严重过度设计**,近期一律不进主清单,只作远期备注(见文末)。近期该做的绝大多数能从 **lcab 近乎平移**(模型同构),cc-connect 提供几个轻量补充小件。

## 主线现状盘点(作为清单的基线事实)

| 维度 | 现状 | 关键位置 |
|---|---|---|
| Agent | 仅 claude,one-shot 子进程(非常驻) | `internal/agent/agent.go:42-56`,`ParseKind` 不识别 codex `agent.go:24-30` |
| 会话 | JSON 原子快照保存上下文;重启恢复 `ClaudeSessionID/history`,但 queued/running 不自动重跑 | `internal/session/store.go`,`internal/bridge/service.go` |
| session key | `/config` 选择 `chat` 时为 `{Agent, ChatID}`；选择 `topic` 时为 `{Agent, ChatID, Thread?}` | `internal/config/preferences.go`,`internal/bridge/service.go` |
| 鉴权 | **完全无鉴权** + 硬编码 `--dangerously-skip-permissions` | `internal/agent/agent.go:51` |
| 去重 | 与 session snapshot 一起持久化,带 TTL/容量上限与启动 watermark | `internal/session/store.go`,`internal/bridge/service.go` |
| 并发 | scope 内串行、不同 scope 并行;busy 输入有界排队并按兼容配置聚合下一批 | `internal/session/session.go`,`internal/bridge/service.go` |
| 卡片渲染 | 已有 reducer 中间层(`agentCardStream`):流式 append + 终态 replace | `internal/bridge/stream_card.go` |
| 附件/图片 | 支持图片及纯文本类文件;下载、内容校验、cache/GC 与失败反馈已接线 | `internal/media/`,`internal/feishu/media_downloader.go` |
| model | `/config` 持久化 requested model/effort;卡片区分 requested 与 CLI 实际报告值 | `internal/config/preferences.go`,`internal/bridge/service.go` |
| conversation mode | 默认普通聊天；可持久切换 topic，显式控制 `reply_in_thread` 和 session scope | `internal/config/preferences.go`,`internal/bridge/service.go`,`internal/feishu/cardkit_client.go` |

---

## P0 — 生产必需,先做

### P0-1 · 会话持久化 + 重启恢复 + `/resume`

- **状态(2026-07-18)**:✅ 个人版已完成。JSON v1 snapshot 使用 `0600` 原子写,保存 session/history/dedup/input 状态;启动时只恢复可继续的 Claude 上下文。
- **恢复语义**:重启不调度旧输入。`debouncing/queued/starting` 终结为 `cancelled`,`running` 终结为 `interrupted`,并写入 recovery audit;用户需重新发送,下一条新消息可用保存的 `ClaudeSessionID` 续接上下文。
- **边界**:`/resume` 继续保持禁用;个人版不需要跨用户 catalog/nonce 选择器。
- **证据**:`.cache/evidence/dee05c5/core-regression-green/summary.md` 的 session restart、pending cancel/interrupted、DM/group debounce、busy merge、queue full、scope parallel、stop 与 recall 十个核心 case 全部通过。

### P0-2 · 访问控制(owner / allowlist / invite)

- **个人版决定(2026-07-18)**:⏸️ 本轮明确不做 owner/allowlist/invite/审批体系,不把企业级安全特性作为 P0/P1 完成门槛。
- **适用边界**:部署者负责把 bot 只放在个人可控 chat/tenant 中。若未来扩大使用人群,再恢复 lcab `policy/access.ts` 的 owner/allowlist 方案。

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

- **状态(2026-07-18)**:✅ 已完成。dedup/watermark 与 session snapshot 一起落盘;每个 scope 有界排队,同 scope 串行、不同 scope 并行。
- **队列语义**:busy 时兼容的连续输入可进入下一 batch;配置/workdir/附件边界不同则保持独立 batch。重启一律清空未终态输入,不自动重放。
- **未引入**:全局 semaphore/FIFO/公平性与跨重启 durable job queue,这些对个人版收益不足。
- **证据**:`.cache/evidence/dee05c5/core-regression-green/summary.md` 覆盖 DM/group debounce、busy merge、queue full、scope parallel 与 stop-preserves-queue。

### P1-2 · 可选 model / effort 指定

- **状态(2026-07-18)**:✅ 已完成。`/config` CardKit 表单持久化一份个人 model/effort 偏好;`default` 分别表示省略 `--model`/`--effort`,配置在消息入队时冻结。
- **可观测性**:结果卡明确展示 requested / actual / effort;actual 只信任 CLI stream/result,缺失时显示 `unknown`,不以 requested 冒充。
- **诊断**:`doctor` 默认执行 20 秒 bounded wrapper preflight并报告 warning;`doctor --strict` 将 warning 作为失败。exit 78 给出 wrapper prerequisites 提示且不打印命令输出或环境值。
- **证据**:`.cache/evidence/e8cca6b/config-real/summary.md` 的 `config_roundtrip`、`config_reset`、`config_frozen_queue`、`requested_actual_model`、`wrapper_preflight` 全部通过;环境默认 `sonnet/medium` 在 reset+restart 后生效。

### P1-3 · 回复展示模式可配 + typing/reaction 反馈

- **状态(2026-07-18)**:✅ 已完成。`/config` 可选择 `append`、`append-clean-card`、`latest-card`;偏好与 latest mapping 使用原子 JSON 持久化。
- **回复语义**:`append` 每轮新建卡;`append-clean-card` 终态隐藏思考/工具过程区;`latest-card` 按 conversation scope 复用卡片,跨重启继续递增 sequence。旧卡 ID 失效时清除 mapping 并新建卡,真实飞书返回的 `10002 cardid invalid` 已纳入 stale 判定。
- **流式体验**:preview 同时满足时间间隔与新增字符门限,终态不截断;等待输入使用 `OneSecond`,运行使用 `Typing`,所有完成/停止/重启/竞态路径统一清理 reaction。
- **证据**:`.cache/evidence/dee05c5/reply-final-summary.md` 汇总六个最终通过的 Reply E2E,并链接保留首轮失败现场与两次定向绿色重跑。

### P1-4 · 普通聊天 / 话题模式可配

- **状态(2026-07-18)**:✅ 已完成。默认 `chat` 模式使用 `reply_in_thread=false` 并按 chat 共用 session；`/config` 可切到 `topic`，使用 `reply_in_thread=true` 并按非空 `ThreadID` 隔离 session。
- **配置边界**:`ConversationMode` 与 Reply mode 正交；环境默认来自 `E2E_CONVERSATION_MODE`。保存只影响新接收消息，queued input 和 pending workdir 均冻结接收时 mode，旧 session 不迁移、不删除。
- **实现边界**:SDK 文本 sender 与 CardKit HTTP client 都接收显式 bool，不再硬编码 thread reply；不同 Conversation mode 的输入不能合并为同一 batch。

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

个人版 P0/P1 已完成。下一步不再扩张本轮范围;P2 仅在确有 Codex、多人使用、审批或运营观测需求时启动。若使用范围从个人可控 chat/tenant 扩大,应优先恢复访问控制与权限收紧,再考虑其他 P2 能力。
