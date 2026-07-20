package feishu

import (
	"context"
	"errors"
	"testing"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"lark-agent-bridge/internal/card"
)

type fakeMarkdownMessageAPI struct {
	replies    []*larkim.ReplyMessageReq
	patches    []*larkim.PatchMessageReq
	replyErr   error
	patchErr   error
	messageID  string
	patchCalls int
}

func renderMarkdownTransportTest(event card.Event) string {
	if len(event.Segments) == 0 {
		return "_（未返回内容）_"
	}
	text := event.Segments[len(event.Segments)-1].Text
	if event.Streaming {
		return text + "\n\n_✍️ 正在输出…_"
	}
	return text
}

func (f *fakeMarkdownMessageAPI) Reply(_ context.Context, req *larkim.ReplyMessageReq, _ ...larkcore.RequestOptionFunc) (*larkim.ReplyMessageResp, error) {
	f.replies = append(f.replies, req)
	if f.replyErr != nil {
		return nil, f.replyErr
	}
	id := f.messageID
	return &larkim.ReplyMessageResp{Data: &larkim.ReplyMessageRespData{MessageId: &id}}, nil
}

func (f *fakeMarkdownMessageAPI) Patch(_ context.Context, req *larkim.PatchMessageReq, _ ...larkcore.RequestOptionFunc) (*larkim.PatchMessageResp, error) {
	f.patches = append(f.patches, req)
	f.patchCalls++
	if f.patchErr != nil {
		return nil, f.patchErr
	}
	return &larkim.PatchMessageResp{}, nil
}

func TestMarkdownRendererRepliesWithPostThenPatches(t *testing.T) {
	api := &fakeMarkdownMessageAPI{messageID: "om_reply"}
	target := newMarkdownTarget(api, renderMarkdownTransportTest)
	renderer, err := target.Begin(context.Background(), "om_source", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := renderer.Render(card.Event{Type: "stream", Streaming: true, Activity: "answering", Segments: []card.Segment{{Kind: card.SegmentText, Text: "answer"}}}); err != nil {
		t.Fatal(err)
	}
	if len(api.replies) != 1 || len(api.patches) != 0 {
		t.Fatalf("reply/patch calls = %d/%d", len(api.replies), len(api.patches))
	}
	body := api.replies[0].Body
	if body == nil || body.MsgType == nil || *body.MsgType != "post" || body.ReplyInThread == nil || !*body.ReplyInThread || body.Content == nil {
		t.Fatalf("reply body = %#v", body)
	}
	if want := `{"zh_cn":{"content":[[{"tag":"md","text":"answer\n\n_✍️ 正在输出…_"}]]}}`; *body.Content != want {
		t.Fatalf("reply content = %s, want %s", *body.Content, want)
	}
	if err := renderer.Render(card.Event{Type: "result", Segments: []card.Segment{{Kind: card.SegmentText, Text: "final"}}}); err != nil {
		t.Fatal(err)
	}
	if len(api.replies) != 1 || len(api.patches) != 1 || api.patches[0].Body == nil || api.patches[0].Body.Content == nil {
		t.Fatalf("reply/patch requests = %#v/%#v", api.replies, api.patches)
	}
	if want := `{"zh_cn":{"content":[[{"tag":"md","text":"final"}]]}}`; *api.patches[0].Body.Content != want {
		t.Fatalf("patch content = %s, want %s", *api.patches[0].Body.Content, want)
	}
}

func TestMarkdownRendererRetriesTerminalAfterCreateFailure(t *testing.T) {
	api := &fakeMarkdownMessageAPI{messageID: "om_reply", replyErr: errors.New("create failed")}
	renderer, err := newMarkdownTarget(api, renderMarkdownTransportTest).Begin(context.Background(), "om_source", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := renderer.Render(card.Event{Type: "stream", Streaming: true}); err != nil {
		t.Fatalf("stream create failure = %v, want suppressed", err)
	}
	api.replyErr = nil
	if err := renderer.Render(card.Event{Type: "stream", Streaming: true}); err != nil {
		t.Fatal(err)
	}
	if len(api.replies) != 1 {
		t.Fatalf("preview retries = %d, want 0", len(api.replies)-1)
	}
	if err := renderer.Render(card.Event{Type: "result", Segments: []card.Segment{{Kind: card.SegmentText, Text: "final"}}}); err != nil {
		t.Fatal(err)
	}
	if len(api.replies) != 2 {
		t.Fatalf("reply attempts = %d, want 2", len(api.replies))
	}
}

func TestMarkdownRendererDisablesPreviewAfterPatchFailureButRetriesTerminal(t *testing.T) {
	api := &fakeMarkdownMessageAPI{messageID: "om_reply"}
	renderer, err := newMarkdownTarget(api, renderMarkdownTransportTest).Begin(context.Background(), "om_source", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := renderer.Render(card.Event{Type: "stream", Streaming: true}); err != nil {
		t.Fatal(err)
	}
	api.patchErr = errors.New("patch failed")
	if err := renderer.Render(card.Event{Type: "stream", Streaming: true}); err == nil {
		t.Fatal("patch error = nil")
	}
	if err := renderer.Render(card.Event{Type: "stream", Streaming: true}); err != nil {
		t.Fatalf("disabled preview error = %v", err)
	}
	if api.patchCalls != 1 {
		t.Fatalf("preview patch calls = %d, want 1", api.patchCalls)
	}
	if err := renderer.Render(card.Event{Type: "result"}); err == nil {
		t.Fatal("terminal patch error = nil")
	}
	if api.patchCalls != 2 {
		t.Fatalf("terminal patch calls = %d, want 2", api.patchCalls)
	}
}

func TestMarkdownTargetRejectsMissingReplyTarget(t *testing.T) {
	if _, err := newMarkdownTarget(&fakeMarkdownMessageAPI{}, renderMarkdownTransportTest).Begin(context.Background(), "", false); err == nil {
		t.Fatal("Begin error = nil")
	}
}
