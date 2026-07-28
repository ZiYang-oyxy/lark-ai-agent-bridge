# fakeclaude fixtures

- 建立日期:2026-07-28
- 上下文:`docs/plans/2026-07-28-test-framework-overhaul-P-OBSERVE.md` §3.6 fake agent 统一

## 是什么

每个 `.json` 文件是一份**确定性 fake claude 场景**:声明"什么样的 argv/prompt 触发我 → 我该往 stdout 打什么 NDJSON 行"。

L1(Go 层 `simulateRunner`)与 L2(shell 侧 `cmd/lark-agent-fake-claude` 二进制)**共读同一份 fixture**,消灭原来两套硬编码 fake 的分裂。

## 字段

```json
{
  "name": "human_readable_label",
  "match": {
    "marker_pattern": "E2E_.*",          // 可选:regexp,匹配 argv 里最后一个 E2E_* token
    "prompt_contains": "substring"       // 可选:prompt 里必须出现的子串
  },
  "emit": [
    {"line": "{...NDJSON}", "delay_sec": 0.0},
    ...
  ],
  "post_delay_sec": 0                    // 可选:全部 emit 后额外 sleep(模拟 E2E_BLOCK 类挂起)
}
```

`marker_pattern` 与 `prompt_contains` 都设时,须**都命中**。二者都空视为无效 fixture。

分派顺序:目录字典序(建议 `10-*` / `20-*` 数字前缀控制优先级)。第一个命中即返回。没有命中则回落到内置 default(打印 `FAKE_E2E_STARTED <marker>` result,exit 0)。

## 与 shell shim 现状的关系

**当前 P-OBSERVE §3.6 Step 6a 只落地 fixture 引擎 + 单元测试 + 若干示例 fixture,不切换 e2e-real 的 `prepare_fake_claude_if_needed`**(那 100+ 行 heredoc 保留)。Step 6b 会把 shim 从"内联 case 分支"改成"调 `lark-agent-fake-claude` 二进制并 export LAB_FAKE_FIXTURE_DIR",到时才需要把全部现有分支迁到独立 fixture。

## 待迁清单(Step 6b)

shell shim 里的分支需要对应 fixture:

| shim 分支 | fixture 备注 |
|---|---|
| `E2E_BRIDGE_IMAGE_NO_INTENT` | 需要 fixture 支持 side-effect 写图(见 image_writer 讨论) |
| `E2E_BRIDGE_IMAGE_AMBIGUOUS` | 纯文本 result,可静态 fixture |
| `E2E_BRIDGE_IMAGE_INTENT` | 需要 image side-effect + marker 插值 |
| `E2E_*_OUTPUT_IMAGE` | 需要 image side-effect + marker 插值 |
| `E2E_*_NATIVE_TEXT_STREAM_STOP_E2E_BLOCK` | delay + post_delay(挂起) |
| `E2E_*_NATIVE_TEXT_STREAM_NORMAL` | delay 序列 + result |
| `E2E_*_STREAM` | delay + result,marker 插值 |
| `E2E_PREVIEW_THRESHOLDS` | 长字符串(2100 C)inline |
| `E2E_REPLY_MODE_SEMANTICS` | 4 段 assistant + result |
| `E2E_PROCESS_PANELS` | 3 段 delta + result |

Step 6a 落地的示例 fixture 覆盖了后 3 种(相对简单、无 side-effect),证明 fixture 引擎能跑;image side-effect 与 marker 模板插值属 Step 6b 的引擎增强,不在本步。
