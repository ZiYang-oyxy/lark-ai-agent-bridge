# 验证工作流：三入口

面向两个时机的一键验证入口。测试本身见 [`testing.md`](testing.md) / [`e2e-coverage-matrix.md`](e2e-coverage-matrix.md)，本页只讲“何时用哪个、怎么读结果”。

## 三入口

| 入口 | 时机 | 落地仓 | 调用 | 成功标记 |
|---|---|---|---|---|
| `scripts/smoke-local.sh` | 改动提交后（默认） | 主线仓 | `./scripts/smoke-local.sh` | `SMOKE_LOCAL_OK` |
| `.agents/skills/deploying-lark-bridge-to-server/scripts/smoke-on-server.sh` | 真要验真机时（可选） | 容器仓 skill | `./smoke-on-server.sh --rc <已发布RC> --bridge-checkout <对应tag checkout>` | `SMOKE_OK` |
| `scripts/release-regression.sh` | 正式发版前（门禁） | 主线仓 | `./scripts/release-regression.sh --profile <p> --release <v>` | `RELEASE_REGRESSION_OK` |

- **本地秒级冒烟**：纯本地、不上机、秒级，验装配和核心命令面。提交后默认跑这个。
- **上机冒烟**：接受一个**已发布的 RC**（RC 发布含人工审阅，由 releasing skill 独立完成），并要求 clean bridge checkout 的 `HEAD` 精确匹配该 RC tag。脚本部署发布件后，通过 server skill 的事务入口对同一 commit 跑真实飞书 `/stop + /resume`，结束后恢复到已部署 RC。它不打包、不发布、不建 tag。
- **全量回归**：发版前跑 L0/L1（verify + `-race`）→ L2（e2e-real full）→ L3（真实 Claude canary）。L3 默认只告警不阻断（`l3=warn` 仍输出 OK），`--strict-l3` 升硬门禁。

## 结果与证据

- 成功：各脚本末行输出对应标记；缺标记即视为未通过。
- 失败定位：E2E 证据落 `.cache/evidence/`；上机部署失败看 `deploy-release.sh` 输出与其自动回滚证据。

## 已知未自动化：`card.action.trigger` 真实平台投递

真实用户点击卡片按钮触发的 `card.action.trigger` 平台投递**无法在无人环境自动触发**——这是飞书平台限制（平台语义绑定真人点击，官方只提供“接收回调”能力，无“制造用户交互”的服务端 API；Codex + 官方文档查证 2026-07-22）。两个成品级参考仓（cc-connect / lcab）同样不真测此链路。其**处理逻辑**已由 L0 确定性测试 + L2 render 注入覆盖；需要真实中断时用文本 `/stop` 替代点按钮。
