package bridge

import "lark-agent-bridge/internal/config"

const (
	IntakeReasonAccepted        = "accepted"
	IntakeReasonSelf            = "self"
	IntakeReasonBotDisabled     = "bot_disabled"
	IntakeReasonMentionRequired = "mention_required"
	IntakeReasonTopicNotJoined  = "topic_not_participated"
)

type IntakeDecision struct {
	Accept bool
	Reason string
	Mark   bool
	Touch  bool
}

func DecideIntake(msg Message, preference config.RuntimePreference, selfOpenID string, participated bool) IntakeDecision {
	if reason := senderRejectionReason(msg, preference.RespondToBots, selfOpenID); reason != "" {
		return IntakeDecision{Reason: reason}
	}
	if !msg.IsGroup {
		return IntakeDecision{Accept: true, Reason: IntakeReasonAccepted}
	}
	mark := msg.Mentioned && msg.ThreadID != ""
	if msg.Mentioned {
		return IntakeDecision{Accept: true, Reason: IntakeReasonAccepted, Mark: mark}
	}
	mode := preference.GroupMessageMode
	if mode == "" {
		mode = config.GroupMessageModeMentionOnly
	}
	switch mode {
	case config.GroupMessageModeAll:
		return IntakeDecision{Accept: true, Reason: IntakeReasonAccepted, Touch: participated && msg.ThreadID != ""}
	case config.GroupMessageModeParticipatedTopics:
		if msg.ThreadID != "" && participated {
			return IntakeDecision{Accept: true, Reason: IntakeReasonAccepted, Touch: true}
		}
		if msg.ThreadID != "" {
			return IntakeDecision{Reason: IntakeReasonTopicNotJoined}
		}
		return IntakeDecision{Reason: IntakeReasonMentionRequired}
	default:
		return IntakeDecision{Reason: IntakeReasonMentionRequired}
	}
}

func senderRejectionReason(msg Message, respondToBots bool, selfOpenID string) string {
	if selfOpenID != "" && msg.Sender == selfOpenID {
		return IntakeReasonSelf
	}
	if msg.SenderType == "bot" && !respondToBots {
		return IntakeReasonBotDisabled
	}
	return ""
}
