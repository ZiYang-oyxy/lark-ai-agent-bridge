package bridge

import (
	"os"
	"strings"
	"testing"
	"time"

	"lark-agent-bridge/internal/media"
	"lark-agent-bridge/internal/session"
)

func TestBuildBatchPromptEmpty(t *testing.T) {
	if got := BuildBatchPrompt(session.Batch{}); got != "" {
		t.Fatalf("empty prompt = %q", got)
	}
}

func TestBuildBatchPromptSingleTrimsText(t *testing.T) {
	if got := BuildBatchPrompt(session.Batch{Inputs: []session.Input{{Text: "  hello  "}}}); got != "hello" {
		t.Fatalf("single prompt = %q", got)
	}
}

func TestBuildBatchPromptExactFormatAndOrder(t *testing.T) {
	loc := time.FixedZone("CST", 8*60*60)
	batch := session.Batch{Inputs: []session.Input{
		{Sender: "alice", Text: "  first  ", Time: time.Date(2026, 7, 18, 9, 10, 11, 0, loc)},
		{Sender: "bob", Text: "\nsecond\n", Time: time.Date(2026, 7, 18, 9, 10, 12, 0, loc)},
	}}
	want := "以下是按时间顺序合并的一批用户消息。\n\n[消息 1 | 2026-07-18 09:10:11 | alice]\nfirst\n\n[消息 2 | 2026-07-18 09:10:12 | bob]\nsecond"
	if got := BuildBatchPrompt(batch); got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
	if batch.Inputs[0].Text != "  first  " || batch.Inputs[1].Text != "\nsecond\n" {
		t.Fatal("inputs were modified")
	}
}

func TestBuildBatchPromptSingleWithQuoteRendersQuotedBlock(t *testing.T) {
	batch := session.Batch{Inputs: []session.Input{{
		Text:             "我是一只什么猫",
		QuotedText:       "我是一只黑猫",
		QuotedSender:     "ou_author",
		QuotedSenderType: "user",
	}}}
	got := BuildBatchPrompt(batch)
	// header 保留 open_id（兼容旧断言点），正文行前缀 "> "，用户文本尾随。
	for _, want := range []string{
		"[用户引用了 ou_author 的消息]",
		"> 我是一只黑猫",
		"我是一只什么猫",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt = %q, missing %q", got, want)
		}
	}
	// 主体身份隔离提示是新增契约的核心：必须出现"引用是外部消息"+"第一人称不指你"
	// +"以本轮 tool_use 记录为准"三个要点。丢任何一个都视为回归。
	for _, must := range []string{
		"以上引用是被引用的外部消息",
		"里面的第一人称不指你",
		"tool_use",
	} {
		if !strings.Contains(got, must) {
			t.Fatalf("quote must carry subject-isolation hint %q, got: %q", must, got)
		}
	}
}

func TestBuildBatchPromptQuoteWithoutSenderUsesGenericLabel(t *testing.T) {
	batch := session.Batch{Inputs: []session.Input{{
		Text:       "and this",
		QuotedText: "first line\nsecond line",
	}}}
	got := BuildBatchPrompt(batch)
	if !strings.Contains(got, "[用户引用了一条消息]") {
		t.Fatalf("missing generic quote label: %q", got)
	}
	if !strings.Contains(got, "> first line\n> second line") {
		t.Fatalf("multiline quote not prefixed per line: %q", got)
	}
	if !strings.Contains(got, "and this") {
		t.Fatalf("user text dropped: %q", got)
	}
}

