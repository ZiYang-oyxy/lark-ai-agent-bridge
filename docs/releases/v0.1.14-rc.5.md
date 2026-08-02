# v0.1.14-rc.5

## Breaking Changes

- 无。

## Features

- 无。

## Bug Fixes

- 修复 GitHub Actions checkout 将本地 annotated tag ref 覆盖为 commit、导致发布器拒绝构建的问题；workflow 现在从远端恢复并校验 tag object 与目标 commit 后再打包。
- GitHub 官方 actions 升级到 Node 24 runtime 版本，消除 runner 的 Node 20 deprecated warning。

## Upgrade Notes

- `v0.1.14-rc.4` 在构建校验阶段退出，未创建 GitHub Release、未更新 channel；请直接安装本 RC。

## Miscellaneous

- 无。
