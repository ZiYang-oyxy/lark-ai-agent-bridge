# CardKit Renderer 有界回收 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 `CardKitRouterRenderer` 增加 terminal LRU + TTL 有界回收，阻止 renderer map 随累计 run 数量线性增长，同时保护 active 与 interaction renderer。

**Architecture:** Router 用内部 entry 保存 renderer、最后使用时间和 terminal 状态。公开构造路径返回 tracking `ResumableRenderer`，只在底层渲染成功后刷新 entry 并同步 sweep；sweep 先清理 TTL 过期 terminal，再按 LRU 将 terminal 数量压回上限，active/interaction entry 永不被强制淘汰。

**Tech Stack:** Go 1.26.3、标准库 `sync`/`sort`/`time`、现有 CardKit fake client 与 Go race detector。

## Global Constraints

- 默认 terminal 上限固定为 512，不新增环境变量或公开配置项。
- 默认 terminal TTL 固定为 24 小时，与 `defaultActionGrantTTL` 保持同一数量级。
- 只有成功渲染 `result`、`error`、`stopped` 后才标记 terminal。
- active 与 `interactionDepth > 0` 的 renderer 不被淘汰；必要时允许 cache 临时超限。
- 不新增后台 goroutine，不修改 CardKit sequence、`RenderRef`、reply policy 或 Service 生命周期。
- 严格执行 RED → GREEN → REFACTOR；生产代码前必须看到对应测试按预期失败。

---

### Task 1: 建立 terminal 容量与 TTL 回收契约

**Files:**
- Modify: `internal/feishu/cardkit_renderer_test.go`
- Modify: `internal/feishu/cardkit_renderer.go`

**Interfaces:**
- Consumes: `newCardKitRouterRendererWithClock(...)`、`ResumableRenderer.Render`、`CardKitRouterRenderer.ActiveCards`。
- Produces: `newCardKitRouterRendererWithRetention(client, observer, journal, now, maxTerminal, terminalTTL)` 测试构造器，以及 router 内部的 terminal retention 行为。

- [ ] **Step 1: 写容量回收失败测试**

增加 `TestCardKitRouterEvictsOldestTerminalRendererOverLimit`：使用上限 `1` 创建 `run-1`、`run-2`，分别成功渲染 `result`，推进受控时钟后断言只保留较新的 `run-2`。

```go
router := newCardKitRouterRendererWithRetention(client, nil, nil, clock.Now, 1, 24*time.Hour)
first, _ := router.NewStreaming(t.Context(), "run-1", "message-1")
if err := first.Render(card.Event{Type: "result", SessionID: "run-1"}); err != nil { t.Fatal(err) }
clock.Set(now.Add(time.Minute))
second, _ := router.NewStreaming(t.Context(), "run-2", "message-2")
if err := second.Render(card.Event{Type: "result", SessionID: "run-2"}); err != nil { t.Fatal(err) }
if router.ActiveCards() != 1 { t.Fatalf("active cards = %d, want 1", router.ActiveCards()) }
```

- [ ] **Step 2: 运行测试并确认 RED**

```bash
go test ./internal/feishu -run TestCardKitRouterEvictsOldestTerminalRendererOverLimit -count=1
```

Expected: 编译失败，提示 `newCardKitRouterRendererWithRetention` 未定义。

- [ ] **Step 3: 增加 entry、tracking wrapper 与构造器**

在 `internal/feishu/cardkit_renderer.go` 增加以下内部类型；生产构造器使用默认上限和 TTL，测试构造器允许注入小值：

```go
const (
	defaultMaxTerminalRenderers = 512
	defaultTerminalRendererTTL = 24 * time.Hour
)

type cardKitRendererEntry struct {
	renderer *CardKitRenderer
	lastUsed time.Time
	terminal bool
}

type trackingResumableRenderer struct {
	ResumableRenderer
	afterRender func(card.Event)
}

func (r *trackingResumableRenderer) Render(event card.Event) error {
	if err := r.ResumableRenderer.Render(event); err != nil { return err }
	if r.afterRender != nil { r.afterRender(event) }
	return nil
}
```

将 `renderers` 改为 `map[string]*cardKitRendererEntry`，增加 `maxTerminal`、`terminalTTL`。`NewStreamingBound`、`RehydrateBound` 与 router 自身 `Render` 均在成功渲染后调用 `markRendered(key, renderer, event)`；回调必须校验 entry 仍指向同一个 renderer，避免旧 wrapper 更新替换后的 entry。

- [ ] **Step 4: 实现 terminal 判定和 sweep**

```go
func terminalCardEvent(event card.Event) bool {
	switch event.Type {
	case "result", "error", "stopped":
		return true
	default:
		return false
	}
}
```

`sweepLocked(now)` 在持有 router mutex 时执行：先删除超过 TTL 且 `interactionDepth == 0` 的 terminal entry，再按 `lastUsed`、key 稳定排序删除最旧 terminal，直到 terminal 数量不超过上限。读取 `interactionDepth` 时锁对应 renderer mutex。active 不计入 terminal 上限。

- [ ] **Step 5: 运行定向测试并确认 GREEN**

```bash
go test ./internal/feishu -run TestCardKitRouterEvictsOldestTerminalRendererOverLimit -count=1
```

Expected: PASS。

---

### Task 2: 保护 active、interaction 与失败渲染路径

**Files:**
- Modify: `internal/feishu/cardkit_renderer_test.go`
- Modify: `internal/feishu/cardkit_renderer.go`