// TestBuildBatchPromptQuoteFromOtherBotAddsAppFrame 引用消息来自别的 bot（app）时，
// header 必须显式标注"来自另一个 bot（不是你）"，隔离段必须强调"这是另一个 bot
// 的消息，它的自述与你无关"——防止 quote 里的第一人称"我正在跑子代理"被 agent
// 误当成自己的实际状态。
func TestBuildBatchPromptQuoteFromOtherBotAddsAppFrame(t *testing.T) {
	batch := session.Batch{Inputs: []session.Input{{
		Text:             "你看下他起子代理，后台现在真的在跑吗",
		QuotedText:       "review subagent 已重新启动，等它完成通知，中途不打断",
		QuotedSender:     "ou_other_bot",
		QuotedSenderType: "app",
	}}}
	got := BuildBatchPrompt(batch)
	for _, must := range []string{
		"来自另一个 bot",
		"另一个 bot 的消息",
		"它的自述与你无关",
	} {
		if !strings.Contains(got, must) {
			t.Fatalf("app-sender quote must add stricter isolation hint %q, got: %q", must, got)
		}
	}
	// user 版通用提示不应出现在 app 分支
	if strings.Contains(got, "视为发出者的自述") {
		t.Fatalf("app-sender branch must not fall back to generic user-branch hint: %q", got)
	}
}

// TestBuildBatchPromptQuoteFromSelfBotMarksSelfHistory 引用消息发送者就是本 bot
// 自己：header 必须显式说"由你自己（本 bot）早先发出"，隔离段必须强调这只是
// 历史文本、不代表当前实际状态——避免 agent 拿一段自己早先发过的进度快照当作
// 现在还成立的运行状态。
func TestBuildBatchPromptQuoteFromSelfBotMarksSelfHistory(t *testing.T) {
	batch := session.Batch{Inputs: []session.Input{{
		Text:             "现在还在跑吗？",
		QuotedText:       "review subagent 已重新启动",
		QuotedSender:     "ou_self",
		QuotedSenderType: "self_bot",
	}}}
	got := BuildBatchPrompt(batch)
	for _, must := range []string{
		"由你自己（本 bot）早先发出",
		"只是历史文本",
		"不代表你现在的实际状态",
	} {
		if !strings.Contains(got, must) {
			t.Fatalf("self_bot quote must add self-history isolation hint %q, got: %q", must, got)
		}
	}
}

func TestBuildBatchPromptSinglePlainStaysLegacyWhenNoQuote(t *testing.T) {
	// Regression guard: a plain single message with no quote/attachment must
	// keep the bare fast-path form (no header, no quote block).
	if got := BuildBatchPrompt(session.Batch{Inputs: []session.Input{{Text: "hi"}}}); got != "hi" {
		t.Fatalf("plain single prompt = %q", got)
	}
}

func TestBuildBatchPromptIncludesTypedAttachmentPathsExactlyOnce(t *testing.T) {
	imagePath := "/absolute/cache/sha-image.png"
	textPath := "/absolute/cache/sha-notes.md"
	batch := session.Batch{Inputs: []session.Input{
		{
			Sender: "alice", Time: time.Date(2026, 7, 18, 9, 10, 11, 0, time.UTC), Text: "inspect these",
			Attachments: []media.Attachment{{Path: imagePath, MIME: "image/png"}, {Path: textPath, MIME: "text/markdown"}},
		},
	}}

	prompt := BuildBatchPrompt(batch)
	for _, want := range []string{
		"alice", "2026-07-18 09:10:11", "inspect these",
		"[image] " + imagePath,
		"[text-file] " + textPath,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt = %q, missing %q", prompt, want)
		}
	}
	if strings.Count(prompt, imagePath) != 1 || strings.Count(prompt, textPath) != 1 {
		t.Fatalf("attachment paths must appear exactly once: %q", prompt)
	}
}

func TestBuildBatchPromptAttachmentOnlyDoesNotInlineFileContent(t *testing.T) {
	sentinel := "ATTACHMENT_BODY_MUST_NOT_APPEAR"
	path := t.TempDir() + "/sha-report.txt"
	if err := os.WriteFile(path, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	prompt := BuildBatchPrompt(session.Batch{Inputs: []session.Input{{
		Sender: "alice", Time: time.Date(2026, 7, 18, 9, 10, 11, 0, time.UTC),
		Attachments: []media.Attachment{{Path: path, MIME: "text/plain"}},
	}}})
	if !strings.Contains(prompt, "[text-file] "+path) || strings.Count(prompt, path) != 1 {
		t.Fatalf("attachment-only prompt = %q", prompt)
	}
	if strings.Contains(prompt, sentinel) {
		t.Fatalf("prompt inlined file body: %q", prompt)
	}
}
