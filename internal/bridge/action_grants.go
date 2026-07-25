package bridge

import (
	"fmt"
	"time"

	"lark-agent-bridge/internal/actiongrant"
	"lark-agent-bridge/internal/card"
)

const defaultActionGrantTTL = 24 * time.Hour

func protectedAction(actionID string) bool {
	switch actionID {
	case "stop", "create_workdir", "cancel_workdir", "schedule.confirm", "schedule.cancel", "resume.select":
		return true
	default:
		return false
	}
}

func (s *Service) issueActionGrant(actor, chatID, sessionID, actionID, value string, expiresAt time.Time) (string, error) {
	return s.issueActionGrantWithAdmin(actor, chatID, sessionID, actionID, value, expiresAt, true)
}

func (s *Service) issueActionGrantWithAdmin(actor, chatID, sessionID, actionID, value string, expiresAt time.Time, allowAdmin bool) (string, error) {
	if s.ActionGrants == nil {
		return "", nil
	}
	now := time.Now()
	if expiresAt.IsZero() {
		expiresAt = now.Add(defaultActionGrantTTL)
	}
	grant, err := s.ActionGrants.Issue(actiongrant.Spec{
		Actor: actor, ChatID: chatID, SessionID: sessionID, ActionID: actionID, Value: value,
		PolicyRevision: s.accessPolicyRevision(), AllowAdmin: allowAdmin, ExpiresAt: expiresAt,
	}, now)
	if err != nil {
		return "", err
	}
	return grant.ID, nil
}

func (s *Service) attachResumeGrants(resume *card.ResumeCard, actor, chatID, sessionID string, expiresAt time.Time) error {
	if resume == nil || s.ActionGrants == nil {
		return nil
	}
	for i := range resume.Items {
		if resume.Items[i].Current {
			continue
		}
		grantID, err := s.issueActionGrant(actor, chatID, sessionID, "resume.select", resume.Items[i].SessionID, expiresAt)
		if err != nil {
			return err
		}
		resume.Items[i].GrantID = grantID
	}
	return nil
}

func (s *Service) attachActionGrants(event *card.Event, actor, chatID string, expiresAt time.Time) error {
	if event == nil || s.ActionGrants == nil {
		return nil
	}
	for i := range event.Actions {
		if event.Actions[i].Disabled || !protectedAction(event.Actions[i].ID) {
			continue
		}
		grantID, err := s.issueActionGrant(actor, chatID, event.SessionID, event.Actions[i].ID, event.Actions[i].Value, expiresAt)
		if err != nil {
			return err
		}
		event.Actions[i].GrantID = grantID
	}
	return nil
}

func (s *Service) authorizeAction(req ActionRequest) (actiongrant.Decision, error) {
	if s.ActionGrants == nil || !protectedAction(req.ActionID) {
		return actiongrant.Decision{OK: true, Reason: actiongrant.ReasonAllowed}, nil
	}
	actorIsAdmin := s.Access != nil && s.canRunAdminCommand(req.Actor)
	return s.ActionGrants.Consume(actiongrant.Request{
		GrantID: req.GrantID, Actor: req.Actor, ChatID: req.ChatID, SessionID: req.SessionID, ActionID: req.ActionID,
		Value: req.Value, PolicyRevision: s.accessPolicyRevision(), ActorIsAdmin: actorIsAdmin, Now: time.Now(),
	})
}

func (s *Service) accessPolicyRevision() uint64 {
	if s.Access == nil {
		return 0
	}
	return s.Access.Revision()
}

func actionDeniedEvent(req ActionRequest, reason actiongrant.Reason) card.Event {
	return card.Event{
		Type: "error", SessionID: req.SessionID,
		Segments: []card.Segment{{Kind: card.SegmentError, Text: fmt.Sprintf("此卡片操作已失效或无权执行（%s），请重新打开对应卡片。", reason)}},
	}
}
