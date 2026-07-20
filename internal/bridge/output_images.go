package bridge

import (
	"context"
	"crypto/sha1"
	"fmt"

	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/feishu"
	"lark-agent-bridge/internal/outputmedia"
	"lark-agent-bridge/internal/session"
)

func (s *Service) finishStreamWithOutputImages(ctx context.Context, stream *agentCardStream, cardStatus string, meta card.Meta, result AgentRunResult, sess session.Session, batch session.Batch) {
	if cardStatus != "completed" || s.OutputImages == nil || len(batch.Inputs) == 0 {
		s.finishStreamAndAudit(stream, cardStatus, meta, result, sess.ID)
		return
	}

	var plan *outputmedia.Plan
	terminal, err := stream.FinishTransformed(cardStatus, meta, result, func(event card.Event) card.Event {
		plan = outputmedia.Prepare(sess.WorkDir, event.Segments)
		if plan.HasImages() {
			event.Segments = plan.PendingSegments()
		}
		return event
	})
	if plan != nil {
		defer plan.Close()
	}
	if err != nil {
		s.Audit.Record("system", "terminal_card_render_failed", sess.ID, fmt.Sprintf("status=%s error=%v", cardStatus, err))
		return
	}
	if plan == nil || !plan.HasImages() {
		return
	}

	items := plan.Items()
	results := make([]outputmedia.Result, len(items))
	succeeded := 0
	failed := 0
	input := batch.Inputs[0]
	replyInThread := input.ConversationMode == config.ConversationModeTopic
	for i, item := range items {
		if item.Rejection != "" {
			results[i] = outputmedia.Result{State: outputmedia.ResultFailed, Reason: item.Rejection}
			failed++
			s.Audit.Record("system", "output_image_rejected", sess.ID, outputImageAuditDetail(item, "validate", item.Rejection, ""))
			continue
		}
		if item.ReuseOf >= 0 {
			results[i] = results[item.ReuseOf]
			if results[i].State == outputmedia.ResultSent {
				succeeded++
			} else {
				failed++
			}
			continue
		}

		imageKey, uploadErr := s.OutputImages.UploadImage(ctx, item.Reader)
		if uploadErr != nil {
			results[i] = outputmedia.Result{State: outputmedia.ResultFailed, Reason: outputmedia.ReasonUploadFailed}
			failed++
			s.Audit.Record("system", "output_image_failed", sess.ID, outputImageAuditDetail(item, "upload", outputmedia.ReasonUploadFailed, ""))
			continue
		}
		s.Audit.Record("system", "output_image_uploaded", sess.ID, outputImageAuditDetail(item, "upload", "", ""))
		replyResult, replyErr := s.OutputImages.ReplyImage(ctx, feishu.ImageReply{
			ReplyToMessageID: input.ReplyToMessageID,
			ReplyInThread:    replyInThread,
			ImageKey:         imageKey,
			UUID:             outputImageReplyUUID(input.ReplyToMessageID, terminal.SessionID, item.Index),
		})
		if replyErr != nil {
			results[i] = outputmedia.Result{State: outputmedia.ResultFailed, Reason: outputmedia.ReasonReplyFailed}
			failed++
			s.Audit.Record("system", "output_image_failed", sess.ID, outputImageAuditDetail(item, "reply", outputmedia.ReasonReplyFailed, ""))
			continue
		}
		results[i] = outputmedia.Result{State: outputmedia.ResultSent}
		succeeded++
		detail := outputImageAuditDetail(item, "reply", "", replyResult.MessageID)
		detail += " conversation_mode=" + string(input.ConversationMode)
		s.Audit.Record("system", "output_image_replied", sess.ID, detail)
	}

	terminal.Segments = plan.FinalSegments(results)
	if err := stream.RenderTerminalUpdate(terminal); err != nil {
		s.Audit.Record("system", "terminal_card_render_failed", sess.ID, fmt.Sprintf("status=completed_output_images error=%v", err))
	}
	s.Audit.Record("system", "output_images_completed", sess.ID, fmt.Sprintf("candidates=%d succeeded=%d failed=%d", len(items), succeeded, failed))
}

func outputImageAuditDetail(item outputmedia.Item, stage string, reason outputmedia.ReasonCode, messageID string) string {
	detail := fmt.Sprintf("index=%d mime=%s size=%d stage=%s", item.Index+1, item.MIME, item.Size, stage)
	if reason != "" {
		detail += " reason=" + string(reason)
	}
	if messageID != "" {
		detail += " message_id=" + messageID
	}
	return detail
}

func outputImageReplyUUID(sourceMessageID, sessionID string, index int) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("output-image:%s:%s:%d", sourceMessageID, sessionID, index)))
	var uuid [16]byte
	copy(uuid[:], sum[:16])
	uuid[6] = (uuid[6] & 0x0f) | 0x50
	uuid[8] = (uuid[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		uuid[0], uuid[1], uuid[2], uuid[3],
		uuid[4], uuid[5], uuid[6], uuid[7],
		uuid[8], uuid[9], uuid[10], uuid[11], uuid[12], uuid[13], uuid[14], uuid[15],
	)
}
