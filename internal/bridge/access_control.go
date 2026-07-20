package bridge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"lark-agent-bridge/internal/access"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/feishu"
)

func (s *Service) RunAccessRefresh(ctx context.Context) {
	if s.AccessInfo == nil || s.AccessControls == nil || s.AccessAppID == "" {
		return
	}
	_ = s.refreshKnownChats(ctx)
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := access.RefreshOwner(ctx, s.AccessControls, s.AccessInfo, s.AccessAppID); err != nil {
				s.Audit.Record("system", "owner_refresh_failed", "", err.Error())
			}
			_ = s.refreshKnownChats(ctx)
		}
	}
}

func (s *Service) refreshKnownChats(ctx context.Context) error {
	chats, err := s.AccessInfo.ListChats(ctx)
	if err != nil {
		s.Audit.Record("system", "known_chats_refresh_failed", "", err.Error())
		return err
	}
	if len(chats) > 0 {
		s.accessMu.Lock()
		s.knownChats = append([]feishu.KnownChat(nil), chats...)
		s.accessMu.Unlock()
	}
	return nil
}

func (s *Service) populateAccessConfigForm(form *card.ConfigForm) {
	if form == nil || s.Access == nil {
		return
	}
	policy := s.Access.Get()
	form.AllowedUsers = policy.AllowedUsers
	form.Admins = policy.Admins
	s.accessMu.RLock()
	known := append([]feishu.KnownChat(nil), s.knownChats...)
	s.accessMu.RUnlock()
	names := make(map[string]string, len(known))
	for _, chat := range known {
		names[chat.ID] = chat.Name
	}
	for _, id := range policy.AllowedChats {
		form.AllowedChats = append(form.AllowedChats, card.AccessChat{ID: id, Name: names[id]})
	}
	snapshot := s.AccessControls.Snapshot()
	owner := "missing"
	if snapshot.BotOwnerID != "" {
		owner = "present"
	}
	form.OwnerState = fmt.Sprintf("%s owner=%s", snapshot.OwnerRefreshState, owner)
}

func (s *Service) messageAccessDecision(msg Message) (access.Decision, bool) {
	if s.Access == nil {
		return access.Decision{OK: true}, false
	}
	policy := s.Access.Get()
	if msg.IsGroup {
		return access.CanUseGroup(policy, s.AccessControls, msg.ChatID, msg.Sender), true
	}
	return access.CanUseDM(policy, s.AccessControls, msg.Sender), true
}

func (s *Service) adminCommand(kind CommandType) bool {
	return kind == CommandConfig || kind == CommandAgentMode || kind == CommandInvite || kind == CommandRemove
}

func (s *Service) canRunAdminCommand(sender string) bool {
	if s.Access == nil {
		return true
	}
	return access.CanRunAdminCommand(s.Access.Get(), s.AccessControls, sender).OK
}

func (s *Service) handleInviteCommand(ctx context.Context, msg Message, cmd Command, preference config.RuntimePreference) error {
	tokens := lowerFields(cmd.Text)
	if hasToken(tokens, "all") && hasToken(tokens, "group") {
		if s.AccessInfo == nil {
			return s.accessReply(msg, preference, card.SegmentError, "群列表服务不可用。")
		}
		chats, err := s.AccessInfo.ListChats(ctx)
		if err != nil {
			return s.accessReply(msg, preference, card.SegmentError, "获取 bot 所在群失败，请稍后重试。")
		}
		added := 0
		err = s.Access.Update(func(p *access.Policy) {
			seen := stringSet(p.AllowedChats)
			for _, chat := range chats {
				if _, ok := seen[chat.ID]; !ok {
					p.AllowedChats = append(p.AllowedChats, chat.ID)
					seen[chat.ID] = struct{}{}
					added++
				}
			}
		})
		if err != nil {
			return s.accessSaveFailed(msg, preference, err)
		}
		if len(chats) == 0 {
			return s.accessReply(msg, preference, card.SegmentText, "当前 bot 还不在任何群里，没有可加入的群。")
		}
		return s.accessReply(msg, preference, card.SegmentText, fmt.Sprintf("✅ 已把 bot 所在的 %d 个群加入响应群名单（共 %d 个）。", added, len(s.Access.Get().AllowedChats)))
	}
	kind := firstKind(tokens)
	if kind == "" {
		return s.accessReply(msg, preference, card.SegmentError, "用法：/invite user @某人；/invite admin @某人；/invite group；/invite all group")
	}
	if kind == "group" {
		if !msg.IsGroup {
			return s.accessReply(msg, preference, card.SegmentError, "❌ /invite group 只能在群里发。")
		}
		already := false
		if err := s.Access.Update(func(p *access.Policy) {
			already = hasString(p.AllowedChats, msg.ChatID)
			if !already {
				p.AllowedChats = append(p.AllowedChats, msg.ChatID)
			}
		}); err != nil {
			return s.accessSaveFailed(msg, preference, err)
		}
		if already {
			return s.accessReply(msg, preference, card.SegmentText, "✅ 当前群已在白名单里，无需重复添加。")
		}
		return s.accessReply(msg, preference, card.SegmentText, fmt.Sprintf("✅ 已把当前群（`%s`）加入响应群名单。", msg.ChatID))
	}
	return s.mutateMentionList(msg, preference, kind, true)
}

