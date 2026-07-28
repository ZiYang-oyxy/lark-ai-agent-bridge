# Bridge 测试单一入口
#
# 分层测试体系统一编号 L0-L3(见 docs/workflow/test-framework.md、
# docs/plans/2026-07-28-test-framework-overhaul.md):
#   L0 单元      纯逻辑 go test,秒级,无依赖
#   L1 组件      simulate + fake agent,本地秒级,无网络
#   L2 确定性e2e 真飞书 + fake agent,分钟级(需 profile)
#   L3 真agent   真 Claude/Codex canary,正式版发布(需 profile)
#
# 快速回归(合入前 / rc 发版):  make test-fast   = L0 + L1
# 正式版全量:                  make test-release = L0+L1+L2+L3
#
# 这些 target 是现有脚本的收口入口,不改脚本行为;逐步迁移的过渡形态,
# 拆巨石(P1)后 L2/L3 会指向 scripts/e2e/run.sh。

# go 二进制:Mac 走 PATH,Linux 服务器走 /usr/local/go/bin/go(见 self-loop GUIDE)。
GO ?= $(shell command -v go 2>/dev/null || echo /usr/local/go/bin/go)
GOCACHE ?= $(CURDIR)/.cache/go-build
# 部分子脚本(smoke-local.sh/verify.sh)内部调裸 `go`,服务器上 go 不在 PATH。
# 把 $(GO) 所在目录前置进 PATH 交给它们,不改脚本行为。
GOBIN_DIR := $(dir $(GO))
SCRIPT_PATH := PATH="$(GOBIN_DIR):$$PATH"

# L2/L3 的目标环境。P-OBSERVE §3.4 后 environment 声明在 docs/environments/<ENV>.env,
# 描述角色/audit 路径/sender App 归属;敏感值仍在 profile(不进 git)。ENV=<name> 会
# 通过 --environment 让 e2e-real.sh 加载对应声明。兼容旧调用:未声明 ENV 时可用
# PROFILE=<name> 直连 profile,跳过 environment。
ENV ?=
PROFILE ?= $(ENV)
ENV_FILE = docs/environments/$(ENV).env

# serve 注入的 durable state 路径是运行时输入,不是测试输入;残留会让
# 默认路径的配置测试读到实时 workspace。L0/L1 一律清掉(verify.sh 已自清)。
UNSET_STATE = E2E_PREFERENCE_STORE E2E_REPLY_STORE E2E_MEDIA_CACHE_DIR E2E_SESSION_STORE

.DEFAULT_GOAL := help

.PHONY: help test-l0 test-l1 test-fast test-l2 test-l3 test-release verify smoke deploy-test

help: ## 列出所有 target
	@echo "Bridge 测试入口(分层 L0-L3):"
	@echo ""
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| sort | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "变量: GO=$(GO)  ENV=<profile>(L2/L3 必填)"

test-l0: ## L0 单元:go test ./... 纯逻辑,秒级
	@echo "== L0 单元测试 =="
	@mkdir -p "$(GOCACHE)"
	env $(foreach v,$(UNSET_STATE),-u $(v)) \
		GOCACHE="$(GOCACHE)" $(GO) test ./...

test-l1: ## L1 组件:simulate + fake agent + smoke YAML,本地秒级无网络
	@echo "== L1 组件测试(simulate + fake agent)=="
	@mkdir -p "$(GOCACHE)"
	env $(foreach v,$(UNSET_STATE),-u $(v)) \
		GOCACHE="$(GOCACHE)" $(GO) run ./cmd/lark-bridge-test --smoke

# smoke-local.sh 的命令面/群 intake shell 断言:比 test-l1 的 testfw smoke 重
# (起 doctor、多次 go run),归 verify 全量门禁,不进 test-fast 快回路以保持快。
smoke-local: ## L1 shell 命令面断言(较重,不进 test-fast)
	env $(SCRIPT_PATH) GOCACHE="$(GOCACHE)" bash scripts/smoke-local.sh

