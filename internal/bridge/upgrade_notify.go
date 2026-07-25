package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"lark-agent-bridge/internal/buildinfo"
	"lark-agent-bridge/internal/feishu"
)

// upgradeNotifyEnvVar 是旧版本/非托管重启的兼容通道。新版本以 session
// store 同目录的原子一次性文件为权威通道，因为 launchd/systemd 重拉的
// 进程不会继承 detach child 的额外 env。
const upgradeNotifyEnvVar = "LAB_UPGRADE_NOTIFY"

// upgradeNotifyContext 是重启前后传递的升级上下文。字段刻意最小化:只保留把消息
// 送回原对话所需的信息。不含 @ 相关字段——卡片回调没有可靠的群/私聊信号,新消息
// 本身已产生未读红点,不额外 @。
type upgradeNotifyContext struct {
	Version   string    `json:"version"`
	ChatID    string    `json:"chat_id"`
	MessageID string    `json:"message_id"` // 发起升级的那条卡片消息,用作 reply 锚点
	CreatedAt time.Time `json:"created_at,omitempty"`
}

const upgradeNotifyMaxAge = 10 * time.Minute

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
	out := stripUpgradeNotifyEnv(env)
	return append(out, item)
}

func stripUpgradeNotifyEnv(env []string) []string {
	out := make([]string, 0, len(env))
	prefix := upgradeNotifyEnvVar + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func (s *Service) upgradeNotifyPath() string {
	path := strings.TrimSpace(s.Config.SessionStorePath)
	if path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(path), "upgrade-notify.json")
}

// persistUpgradeNotify 在替换 binary 前原子落盘。launchd/systemd 重拉的进程
// 不会继承 detach child 的额外 env，因此这是受托管部署下的权威交接通道。
func (s *Service) persistUpgradeNotify(ctx upgradeNotifyContext) error {
	path := s.upgradeNotifyPath()
	if path == "" || strings.TrimSpace(ctx.ChatID) == "" || strings.TrimSpace(ctx.MessageID) == "" {
		return fmt.Errorf("upgrade notification anchor or store path is missing")
	}
	ctx.CreatedAt = time.Now().UTC()
	data, err := json.Marshal(ctx)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".upgrade-notify-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func (s *Service) clearUpgradeNotify() {
	if path := s.upgradeNotifyPath(); path != "" {
		_ = os.Remove(path)
	}
}

func (s *Service) claimPersistedUpgradeNotify() (upgradeNotifyContext, bool, error) {
	path := s.upgradeNotifyPath()
	if path == "" {
		return upgradeNotifyContext{}, false, nil
	}
	claimed := path + ".consuming"
	if err := os.Rename(path, claimed); err != nil {
		if os.IsNotExist(err) {
			return upgradeNotifyContext{}, false, nil
		}
		return upgradeNotifyContext{}, false, err
	}
	defer os.Remove(claimed)
	data, err := os.ReadFile(claimed)
	if err != nil {
		return upgradeNotifyContext{}, false, err
	}
	var ctx upgradeNotifyContext
	if err := json.Unmarshal(data, &ctx); err != nil {
		return upgradeNotifyContext{}, false, err
	}
	return ctx, true, nil
}

// NotifyUpgradeSuccessIfPending 在新进程启动后调用：优先原子消费持久化
// handoff，没有时再兼容旧 env 通道；命中则往原对话主动回一条「升级成功」。
//
// 绝不影响启动主流程：Notifier 缺失、handoff 缺失/损坏、发送失败，都只记 audit、
// 直接返回,不 panic、不阻断 serve。设计为可安全放进 goroutine。
func (s *Service) NotifyUpgradeSuccessIfPending(ctx context.Context) {
	nctx, found, err := s.claimPersistedUpgradeNotify()
	if err != nil {
		if s.Audit != nil {
			s.Audit.Record("system", "upgrade_notify_claim_failed", "", err.Error())
		}
		return
	}
	if !found {
		raw := strings.TrimSpace(os.Getenv(upgradeNotifyEnvVar))
		if raw == "" {
			return
		}
		// env 是非托管进程的兼容通道，读到后立即清除。
		_ = os.Unsetenv(upgradeNotifyEnvVar)
		if err := json.Unmarshal([]byte(raw), &nctx); err != nil {
			if s.Audit != nil {
				s.Audit.Record("system", "upgrade_notify_decode_failed", "", err.Error())
			}
			return
		}
	}
	if strings.TrimSpace(nctx.Version) != "" && strings.TrimSpace(buildinfo.Version) != strings.TrimSpace(nctx.Version) {
		if s.Audit != nil {
			s.Audit.Record("system", "upgrade_notify_version_mismatch", "", "expected="+nctx.Version+" actual="+buildinfo.Version)
		}
		return
	}
	if !nctx.CreatedAt.IsZero() && time.Since(nctx.CreatedAt) > upgradeNotifyMaxAge {
		if s.Audit != nil {
			s.Audit.Record("system", "upgrade_notify_stale", "", "version="+nctx.Version)
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
