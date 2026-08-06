package bridge

import (
	"context"
	"strings"

	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/feishu"
)

// topicPrecreateGuideText is the body of the seed message the bridge sends to
// persuade Feishu to allocate a real thread_id when the user's
// top-level @bot message is msg_type=post. Feishu only honours
// reply_in_thread=true on msg_type=text; a post never gets a thread_id
// assigned, which strands the bridge in a synthetic-only key that never shows
// up in the Feishu topic sidebar. Keep the seed as an explicit guide: recalling
// it leaves a permanent "message recalled" system placeholder in Feishu.
const topicPrecreateGuideText = "🧵 AI 回复将在下方持续更新。"

// shouldPrecreateTopicForPost decides whether runWithPreference should preflight
// a topic before the real agent stream card. The four preconditions map 1:1 to
// the failing case observed empirically:
//   - preference asks for topic mode
//   - message is a top-level @bot mention (no existing thread_id)
//   - message body is a Feishu post (which Feishu refuses to attach a thread
//     to via reply_in_thread=true)
//   - we have a stable message id to route the seed reply to
//
// text/image/other types either already get a thread_id from Feishu on the
// first reply_in_thread reply, or belong to a different routing branch
// (participation_topics follow-ups without @bot); we deliberately leave them
// alone to keep the change minimal.
func shouldPrecreateTopicForPost(msg Message, mode config.ConversationMode) bool {
	if mode != config.ConversationModeTopic {
		return false
	}
	if !msg.ExplicitBotMention {
		return false
	}
	if strings.TrimSpace(msg.ThreadID) != "" {
		return false
	}
	if strings.TrimSpace(msg.ID) == "" {
		return false
	}
	if strings.ToLower(strings.TrimSpace(msg.MessageType)) != "post" {
		return false
	}
	return true
}

// precreateTopicForPost sends a minimal reply-in-thread probe so Feishu assigns
// a real thread_id, then wires that id back to the synthetic "@bot:<msg_id>"
// key the router would otherwise use. Returns the assigned thread_id on
// success. On any failure — no sender configured, API error, or a Feishu
// response without a thread_id — returns an empty string so the caller can
// fall back to the pre-existing synthetic-only routing without disturbing
// message delivery. Every outcome is audited so operators can see when the
// guide is sent and why topic creation fails.
//
// The guide message remains visible after the thread_id is captured. Feishu
// renders recalled messages as persistent system placeholders, so deleting the
// seed would replace useful context with unexplained "message recalled" noise.
func (s *Service) precreateTopicForPost(ctx context.Context, msg Message) string {
	if s == nil || s.Notifier == nil {
		return ""
	}
	syntheticThread := SyntheticTopicThreadPrefix + msg.ID
	result, err := s.Notifier.SendReply(ctx, feishu.Reply{
		ReplyToMessageID: msg.ID,
		ReplyInThread:    true,
		Message:          topicPrecreateGuideText,
		ShouldReply:      true,
		Kind:             feishu.ReplyKindPlaceholder,
	})
	if err != nil {
		s.Audit.Record(msg.Sender, "topic_precreate_failed", msg.ChatID,
			"message="+msg.ID+" reason="+err.Error())
		return ""
	}
	threadID := strings.TrimSpace(result.ThreadID)
	if threadID == "" {
		s.Audit.Record(msg.Sender, "topic_precreate_failed", msg.ChatID,
			"message="+msg.ID+" reason=empty_thread_id guide_message="+result.MessageID)
		return ""
	}
	if s.TopicAliases != nil {
		s.TopicAliases.Bind(msg.ChatID, threadID, syntheticThread)
	}
	s.Audit.Record(msg.Sender, "topic_precreate_ok", msg.ChatID,
		"message="+msg.ID+" thread="+threadID+" synthetic="+syntheticThread+" guide_message="+result.MessageID)
	return threadID
}
