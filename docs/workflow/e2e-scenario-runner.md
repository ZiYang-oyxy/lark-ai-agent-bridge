# Go E2E Scenario Runner

`lark-bridge-e2e` 是真实飞书 deterministic E2E 的场景执行层。它负责业务步骤、稳定 observable、断言、失败分类和证据收集；它不拥有 candidate 部署、进程替换、`doctor --strict`、commit 或 rollback。

阶段 1 只注册 `stop`。顶层 `deploying-lark-bridge-to-server` Skill 完成一次旧 verifier/新 runner 等价验收并接线前，现有部署门禁保持不变。

## 命令

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-bridge-e2e run \
  --scenario stop \
  --deployment /absolute/path/deployment.json \
  --config /absolute/path/config.json \
  --evidence-dir /absolute/path/evidence
```

stdout 只有一行稳定摘要：

```text
PASS scenario=stop evidence=/path/to/evidence/e2e-...
```

或：

```text
FAIL class=assertion scenario=stop step=assert_ready evidence=/path/to/evidence/e2e-...
```

详细原因只从 `result.json` 和 evidence bundle 读取。runner 不打印 credential、命令 stderr 或原始远端 environment。

## 输入

`deployment.json` 由事务部署 controller 在 candidate 和 strict doctor 成功后生成：

```json
{
  "schema_version": 1,
  "transaction": "/remote/state/deployments/txn.example",
  "candidate_pid": 12345,
  "source_commit": "0123456789abcdef",
  "binary_sha256": "sha256:0123456789abcdef",
  "workspace": "/remote/workspace",
  "state_dir": "/remote/state",
  "fixture_dir": "/remote/state/fixtures"
}
```

`config.json` 只包含非 credential 的固定 Test instance 配置：

```json
{
  "schema_version": 1,
  "profile": "test-profile",
  "expected_bot_name": "Test Bot",
  "app_id": "cli_test_app",
  "chat_id": "oc_test_chat",
  "remote_host": "user@example.test",
  "audit_path": "/remote/workspace/.bridge/audit.jsonl",
  "poll_interval_ms": 200,
  "step_timeout_ms": 12000
}
```

两个 loader 都使用严格 JSON schema：拒绝 unknown field、额外 JSON value、不兼容版本、相对路径、非法 transaction/fixture 关系和危险 remote host。App Secret、OAuth token 和 password 没有合法配置字段。

## `/stop` 场景

场景按顺序执行：

1. 选择 Claude agent mode；
2. arm 一次性 nonce fixture；
3. 发送 nonce 并等待关联 reply；
4. 断言用户可见卡片包含 `READY_<nonce>`；
5. 发送 `/stop` 并断言确认卡片包含 `已请求停止当前任务`；
6. 等待 `batch_stop_requested`；
7. 等待原任务 `cardkit_update event=stopped`；
8. cleanup nonce；如果启动消息已发送但 `/stop` 尚未发送，尽力停止残留任务。

READY 断言递归检查卡片 JSON 中所有字符串，不要求 `cardkit_text_stream`。native stream 和 full-card fallback 只要产生相同用户可见结果都算成功。

## 失败分类

- `product`：Bridge 稳定业务状态错误；
- `assertion`：用户可见结果与期望不匹配；
- `harness`：runner、schema、解析器或 fixture protocol 错误；
- `platform`：飞书 OAuth、API 或 CardKit 不可用；
- `environment`：SSH、命令、文件、权限或 toolchain；
- `timeout`：等待超时，包含等待对象与最后观测；
- `cleanup`：主场景结束后的资源恢复失败。

primary failure 与 cleanup failures 分开保存。cleanup 不会覆盖最先发生的业务失败。

## Evidence

```text
<evidence-dir>/<run-id>/
  deployment.json
  scenario.json
  actions.jsonl
  audit.jsonl
  cards/
  result.json
```

run 目录和 `cards/` 为 `0700`，所有文件为 `0600`。JSON 文档采用同目录临时文件、`fsync`、原子 rename；JSONL 每条追加后同步。写盘前统一脱敏，包括普通 header 和 JSON 形式的 Authorization Bearer value。

Evidence 创建失败时 runner 不执行任何飞书或远端 action。

## 安全边界

- `lark-cli` 参数使用独立 argv，不通过 `sh -c`。
- SSH 的 stdin 永远是静态脚本；动态值先 Base64 编码为安全 argv，远端脚本再解码，避免 OpenSSH 远端 shell 二次解析。
- Bot credential 只在固定远端 `service.env` 中读取；identity 脚本只返回 Bot open ID。
- runner 不提交或回滚 transaction。调用方必须根据退出码和 `result.json` 决定精确 `commit/rollback`。

## 本地验证

```bash
GOCACHE=$PWD/.cache/go-build go test ./internal/e2e/... ./cmd/lark-bridge-e2e -count=1
GOCACHE=$PWD/.cache/go-build go vet ./internal/e2e/... ./cmd/lark-bridge-e2e
```

fake-driver tests 覆盖成功、reply/READY/ack/audit/terminal 失败、timeout、残留 stop、cleanup failure、unsafe nonce、argv 隔离、SSH 参数编码和 malformed JSON。它们不访问飞书或远端服务器。

## 后续阶段

- 顶层部署 Skill 生成 descriptor，在同一 pending candidate 上顺序运行旧 verifier 与新 runner；任一失败都 rollback。
- 等价验收后，新 runner 成为 `/stop` 唯一 L2 gate。
- 阶段 2 增加通用 Agent fixture transcript 和 `/resume` 场景。
- 阶段 3 再拆 deployment controller，并删除旧 `verify-stop.sh` 与历史 verifier。
