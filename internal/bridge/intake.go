package bridge

import (
	"strings"

	"lark-agent-bridge/internal/config"
)

const (
	IntakeReasonAccepted        = "accepted"
	IntakeReasonSelf            = "self"
	IntakeReasonBotDisabled     = "bot_disabled"
	IntakeReasonMentionRequired = "mention_required"
	IntakeReasonTopicNotJoined  = "topic_not_participated"
	// 用户转发/分享的素材消息(interactive、share_*、merge_forward)在 P2P 会被独立
	// 触发 agent run,与紧接着 @bot 的真实指令形成"素材+指令"双回复。视为素材、跳过
	// 独立回复;下一条 text/post 指令通过 pendingMergeForward 附带其内容作为上下文。
	IntakeReasonForwardMaterial = "forward_material"
)

type IntakeDecision struct {
	Accept bool
	Reason string
	Mark   bool
	Touch  bool
}

// forwardMaterialMessageTypes 是 P2P 场景下被视为"转发/分享素材"、不应独立触发
// agent run 的 Feishu 消息类型集合。text/post/图片/文件/音视频/贴纸仍按原路径接受
// (用户直接发一张图让 bot OCR 之类的场景不受影响)。
var forwardMaterialMessageTypes = map[string]struct{}{
	"share_chat":    {},
	"share_user":    {},
	"share_message": {},
	"merge_forward": {},
	"interactive":   {},
}

func isForwardMaterial(messageType string) bool {
	_, ok := forwardMaterialMessageTypes[strings.ToLower(strings.TrimSpace(messageType))]
	return ok
}

func DecideIntake(msg Message, preference config.RuntimePreference, selfOpenID string, participated bool) IntakeDecision {
	if reason := senderRejectionReason(msg, preference.RespondToBots, selfOpenID); reason != "" {
		return IntakeDecision{Reason: reason}
	}
	if !msg.IsGroup {
		// P2P 独立单聊场景无 mention gating,一切消息都会走 agent。这里对"用户转
		// 发/分享的素材消息"提前跳过,避免它们独立触发一次 agent run;真实指令仍
		// 会随下一条 text/post 消息进入,并可通过 pendingMergeForward 携带这段
		// forwarded 素材作为上下文。
		if isForwardMaterial(msg.MessageType) {
			return IntakeDecision{Reason: IntakeReasonForwardMaterial}
		}
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
