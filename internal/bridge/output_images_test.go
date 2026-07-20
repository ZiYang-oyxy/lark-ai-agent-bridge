package bridge

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/feishu"
	"lark-agent-bridge/internal/session"
)

type outputTrace struct {
	entries []string
}

type outputTraceRenderer struct {
	trace  *outputTrace
	events []card.Event
	failAt int
}

func (r *outputTraceRenderer) Render(event card.Event) error {
	r.events = append(r.events, event)
	text := ""
	if len(event.Segments) > 0 {
		text = event.Segments[0].Text
	}
	r.trace.entries = append(r.trace.entries, "render:"+text)
	if r.failAt > 0 && len(r.events) == r.failAt {
		return errors.New("render failed")
	}
	return nil
}

type outputFakeImageSender struct {
	trace       *outputTrace
	uploads     int
	replies     []feishu.ImageReply
	uploadErrAt int
	replyErrAt  int
}

func (s *outputFakeImageSender) UploadImage(_ context.Context, reader io.Reader) (string, error) {
	s.uploads++
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	s.trace.entries = append(s.trace.entries, fmt.Sprintf("upload:%d:%d", s.uploads, len(data)))
	if s.uploads == s.uploadErrAt {
		return "", errors.New("upload included /private/secret.png img_secret")
	}
	return fmt.Sprintf("img_%d_secret", s.uploads), nil
}

func (s *outputFakeImageSender) ReplyImage(_ context.Context, reply feishu.ImageReply) (feishu.SendResult, error) {
	s.replies = append(s.replies, reply)
	s.trace.entries = append(s.trace.entries, fmt.Sprintf("reply:%d:%t", len(s.replies), reply.ReplyInThread))
	if len(s.replies) == s.replyErrAt {
		return feishu.SendResult{}, errors.New("reply included img_secret")
	}
	return feishu.SendResult{MessageID: fmt.Sprintf("om_image_%d", len(s.replies))}, nil
}

func TestFinishStreamWithOutputImagesContinuesAfterPartialFailure(t *testing.T) {
	work := t.TempDir()
	for i := 1; i <= 3; i++ {
		writeOutputPNG(t, filepath.Join(work, fmt.Sprintf("image-%d.png", i)), byte(i))
	}
	trace := &outputTrace{}
	renderer := &outputTraceRenderer{trace: trace}
	sender := &outputFakeImageSender{trace: trace, uploadErrAt: 2}
	recorder := audit.NewRecorder()
	svc := NewService(testConfig(t), renderer, newFakeRunner(), recorder)
	svc.OutputImages = sender
	sess, batch, stream := newOutputImageTestRun(t, svc, renderer, work, config.ConversationModeTopic)
	result := AgentRunResult{Segments: []card.Segment{{Kind: card.SegmentText, Text: "![一](image-1.png) ![二](image-2.png) ![三](image-3.png)"}}}

	svc.finishStreamWithOutputImages(context.Background(), stream, "completed", card.Meta{}, result, sess, batch)

	if len(renderer.events) != 2 {
		t.Fatalf("render events=%d", len(renderer.events))
	}
	if got := renderer.events[0].Segments[0].Text; !strings.Contains(got, "一（正在发送）") || !strings.Contains(got, "三（正在发送）") {
		t.Fatalf("pending=%q", got)
	}
	final := renderer.events[1].Segments[0].Text
	for _, want := range []string{"一（已作为图片发送）", "二（图片发送失败：上传失败）", "三（已作为图片发送）"} {
		if !strings.Contains(final, want) {
			t.Fatalf("final=%q missing %q", final, want)
		}
	}
	if sender.uploads != 3 || len(sender.replies) != 2 {
		t.Fatalf("uploads=%d replies=%d", sender.uploads, len(sender.replies))
	}
	for _, reply := range sender.replies {
		if !reply.ReplyInThread || reply.ReplyToMessageID != "source-message" || reply.UUID == "" {
			t.Fatalf("reply=%#v", reply)
		}
	}
	if detail := auditDetailFor(recorder.Events(), "output_image_replied"); !strings.Contains(detail, "conversation_mode=topic") {
		t.Fatalf("reply audit detail=%q", detail)
	}
	if got := trace.entries; !strings.HasPrefix(got[0], "render:") || !strings.HasPrefix(got[len(got)-1], "render:") {
		t.Fatalf("trace=%v", got)
	}
	assertOutputAuditSafe(t, recorder.Events(), work, "img_2_secret")
}

func TestFinishStreamWithOutputImagesDeduplicatesAndUsesChatReply(t *testing.T) {
	work := t.TempDir()
	writeOutputPNG(t, filepath.Join(work, "one.png"), 1)
	writeOutputPNG(t, filepath.Join(work, "copy.png"), 1)
	trace := &outputTrace{}
	renderer := &outputTraceRenderer{trace: trace}
	sender := &outputFakeImageSender{trace: trace}
	svc := NewService(testConfig(t), renderer, newFakeRunner(), audit.NewRecorder())
	svc.OutputImages = sender
	sess, batch, stream := newOutputImageTestRun(t, svc, renderer, work, config.ConversationModeChat)

	svc.finishStreamWithOutputImages(context.Background(), stream, "completed", card.Meta{}, AgentRunResult{
		Segments: []card.Segment{{Kind: card.SegmentText, Text: "![one](one.png) ![copy](copy.png)"}},
	}, sess, batch)

	if sender.uploads != 1 || len(sender.replies) != 1 {
		t.Fatalf("uploads=%d replies=%d", sender.uploads, len(sender.replies))
	}
	if sender.replies[0].ReplyInThread {
		t.Fatal("chat image unexpectedly replied in thread")
	}
	final := renderer.events[1].Segments[0].Text
	if strings.Count(final, "已作为图片发送") != 2 {
		t.Fatalf("final=%q", final)
	}
}

