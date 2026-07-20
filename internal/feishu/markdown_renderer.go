package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"lark-agent-bridge/internal/card"
)

type MarkdownMessageAPI interface {
	Reply(context.Context, *larkim.ReplyMessageReq, ...larkcore.RequestOptionFunc) (*larkim.ReplyMessageResp, error)
	Patch(context.Context, *larkim.PatchMessageReq, ...larkcore.RequestOptionFunc) (*larkim.PatchMessageResp, error)
}

type MarkdownTarget struct {
	api    MarkdownMessageAPI
	render func(card.Event) string
}

func NewMarkdownTarget(sender *SDKSender, render func(card.Event) string) *MarkdownTarget {
	if sender == nil {
		return &MarkdownTarget{render: render}
	}
	return newMarkdownTarget(sender.markdownAPI, render)
}

func newMarkdownTarget(api MarkdownMessageAPI, render func(card.Event) string) *MarkdownTarget {
	return &MarkdownTarget{api: api, render: render}
}

func (t *MarkdownTarget) Begin(ctx context.Context, replyTo string, replyInThread bool) (card.Renderer, error) {
	if t == nil || t.api == nil || t.render == nil {
		return nil, fmt.Errorf("markdown target unavailable")
	}
	if replyTo == "" {
		return nil, fmt.Errorf("markdown reply requires a source message id")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return &markdownRenderer{ctx: ctx, api: t.api, render: t.render, replyTo: replyTo, replyInThread: replyInThread}, nil
}

type markdownRenderer struct {
	mu              sync.Mutex
	ctx             context.Context
	api             MarkdownMessageAPI
	render          func(card.Event) string
	replyTo         string
	replyInThread   bool
	messageID       string
	previewDisabled bool
}

func (r *markdownRenderer) Render(event card.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	terminal := !event.Streaming
	if r.previewDisabled && !terminal {
		return nil
	}
	content, err := buildMarkdownPostContent(r.render(event))
	if err != nil {
		return err
	}
	if r.messageID == "" {
		err = r.reply(content, event.SessionID)
		if err != nil && !terminal {
			r.previewDisabled = true
			return nil
		}
		return err
	}
	err = r.patch(content)
	if err != nil && !terminal {
		r.previewDisabled = true
	}
	return err
}

func (r *markdownRenderer) reply(content, sessionID string) error {
	body := larkim.NewReplyMessageReqBodyBuilder().
		MsgType("post").
		Content(content).
		ReplyInThread(r.replyInThread).
		Uuid(stableUUID("markdown-reply", r.replyTo, sessionID)).
		Build()
	req := larkim.NewReplyMessageReqBuilder().MessageId(r.replyTo).Body(body).Build()
	req.Body = body
	resp, err := r.api.Reply(r.ctx, req)
	if err != nil {
		return fmt.Errorf("reply feishu markdown: %w", err)
	}
	if resp == nil {
		return fmt.Errorf("reply feishu markdown failed: empty response")
	}
	if !resp.Success() {
		return fmt.Errorf("reply feishu markdown failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil || *resp.Data.MessageId == "" {
		return fmt.Errorf("reply feishu markdown returned empty message id")
	}
	r.messageID = *resp.Data.MessageId
	r.previewDisabled = false
	return nil
}

func (r *markdownRenderer) patch(content string) error {
	body := larkim.NewPatchMessageReqBodyBuilder().Content(content).Build()
	req := larkim.NewPatchMessageReqBuilder().MessageId(r.messageID).Body(body).Build()
	req.Body = body
	resp, err := r.api.Patch(r.ctx, req)
	if err != nil {
		return fmt.Errorf("patch feishu markdown: %w", err)
	}
	if resp == nil {
		return fmt.Errorf("patch feishu markdown failed: empty response")
	}
	if !resp.Success() {
		return fmt.Errorf("patch feishu markdown failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func buildMarkdownPostContent(markdown string) (string, error) {
	payload := map[string]any{
		"zh_cn": map[string]any{
			"content": [][]map[string]string{{{
				"tag":  "md",
				"text": markdown,
			}}},
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal markdown post: %w", err)
	}
	return string(data), nil
}

var _ card.Renderer = (*markdownRenderer)(nil)
