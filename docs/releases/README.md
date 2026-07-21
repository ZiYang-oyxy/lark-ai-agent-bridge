# Bridge Releases

Release note 文件由以下命令生成并在打 tag 前提交：

```bash
./scripts/release.sh prepare v1.2.3
```

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