func TestFinishStreamWithOutputImagesSkipsNonCompletedAndPlainText(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		text   string
	}{
		{name: "failed", status: "failed", text: "![x](one.png)"},
		{name: "stopped", status: "stopped", text: "![x](one.png)"},
		{name: "plain", status: "completed", text: "plain text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			work := t.TempDir()
			writeOutputPNG(t, filepath.Join(work, "one.png"), 1)
			trace := &outputTrace{}
			renderer := &outputTraceRenderer{trace: trace}
			sender := &outputFakeImageSender{trace: trace}
			svc := NewService(testConfig(t), renderer, newFakeRunner(), audit.NewRecorder())
			svc.OutputImages = sender
			sess, batch, stream := newOutputImageTestRun(t, svc, renderer, work, config.ConversationModeChat)
			svc.finishStreamWithOutputImages(context.Background(), stream, tc.status, card.Meta{}, AgentRunResult{
				Segments: []card.Segment{{Kind: card.SegmentText, Text: tc.text}},
			}, sess, batch)
			if sender.uploads != 0 || len(renderer.events) != 1 {
				t.Fatalf("uploads=%d renders=%d", sender.uploads, len(renderer.events))
			}
		})
	}
}

func TestFinishStreamWithOutputImagesDoesNotSendAfterPendingRenderFailure(t *testing.T) {
	work := t.TempDir()
	writeOutputPNG(t, filepath.Join(work, "one.png"), 1)
	trace := &outputTrace{}
	renderer := &outputTraceRenderer{trace: trace, failAt: 1}
	sender := &outputFakeImageSender{trace: trace}
	recorder := audit.NewRecorder()
	svc := NewService(testConfig(t), renderer, newFakeRunner(), recorder)
	svc.OutputImages = sender
	sess, batch, stream := newOutputImageTestRun(t, svc, renderer, work, config.ConversationModeChat)

	svc.finishStreamWithOutputImages(context.Background(), stream, "completed", card.Meta{}, AgentRunResult{
		Segments: []card.Segment{{Kind: card.SegmentText, Text: "![one](one.png)"}},
	}, sess, batch)
	if sender.uploads != 0 {
		t.Fatalf("uploads=%d", sender.uploads)
	}
	if !hasAuditAction(recorder.Events(), "terminal_card_render_failed") {
		t.Fatalf("audit=%#v", recorder.Events())
	}
}

func TestExecuteBatchRoutesCompletedResultThroughOutputImages(t *testing.T) {
	work := t.TempDir()
	writeOutputPNG(t, filepath.Join(work, "result.png"), 1)
	trace := &outputTrace{}
	renderer := &outputTraceRenderer{trace: trace}
	sender := &outputFakeImageSender{trace: trace}
	runner := newFakeRunner()
	runner.results = []AgentRunResult{{Segments: []card.Segment{{Kind: card.SegmentText, Text: "![result](result.png)"}}}}
	cfg := testConfig(t)
	cfg.DefaultWorkDir = work
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	svc.OutputImages = sender
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "source", ChatID: "chat", Sender: "user", Text: "/new create image", Time: now}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: "claude", ChatID: "chat"})
	if sender.uploads != 1 || len(sender.replies) != 1 {
		t.Fatalf("uploads=%d replies=%d trace=%v", sender.uploads, len(sender.replies), trace.entries)
	}
}

func newOutputImageTestRun(t *testing.T, svc *Service, renderer card.Renderer, work string, mode config.ConversationMode) (session.Session, session.Batch, *agentCardStream) {
	t.Helper()
	now := time.Unix(100, 0)
	sess := session.Session{ID: "claude:chat", WorkDir: work}
	input := session.Input{
		ID: "source-message", ReplyToMessageID: "source-message", ConversationMode: mode,
		ReplyMode: config.ReplyModeAppend, Time: now,
	}
	batch := session.Batch{ID: "batch", Inputs: []session.Input{input}}
	stream := newAgentCardStreamWithClock(svc, "run:source-message", sess, input, renderer, nil, &fakeStreamClock{now: now})
	return sess, batch, stream
}

func assertOutputAuditSafe(t *testing.T, events []audit.Event, forbidden ...string) {
	t.Helper()
	joined := fmt.Sprint(events)
	for _, value := range forbidden {
		if strings.Contains(joined, value) {
			t.Fatalf("audit leaked %q: %s", value, joined)
		}
	}
}

func hasAuditAction(events []audit.Event, action string) bool {
	for _, event := range events {
		if event.Action == action {
			return true
		}
	}
	return false
}

func auditDetailFor(events []audit.Event, action string) string {
	for _, event := range events {
		if event.Action == action {
			return event.Detail
		}
	}
	return ""
}

func writeOutputPNG(t *testing.T, path string, suffix byte) {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, suffix)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
