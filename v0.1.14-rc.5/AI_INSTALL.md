# Lark AI Agent Bridge — AI Agent 安装指南

本指南面向执行安装的 AI Agent。普通用户不需要阅读或手动执行其中的命令。

## 目标

将 Lark AI Agent Bridge 安装到当前机器，把用户发起安装时的当前目录作为默认 workspace，使用正式 binary 后台运行，并完成严格自检和最小可用性验证。

发布 manifest：

```text
https://ZiYang-oyxy.github.io/lark-ai-agent-bridge/v0.1.14-rc.5/manifest.json
```

## 执行规则

1. 先检测当前 OS、CPU 架构、workspace、Claude/Codex 和已有 Bridge 部署；不要覆盖归属不明的服务、binary 或状态目录。
2. 读取 manifest，使用 `GOOS/GOARCH` 精确选择 asset。当前官方发布只支持 manifest 明确列出的平台。
3. 下载 asset 后同时校验 manifest 声明的 `size` 和 SHA-256（`sha256`）；任一不符立即停止并删除未安装的临时文件。
4. 如果用户提供的是 Release 页面、压缩包或直接 binary，先寻找同版本官方 manifest 或 checksum；找不到时只请求官方 manifest 或本指南链接，不得跳过校验。
5. 使用正式 binary 长期运行。源码仓库只用于可信构建，不得用 `go run` 托管长期服务。
6. 默认 workspace 是用户发起安装时的当前目录。Bridge 状态保存在该目录的 `.lark-agent-bridge/`；不要把 secret 写入 workspace 或 Git。
7. 自动检测可用的 Claude/Codex。需要 `agents.json` 时使用相对路径，保持 workspace 可搬迁。Codex 的 model、reasoning effort、sandbox、approval、profile、plugins 和 MCP 由所选 executable/wrapper 管理。
8. 只在无法自动获得飞书配置时请求用户完成一个明确动作。不得在对话、终端输出、命令参数、Git、audit 或普通日志中回显 App Secret、token 或 OAuth 信息。
9. 使用生产相同的 binary、环境和 workdir 执行 `doctor --strict`。失败时定位根因，不得降级检查或修改源码绕过。
10. 根据当前系统选择成熟的后台服务管理方式，精确管理当前实例；不得使用宽泛 `pkill`。
11. 启动后验证 PID、binary version、workspace、飞书长连接和一条无副作用消息链路。只有必要检查全部通过才能报告安装成功。

## 用户沟通

- 正常执行过程不展示安装命令和内部细节。
- 需要用户参与时，一次只给一个清晰动作，例如“请完成飞书授权，完成后我会继续”。
- 成功时简短说明 Bridge 已启用以及如何在飞书开始使用。
- 失败时保留脱敏诊断，只告诉用户当前阻塞原因和下一步动作。

## 安全边界

- 不修改 Bridge 源码规避环境、权限或配置错误。
- 不跳过 size 或 SHA-256 校验。
- 不覆盖未知部署，不把本地 E2E profile 当作生产配置。
- 不读取、打印、提交或记录真实 secret。
- 未通过 `doctor --strict` 和启动验证时不得宣称安装完成。
