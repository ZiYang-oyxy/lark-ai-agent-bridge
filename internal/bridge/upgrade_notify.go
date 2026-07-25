package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"lark-agent-bridge/internal/buildinfo"
	"lark-agent-bridge/internal/feishu"
)

// upgradeNotifyEnvVar 承载「升级完成后回原对话报喜」所需的最小上下文。
// 升级会 detach 重启进程,新旧进程之间没有内存共享;把上下文经 env 传给用
// 相同 argv/env 拉起的新进程,新进程启动时读取并主动发一条「升级成功」消息。
//
// 为什么用 env 而非磁盘文件:env 天然一次性——它只存在于升级路径显式追加了该
// 变量的那一次重启;若新进程随后崩溃、由 launchd 用 plist 环境重新拉起,plist
// 里没有这个变量,就不会重复报喜。正好是「只通知一次」的期望语义,无需额外清理
// 陈旧文件或做去重。
const upgradeNotifyEnvVar = "LAB_UPGRADE_NOTIFY"

// upgradeNotifyContext 是重启前后传递的升级上下文。字段刻意最小化:只保留把消息
// 送回原对话所需的信息。不含 @ 相关字段——卡片回调没有可靠的群/私聊信号,新消息
// 本身已产生未读红点,不额外 @。
type upgradeNotifyContext struct {
	Version   string `json:"version"`
	ChatID    string `json:"chat_id"`
	MessageID string `json:"message_id"` // 发起升级的那条卡片消息,用作 reply 锚点
}

// upgradeNotifyEnv 把升级上下文编码成一个 "KEY=value" 环境变量项,追加到重启 env。
// message/chat 任一为空则返回空串(无法定位对话,不注入,新进程自然不报喜)。
func upgradeNotifyEnv(ctx upgradeNotifyContext) string {
	if strings.TrimSpace(ctx.MessageID) == "" || strings.TrimSpace(ctx.ChatID) == "" {
		return ""
	}
	payload, err := json.Marshal(ctx)
	if err != nil {
		return ""
	}
	return upgradeNotifyEnvVar + "=" + string(payload)
}

// appendUpgradeNotifyEnv 在重启 env 基础上追加升级上下文项(若可用)。
// 先剔除可能已存在的同名变量,避免叠加历史值。
func appendUpgradeNotifyEnv(env []string, ctx upgradeNotifyContext) []string {
	item := upgradeNotifyEnv(ctx)
	if item == "" {
		return env
	}
	out := make([]string, 0, len(env)+1)
	prefix := upgradeNotifyEnvVar + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			continue
		}
		out = append(out, e)
	}
	return append(out, item)
}

// NotifyUpgradeSuccessIfPending 在新进程启动后调用:若 env 携带升级上下文,说明本次
// 启动是一次成功自升级的结果,则往原对话主动回一条「升级成功」消息。
//
// 绝不影响启动主流程:Notifier 缺失、env 缺失或解析失败、发送失败,都只记 audit、
// 直接返回,不 panic、不阻断 serve。设计为可安全放进 goroutine。
func (s *Service) NotifyUpgradeSuccessIfPending(ctx context.Context) {
	raw := strings.TrimSpace(os.Getenv(upgradeNotifyEnvVar))
	if raw == "" {
		return
	}
	// 读到后即从当前进程环境移除,避免本进程内任何后续逻辑重复消费。
	_ = os.Unsetenv(upgradeNotifyEnvVar)

	var nctx upgradeNotifyContext
	if err := json.Unmarshal([]byte(raw), &nctx); err != nil {
		if s.Audit != nil {
			s.Audit.Record("system", "upgrade_notify_decode_failed", "", err.Error())
		}
		return
	}
	if s.Notifier == nil || strings.TrimSpace(nctx.MessageID) == "" {
		return
	}

	version := strings.TrimSpace(nctx.Version)
	if version == "" {
		version = buildinfo.Version
	}
	reply := feishu.Reply{
		ShouldReply:      true,
		ReplyToMessageID: nctx.MessageID,
		Message:          fmt.Sprintf("✅ Bridge 已升级到 v%s，正在正常运行。", version),
		Kind:             feishu.ReplyKindFinal,
	}

	if _, err := s.Notifier.SendReply(ctx, reply); err != nil {
		if s.Audit != nil {
			s.Audit.Record("system", "upgrade_notify_failed", "", err.Error())
		}
		return
	}
	if s.Audit != nil {
		s.Audit.Record("system", "upgrade_notify_sent", "", "version="+version)
	}
}
