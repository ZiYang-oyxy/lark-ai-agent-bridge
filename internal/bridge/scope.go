package bridge

import (
	"context"
	"errors"
	"fmt"

	"github.com/larksuite/oapi-sdk-go/v3/scene/registration"

	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/feishu"
)

const groupMessageScope = "im:message.group_msg"

type ScopeInspector interface {
	InspectTenantScope(context.Context, string, string) (feishu.ScopeState, error)
}

func (s *Service) ensureGroupMessageScope(sessionID string, mode config.GroupMessageMode) {
	if mode == config.GroupMessageModeMentionOnly || s.ScopeInspector == nil || s.AccessAppID == "" {
		return
	}
	s.scopeMu.Lock()
	if s.activeScopeCancel != nil {
		s.activeScopeCancel()
	}
	ctx, cancel := context.WithCancel(s.scopeContext)
	s.activeScopeCancel = cancel
	s.scopeWG.Add(1)
	s.scopeMu.Unlock()
	go func() {
		defer s.scopeWG.Done()
		s.runGroupMessageScopeCheck(ctx, sessionID)
	}()
}

func (s *Service) runGroupMessageScopeCheck(ctx context.Context, sessionID string) {
	state, err := s.ScopeInspector.InspectTenantScope(ctx, s.AccessAppID, groupMessageScope)
	if ctx.Err() != nil {
		return
	}
	switch state {
	case feishu.ScopePresent:
		return
	case feishu.ScopeUnknown:
		s.Audit.Record("system", "group_message_scope_check_failed", sessionID, safeScopeError(err))
		s.renderScopeEvent(card.Event{Type: "scope_unknown", SessionID: sessionID, Segments: []card.Segment{{Kind: card.SegmentError, Text: "群消息权限状态暂时无法确认；现有 @bot 功能不受影响。"}}})
		return
	case feishu.ScopeMissing:
		s.Audit.Record("system", "group_message_scope_missing", sessionID, groupMessageScope)
	default:
		s.Audit.Record("system", "group_message_scope_check_failed", sessionID, "unexpected state")
		return
	}
	if s.ScopeGrants == nil {
		s.renderScopeEvent(card.Event{Type: "scope_missing", SessionID: sessionID, Segments: []card.Segment{{Kind: card.SegmentError, Text: "缺少 `im:message.group_msg`，且当前未配置增量授权服务。"}}})
		return
	}
	grant, err := s.ScopeGrants.Begin(ctx, s.AccessAppID, []string{groupMessageScope})
	if err != nil {
		if ctx.Err() == nil {
			s.Audit.Record("system", "group_message_scope_grant_failed", sessionID, safeScopeError(err))
			s.renderScopeEvent(scopeGrantFailureEvent(sessionID, err))
		}
		return
	}
	s.Audit.Record("system", "group_message_scope_grant_started", sessionID, "scope="+groupMessageScope)
	expires := grant.ExpiresAt.Local().Format("15:04:05")
	s.renderScopeEvent(card.Event{
		Type:      "scope_grant_pending",
		SessionID: sessionID,
		Segments:  []card.Segment{{Kind: card.SegmentText, Text: fmt.Sprintf("需要授权 `%s`。链接约在 %s 过期；完成后 Bridge 会再次确认权限。", groupMessageScope, expires)}},
		Actions:   []card.Action{{ID: "scope_grant", Label: "打开授权页面", URL: grant.URL}},
	})
	if grant.Done == nil {
		s.renderScopeEvent(card.Event{Type: "scope_grant_failed", SessionID: sessionID, Segments: []card.Segment{{Kind: card.SegmentError, Text: "授权流程未返回完成状态，请重新打开 `/config`。"}}})
		return
	}
	select {
	case <-ctx.Done():
		return
	case err = <-grant.Done:
	}
	if err != nil {
		s.Audit.Record("system", "group_message_scope_grant_failed", sessionID, safeScopeError(err))
		s.renderScopeEvent(scopeGrantFailureEvent(sessionID, err))
		return
	}
	state, err = s.ScopeInspector.InspectTenantScope(ctx, s.AccessAppID, groupMessageScope)
	if ctx.Err() != nil {
		return
	}
	if state == feishu.ScopePresent {
		s.Audit.Record("system", "group_message_scope_granted", sessionID, groupMessageScope)
		s.renderScopeEvent(card.Event{Type: "scope_granted", SessionID: sessionID, Segments: []card.Segment{{Kind: card.SegmentText, Text: "`im:message.group_msg` 已确认生效。"}}})
		return
	}
	s.Audit.Record("system", "group_message_scope_check_failed", sessionID, "post-grant state="+string(state)+" "+safeScopeError(err))
	s.renderScopeEvent(card.Event{Type: "scope_not_effective", SessionID: sessionID, Segments: []card.Segment{{Kind: card.SegmentError, Text: "授权已完成，但权限尚未确认生效；非 @ 消息目前可能仍不会到达 Bridge。"}}})
}

func (s *Service) renderScopeEvent(event card.Event) {
	if err := s.Cards.Render(event); err != nil {
		s.Audit.Record("system", "group_message_scope_card_failed", event.SessionID, err.Error())
	}
}

func scopeGrantFailureEvent(sessionID string, err error) card.Event {
	eventType := "scope_grant_failed"
	text := "增量授权失败，请重新打开 `/config` 后重试。"
	var denied *registration.AccessDeniedError
	var expired *registration.ExpiredError
	switch {
	case errors.As(err, &denied):
		eventType, text = "scope_grant_denied", "授权已被拒绝；现有 @bot 功能不受影响。"
	case errors.As(err, &expired):
		eventType, text = "scope_grant_expired", "授权链接已过期，请重新打开 `/config`。"
	}
	return card.Event{Type: eventType, SessionID: sessionID, Segments: []card.Segment{{Kind: card.SegmentError, Text: text}}}
}

func safeScopeError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