test-fast: test-l0 test-l1 ## ★ 快速回归(合入前/rc 发版):L0 + L1
	@echo "TEST_FAST_OK (L0 + L1 通过)"

test-l2: ## L2 确定性 e2e:真飞书 + fake agent(需 ENV=<env>,如 linux-steve)
	@test -n "$(ENV)" || { echo "错误: L2 需要 ENV=<name>,例如 make test-l2 ENV=linux-steve"; exit 2; }
	@test -f "$(ENV_FILE)" || { echo "错误: environment 声明不存在: $(ENV_FILE)"; exit 2; }
	@echo "== L2 确定性 e2e(真飞书 + fake agent)environment=$(ENV) =="
	E2E_STATE_ROOT="$$HOME" E2E_REAL_E2E_FAKE_CLAUDE=1 ./scripts/e2e-real.sh --environment "$(ENV)" --mode full --strict-capabilities

test-l3: ## L3 真 agent canary:真 Claude/Codex(需 ENV=<env>)
	@test -n "$(ENV)" || { echo "错误: L3 需要 ENV=<name>,例如 make test-l3 ENV=linux-steve"; exit 2; }
	@test -f "$(ENV_FILE)" || { echo "错误: environment 声明不存在: $(ENV_FILE)"; exit 2; }
	@echo "== L3 真 agent canary(真 Claude)environment=$(ENV) =="
	env -u E2E_REAL_E2E_FAKE_CLAUDE E2E_STATE_ROOT="$$HOME" ./scripts/e2e-real.sh --environment "$(ENV)" --case new_basic

# deploy-test:显式换血被测 bot(rebuild-test.sh)。故意不在 test-l2/test-l3 里
# 自动跑,避免与 self-loop GUIDE 里 supervisor 手工 L3 语义混淆。测试框架和
# self-loop 是同一 rebuild-test.sh 的两个调用者。
#
# 目前只支持 Linux ENV=linux-steve(调 ~/bridge/bridge-self-loop/rebuild-test.sh)。
# Mac ENV=macos-mike 需走 rebuild-test-macos.sh --lease,当前 Makefile 未桥接
# (未在 self-loop 仓做幂等改造前,交叉平台的部署入口不加,避免误踩)。
deploy-test: ## 换血被测 bot 为本地构建版(需 ENV=linux-steve)
	@test -n "$(ENV)" || { echo "错误: deploy-test 需要 ENV=<name>"; exit 2; }
	@test -f "$(ENV_FILE)" || { echo "错误: environment 声明不存在: $(ENV_FILE)"; exit 2; }
	@case "$(ENV)" in \
		linux-steve) \
			echo "== deploy-test environment=$(ENV) =="; \
			bash $$HOME/bridge/bridge-self-loop/rebuild-test.sh $(CURDIR) ;; \
		macos-mike) \
			echo "错误: ENV=macos-mike 请直接跑 rebuild-test-macos.sh --lease,当前 Makefile 未桥接"; \
			exit 2 ;; \
		*) \
			echo "错误: 未知 environment: $(ENV) (支持 linux-steve|macos-mike)"; \
			exit 2 ;; \
	esac

test-release: ## 正式版全量回归:L0+L1+L2+L3(需 ENV=<profile>)
	@test -n "$(PROFILE)" || { echo "错误: test-release 需要 ENV=<profile>"; exit 2; }
	./scripts/release-regression.sh --profile "$(PROFILE)"

# ---- 兼容别名(逐步迁移期保留,指向现有全量本地门禁)----
verify: ## 别名:本地全量 verify.sh(L0/L1 超集门禁)
	env $(SCRIPT_PATH) GOCACHE="$(GOCACHE)" bash scripts/verify.sh

smoke: test-l1 ## 别名:等价 L1(旧 smoke-local 语义)
	@true