func (s *Service) handleRemoveCommand(msg Message, cmd Command, preference config.RuntimePreference) error {
	kind := firstKind(lowerFields(cmd.Text))
	if kind == "" {
		return s.accessReply(msg, preference, card.SegmentError, "用法：/remove user @某人；/remove admin @某人；/remove group")
	}
	if kind == "group" {
		if !msg.IsGroup {
			return s.accessReply(msg, preference, card.SegmentError, "/remove group 请在要移除的群里发。")
		}
		missing := false
		if err := s.Access.Update(func(p *access.Policy) {
			missing = !hasString(p.AllowedChats, msg.ChatID)
			p.AllowedChats = removeString(p.AllowedChats, msg.ChatID)
		}); err != nil {
			return s.accessSaveFailed(msg, preference, err)
		}
		if missing {
			return s.accessReply(msg, preference, card.SegmentText, "✅ 当前群本来就不在响应名单里，无需移除。")
		}
		return s.accessReply(msg, preference, card.SegmentText, "✅ 已把当前群移出响应群名单。")
	}
	return s.mutateMentionList(msg, preference, kind, false)
}

func (s *Service) mutateMentionList(msg Message, preference config.RuntimePreference, kind string, add bool) error {
	targets := make([]Mention, 0, len(msg.Mentions))
	for _, mention := range msg.Mentions {
		if !mention.IsBot && mention.OpenID != "" {
			targets = append(targets, mention)
		}
	}
	if len(targets) == 0 {
		return s.accessReply(msg, preference, card.SegmentError, fmt.Sprintf("❌ 没检测到 @ 的用户。请像这样发：/%s %s @某人。", map[bool]string{true: "invite", false: "remove"}[add], kind))
	}
	changed, unchanged := []string{}, []string{}
	err := s.Access.Update(func(p *access.Policy) {
		list := &p.AllowedUsers
		if kind == "admin" {
			list = &p.Admins
		}
		for _, target := range targets {
			name := target.Name
			if name == "" {
				name = target.OpenID
			}
			present := hasString(*list, target.OpenID)
			if add && !present {
				*list = append(*list, target.OpenID)
				changed = append(changed, name)
			} else if !add && present {
				*list = removeString(*list, target.OpenID)
				changed = append(changed, name)
			} else {
				unchanged = append(unchanged, name)
			}
		}
	})
	if err != nil {
		return s.accessSaveFailed(msg, preference, err)
	}
	label := "用户白名单"
	if kind == "admin" {
		label = "管理员"
	}
	parts := []string{}
	if len(changed) > 0 {
		verb := "加入"
		if !add {
			verb = "移出"
		}
		parts = append(parts, fmt.Sprintf("✅ 已把 %s %s%s。", strings.Join(changed, "、"), verb, label))
	}
	if len(unchanged) > 0 {
		parts = append(parts, fmt.Sprintf("%s 无需变更。", strings.Join(unchanged, "、")))
	}
	return s.accessReply(msg, preference, card.SegmentText, strings.Join(parts, "\n"))
}

func (s *Service) accessReply(msg Message, preference config.RuntimePreference, kind card.SegmentKind, text string) error {
	return s.renderTextWithMode("access", msg.ID, kind, text, preference.ConversationMode)
}

func (s *Service) accessSaveFailed(msg Message, preference config.RuntimePreference, err error) error {
	s.Audit.Record(msg.Sender, "access_save_failed", msg.ChatID, err.Error())
	return s.accessReply(msg, preference, card.SegmentError, "访问控制保存失败，请检查存储状态。")
}

func lowerFields(value string) []string {
	fields := strings.Fields(strings.ToLower(value))
	return fields
}
func hasToken(values []string, value string) bool { return hasString(values, value) }
func firstKind(values []string) string {
	for _, value := range values {
		if value == "user" || value == "admin" || value == "group" {
			return value
		}
	}
	return ""
}
func hasString(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func removeString(values []string, value string) []string {
	out := values[:0]
	for _, v := range values {
		if v != value {
			out = append(out, v)
		}
	}
	return out
}
func stringSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, v := range values {
		out[v] = struct{}{}
	}
	return out
}
