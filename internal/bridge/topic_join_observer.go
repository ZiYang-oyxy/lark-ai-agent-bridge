package bridge

import (
	"time"

	"lark-agent-bridge/internal/audit"
)

// TopicJoinObserver wraps an audit recorder with an auto-join callback that
// marks a topic as participated as soon as CardKit reports a successful reply
// with a non-empty Feishu thread_id. This closes the gap where the top-level
// @bot message carries thread_id="", the bot's card reply is the first place
// the real omt_* id becomes visible, and the very next user message in that
// topic (typically posted without @bot when GroupMessageMode is
// participated_topics or all) would otherwise be dropped as
// topic_not_participated.
type TopicJoinObserver struct {
	Recorder *audit.Recorder
	Topics   TopicParticipation
}

// NewTopicJoinObserver constructs a TopicJoinObserver bound to the given
// recorder and topic-participation store. Both may be nil, in which case the
// corresponding side effect is skipped.
func NewTopicJoinObserver(recorder *audit.Recorder, topics TopicParticipation) *TopicJoinObserver {
	return &TopicJoinObserver{Recorder: recorder, Topics: topics}
}

// Record forwards to the underlying audit recorder.
func (o *TopicJoinObserver) Record(actor, action, sessionID, detail string) {
	if o == nil || o.Recorder == nil {
		return
	}
	o.Recorder.Record(actor, action, sessionID, detail)
}

// RecordCardReply marks the topic as participated. The observer records a
// dedicated audit event so operators can distinguish auto-join from user-@
// participation.
func (o *TopicJoinObserver) RecordCardReply(chatID, threadID, replyToMessageID, messageID string, at time.Time) {
	if o == nil {
		return
	}
	if chatID == "" || threadID == "" {
		return
	}
	if o.Topics != nil {
		if err := o.Topics.Mark(chatID, threadID, at); err != nil {
			if o.Recorder != nil {
				o.Recorder.Record("system", "topic_participation_save_failed",
					chatID,
					"auto_join_via_card_reply reply_to="+replyToMessageID+" thread="+threadID+" card_message="+messageID+" error="+err.Error())
			}
			return
		}
	}
	if o.Recorder != nil {
		o.Recorder.Record("system", "topic_participation_auto_joined",
			chatID,
			"reply_to="+replyToMessageID+" thread="+threadID+" card_message="+messageID)
	}
}
