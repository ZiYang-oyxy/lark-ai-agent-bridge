package bridge

import (
	"testing"

	"lark-agent-bridge/internal/config"
)

func TestDecideIntakeGroupMessageMatrix(t *testing.T) {
	tests := []struct {
		name         string
		msg          Message
		mode         config.GroupMessageMode
		respondBots  bool
		participated bool
		wantAccept   bool
		wantMark     bool
		wantTouch    bool
		wantReason   string
	}{
		{name: "dm", msg: Message{}, mode: config.GroupMessageModeMentionOnly, wantAccept: true},
		{name: "dm text", msg: Message{MessageType: "text"}, mode: config.GroupMessageModeMentionOnly, wantAccept: true},
		{name: "dm image", msg: Message{MessageType: "image"}, mode: config.GroupMessageModeMentionOnly, wantAccept: true},
		{name: "dm file", msg: Message{MessageType: "file"}, mode: config.GroupMessageModeMentionOnly, wantAccept: true},
		{name: "dm post", msg: Message{MessageType: "post"}, mode: config.GroupMessageModeMentionOnly, wantAccept: true},
		{name: "dm interactive forwarded", msg: Message{MessageType: "interactive"}, mode: config.GroupMessageModeMentionOnly, wantReason: IntakeReasonForwardMaterial},
		{name: "dm share_message forwarded", msg: Message{MessageType: "share_message"}, mode: config.GroupMessageModeMentionOnly, wantReason: IntakeReasonForwardMaterial},
		{name: "dm share_chat forwarded", msg: Message{MessageType: "share_chat"}, mode: config.GroupMessageModeMentionOnly, wantReason: IntakeReasonForwardMaterial},
		{name: "dm share_user forwarded", msg: Message{MessageType: "share_user"}, mode: config.GroupMessageModeMentionOnly, wantReason: IntakeReasonForwardMaterial},
		{name: "dm merge_forward", msg: Message{MessageType: "merge_forward"}, mode: config.GroupMessageModeMentionOnly, wantReason: IntakeReasonForwardMaterial},
		// 群里 interactive/merge_forward 走原来的 mention-only 门禁,不被 forward
		// material 分支影响(groups already gate on Mentioned)。
		{name: "group interactive mentioned still accepts", msg: Message{IsGroup: true, MessageType: "interactive", Mentioned: true}, mode: config.GroupMessageModeMentionOnly, wantAccept: true},
		{name: "mention only mentioned", msg: Message{IsGroup: true, Mentioned: true}, mode: config.GroupMessageModeMentionOnly, wantAccept: true},
		{name: "mention only mention all", msg: Message{IsGroup: true, MentionAll: true}, mode: config.GroupMessageModeMentionOnly},
		{name: "participated new topic mention", msg: Message{IsGroup: true, ThreadID: "t", Mentioned: true}, mode: config.GroupMessageModeParticipatedTopics, wantAccept: true, wantMark: true},
		{name: "participated topic followup", msg: Message{IsGroup: true, ThreadID: "t"}, mode: config.GroupMessageModeParticipatedTopics, participated: true, wantAccept: true, wantTouch: true},
		{name: "participated unknown topic", msg: Message{IsGroup: true, ThreadID: "t"}, mode: config.GroupMessageModeParticipatedTopics},
		{name: "participated ordinary group", msg: Message{IsGroup: true}, mode: config.GroupMessageModeParticipatedTopics},
		{name: "all ordinary group", msg: Message{IsGroup: true}, mode: config.GroupMessageModeAll, wantAccept: true},
		{name: "all known topic touches", msg: Message{IsGroup: true, ThreadID: "t"}, mode: config.GroupMessageModeAll, participated: true, wantAccept: true, wantTouch: true},
		{name: "bot disabled", msg: Message{IsGroup: true, SenderType: "bot", Mentioned: true}, mode: config.GroupMessageModeAll},
		{name: "bot enabled", msg: Message{IsGroup: true, SenderType: "bot", Mentioned: true}, mode: config.GroupMessageModeMentionOnly, respondBots: true, wantAccept: true},
		{name: "unknown sender treated as user", msg: Message{IsGroup: true, SenderType: "", Mentioned: true}, mode: config.GroupMessageModeMentionOnly, wantAccept: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideIntake(tc.msg, config.RuntimePreference{GroupMessageMode: tc.mode, RespondToBots: tc.respondBots}, "ou_self", tc.participated)
			if got.Accept != tc.wantAccept || got.Mark != tc.wantMark || got.Touch != tc.wantTouch {
				t.Fatalf("decision = %#v", got)
			}
			if tc.wantReason != "" && got.Reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", got.Reason, tc.wantReason)
			}
		})
	}
}

func TestDecideIntakeAlwaysRejectsSelf(t *testing.T) {
	got := DecideIntake(Message{IsGroup: true, Sender: "ou_self", SenderType: "bot", Mentioned: true}, config.RuntimePreference{GroupMessageMode: config.GroupMessageModeAll, RespondToBots: true}, "ou_self", true)
	if got.Accept || got.Reason != IntakeReasonSelf {
		t.Fatalf("decision = %#v", got)
	}
}