**Interfaces:**
- Consumes: Task 1 的 entry、tracking wrapper、`sweepLocked`。
- Produces: active/interaction 安全语义，以及 interaction release 后的即时回收。

- [ ] **Step 1: 写三个失败测试**

增加：

```go
func TestCardKitRouterRetentionNeverEvictsActiveRenderer(t *testing.T)
func TestCardKitRouterRetentionDefersInteractionEvictionUntilRelease(t *testing.T)
func TestCardKitRouterFailedTerminalRenderRemainsActive(t *testing.T)
```

分别断言：容量压力不删除 active；TTL 过期但 interaction 未 release 时 key 仍存在、release 后删除；terminal update 返回错误时 entry 仍为 active 且不会被 sweep。

- [ ] **Step 2: 运行测试并确认 RED**

```bash
go test ./internal/feishu -run 'TestCardKitRouterRetention|TestCardKitRouterFailedTerminalRenderRemainsActive' -count=1
```

Expected: interaction release 后回收断言失败，因为 release 尚未触发 sweep。

- [ ] **Step 3: 将 interaction 纳入 entry 生命周期**

`BeginCardInteraction` 在 router 锁内取得 entry并刷新 `lastUsed`，再增加底层 `interactionDepth`。release 保持幂等，先减少 depth，释放 renderer mutex 后才获取 router mutex并执行 sweep：

```go
renderer.mu.Lock()
if renderer.interactionDepth > 0 { renderer.interactionDepth-- }
renderer.mu.Unlock()
r.mu.Lock()
r.sweepLocked(r.now().UTC())
r.mu.Unlock()
```

禁止在持有 renderer mutex 时获取 router mutex。

- [ ] **Step 4: 运行保护测试并确认 GREEN**

```bash
go test ./internal/feishu -run 'TestCardKitRouterRetention|TestCardKitRouterFailedTerminalRenderRemainsActive|TestCardKitRouterInteractionFence' -count=1
```

Expected: PASS。

---

### Task 3: 保持 RenderRef/sequence 并验证并发安全

**Files:**
- Modify: `internal/feishu/cardkit_renderer_test.go`
- Modify: `internal/feishu/cardkit_renderer.go`

**Interfaces:**
- Consumes: tracking wrapper 与 retention metadata。
- Produces: 与原有 `ResumableRenderer` 相同的 `RenderRef`，以及通过 race detector 的同步实现。

- [ ] **Step 1: 写 wrapper 契约和并发测试**

增加 `TestCardKitRouterTrackedRendererPreservesRenderRef`：同一 renderer 依次渲染 stream/result，断言 `CardID`、`ReplyMessageID`、`CreatedAt` 与递增 `Version` 保持原语义。

增加 `TestCardKitRouterRetentionConcurrentRenderAndInteraction`：使用线程安全的 `strictSequenceCardKitServer`，并发执行 stream render、`BeginCardInteraction`/release 和不同 terminal renderer 创建；断言无错误且 active key 保留，数据竞争交给 `-race` 判定。

- [ ] **Step 2: 运行测试并确认 RED**

```bash
go test ./internal/feishu -run 'TestCardKitRouterTrackedRendererPreservesRenderRef|TestCardKitRouterRetentionConcurrentRenderAndInteraction' -count=1
```

Expected: 新并发测试或旧 concrete type assertion 失败，暴露 wrapper 兼容点。

- [ ] **Step 3: 完成最小兼容调整**

保持 wrapper 嵌入 `ResumableRenderer`，不重写 `RenderRef`。包内旧测试如需检查 `interactionDepth`，改为从 router entry 取得底层 renderer，不再对公开返回值断言 `*CardKitRenderer`。完成后运行 `gofmt`。

- [ ] **Step 4: 运行 package 与 race 验证**

```bash
go test ./internal/feishu -count=1
go test -race -count=1 ./internal/feishu
```

Expected: 两条命令均 PASS，race detector 无报告。

---

### Task 4: 完整回归并关闭任务台账

**Files:**
- Modify: `tasks.md`
- Verify: `internal/feishu/cardkit_renderer.go`
- Verify: `internal/feishu/cardkit_renderer_test.go`

**Interfaces:**
- Consumes: 完成的 renderer retention 实现。
- Produces: 绿色全仓验证与准确的未完成任务台账。

- [ ] **Step 1: 运行静态与全量验证**

```bash
gofmt -l internal/feishu/cardkit_renderer.go internal/feishu/cardkit_renderer_test.go
go vet ./...
go test ./...
go test -race -count=1 ./internal/bridge ./internal/session ./internal/schedule ./internal/card ./internal/feishu
```

Expected: `gofmt -l` 无输出，其余命令 PASS。

- [ ] **Step 2: 删除已完成的 renderer task**

从 `tasks.md` 删除进行中的 renderer 回收条目，保留其他稳定性、架构和 CardKit P3 条目。

- [ ] **Step 3: 检查 diff 范围**

```bash
git diff --check
git status --short
git diff -- internal/feishu/cardkit_renderer.go internal/feishu/cardkit_renderer_test.go tasks.md
```

Expected: 只包含 renderer 实现、测试和 `tasks.md` 收口；不包含现有未跟踪的项目总览文件。

- [ ] **Step 4: 提交实现**

```bash
git add internal/feishu/cardkit_renderer.go internal/feishu/cardkit_renderer_test.go tasks.md
git commit -m "fix(cardkit): bound retained renderers"
```
