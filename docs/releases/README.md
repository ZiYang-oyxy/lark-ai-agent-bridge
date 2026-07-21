# Bridge Releases

Release note 使用 [`TEMPLATE.md`](./TEMPLATE.md) 的固定结构，由以下命令生成并在打 tag 前提交：

```bash
./scripts/release.sh prepare v1.2.3
```

支持正式版 `vMAJOR.MINOR.PATCH` 与 RC `vMAJOR.MINOR.PATCH-rc.N`。两者必须按风险与操作优先级保留全部 section：`Breaking Changes`、`Features`、`Bug Fixes`、`Upgrade Notes`、`Miscellaneous`；空 section 写 `- 无。`。

- RC：命令按 Conventional Commits 自动分类，不使用 AI 改写。
- 正式版：AI 以从上一个正式版到当前版本的 commits 和 diff 为证据，按模板归并、去重并改写为面向用户的中文条目。

`tag` 与 `bundle` 都会执行结构校验：标题必须与 tag 完全一致，五个 section 必须按模板顺序各出现一次，每个 section 至少包含一个 bullet，`- 无。` 不能与其他条目并存，非空条目不能跨 section 重复。校验失败时不会创建 tag 或构建发布件。

编辑并提交 `docs/releases/v1.2.3.md` 后创建本地 annotated tag：

```bash
./scripts/release.sh tag v1.2.3
```

生成 provider-neutral 静态发布目录：

```bash
./scripts/release.sh bundle v1.2.3 \
  --base-url https://updates.example.com/lark-ai-agent-bridge
```

仓库不负责上传。外部发布工具必须先完整上传不可变的 `dist/v1.2.3/`，逐文件下载并验证 SHA-256 后，最后替换 `dist/stable/manifest.json` 对应的线上对象。
