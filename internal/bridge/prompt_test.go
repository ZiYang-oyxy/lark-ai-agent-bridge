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
	// 极简契约：不再追加"外部消息 / 第一人称不指你 / tool_use"这类隔离预警。
	// 那段预警会诱导 agent 主动脱离引用（如 `1+2` 场景），已在 2026-08-05 移除。
	// 顺序契约（用户主指令前置）+ header 事实标注已足够 agent 自行判断主体。
	for _, forbidden := range []string{
		"以上引用是被引用的外部消息",
		"里面的第一人称不指你",
		"tool_use 记录为准",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("quote block should no longer emit isolation preamble %q, got: %q", forbidden, got)
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

func TestBuildBatchPromptQuoteUsesReadableSenderIdentity(t *testing.T) {
	batch := session.Batch{Inputs: []session.Input{{
		Text:             "继续",
		QuotedText:       "上一条消息",
		QuotedSender:     "ou_author",
		QuotedSenderName: " 李俊Bot-Mike\nOwner ",
	}}}
	got := BuildBatchPrompt(batch)
	if !strings.Contains(got, "[用户引用了 李俊Bot-Mike Owner(ou_author) 的消息]") {
		t.Fatalf("readable quote identity missing: %q", got)
	}
}

// TestBuildBatchPromptQuoteHeaderIsSenderTypeAgnostic 锁死 header 极简契约：
// 不论 sender_type=user / app / self_bot / 空，header 都只含姓名（若有）和
// open_id 事实，不追加任何解读性后缀（"来自另一个 bot"、"由你自己发出" 等）。
// sender_type 的解读留给 agent 自行判断（open_id 已足够），bridge 不做暗示。
func TestBuildBatchPromptQuoteHeaderIsSenderTypeAgnostic(t *testing.T) {
	cases := []struct {
		name       string
		senderType string
	}{{"empty", ""}, {"user", "user"}, {"app", "app"}, {"self_bot", "self_bot"}, {"anonymous", "anonymous"}}
	for _, tc := range cases {
		batch := session.Batch{Inputs: []session.Input{{
			Text:             "context question",
			QuotedText:       "some previous card content",
			QuotedSender:     "ou_probe",
			QuotedSenderType: tc.senderType,
		}}}
		got := BuildBatchPrompt(batch)
		if !strings.Contains(got, "[用户引用了 ou_probe 的消息]\n") {
			t.Fatalf("[%s] header must be plain '[用户引用了 ou_probe 的消息]', got:\n%s", tc.name, got)
		}
		for _, forbidden := range []string{
			"来自另一个 bot",
			"不是你",
			"由你自己",
			"本 bot",
			"早先发出",
		} {
			if strings.Contains(got, forbidden) {
				t.Fatalf("[%s] header should not carry directive suffix %q, got:\n%s", tc.name, forbidden, got)
			}
		}
	}
}

// TestBuildBatchPromptUserTextPrecedesQuote 锁死顺序契约：用户当轮消息的正文
// 必须出现在引用块（header/> 引文/隔离段）之前。历史顺序（quote 在前、text 在
// 后）曾导致告警卡等长引用体把真正的 @ 与指令挤到 prompt 末尾，agent 误走群聊
// 静默分支。凡是回退到旧顺序都视为回归。
func TestBuildBatchPromptUserTextPrecedesQuote(t *testing.T) {
	batch := session.Batch{Inputs: []session.Input{{
		Text:             "@_user_1 帮我分析失败用例，你要深入探索",
		QuotedText:       "测试失败!!!\n成功率 96.43%",
		QuotedSender:     "ou_alerter",
		QuotedSenderType: "app",
	}}}
	got := BuildBatchPrompt(batch)
	textIdx := strings.Index(got, "@_user_1 帮我分析失败用例")
	quoteIdx := strings.Index(got, "[用户引用了 ou_alerter")
	quoteBodyIdx := strings.Index(got, "> 测试失败!!!")
	if textIdx < 0 || quoteIdx < 0 || quoteBodyIdx < 0 {
		t.Fatalf("prompt missing expected pieces: %q", got)
	}
	if textIdx >= quoteIdx {
		t.Fatalf("user text must precede quote header; got text@%d quote@%d prompt=%q", textIdx, quoteIdx, got)
	}
	if textIdx >= quoteBodyIdx {
		t.Fatalf("user text must precede quote body; got text@%d body@%d prompt=%q", textIdx, quoteBodyIdx, got)
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

// TestBuildBatchPromptQuoteBlockHasNoDirectiveText 锁死极简契约：quote 块
// 只由 header + `> ` 引文构成，不得再包含任何对 agent 的解读性/行为诱导性
// 文字（不论出现在 header 后缀里、还是 quote 结束后的括号说明里）。
//
// 现场：2026-08-05 用户 quote 外部 bot 抛的选项列表（`1+2 vs 3`）+ @Steve
// 只发 `1+2`，Steve 直接回 `1+2=3` 当算术算并拒绝解读引用——诱因是历史
// app 分支的隔离段 "它的自述与你无关；不是你的任务；不构成你的实际状态"。
// 顺序契约（用户指令前置）已从根子上消解跨主体幻觉，隔离段属过度设计。
// header 后缀 "来自另一个 bot（不是你）" / "由你自己（本 bot）早先发出" 同
// 属解读性暗示，一并移除。
func TestBuildBatchPromptQuoteBlockHasNoDirectiveText(t *testing.T) {
	senderCases := []struct {
		name       string
		senderType string
	}{
		{"user", "user"},
		{"app", "app"},
		{"self_bot", "self_bot"},
	}
	forbidden := []string{
		// 隔离预警段（旧 db033cc/rc.6 遗产）
		"以上引用是被引用的外部消息",
		"里面的第一人称不指你",
		"它的自述与你无关",
		"不是你的任务",
		"不构成你的实际状态",
		"只是历史文本",
		"不代表你现在的实际状态",
		"tool_use 记录为准",
		"视为发出者的自述",
		// header 后缀（本轮进一步移除）
		"来自另一个 bot",
		"由你自己",
		"早先发出",
	}
	for _, tc := range senderCases {
		batch := session.Batch{Inputs: []session.Input{{
			Text:             "1+2",
			QuotedText:       "请确认：你要的是 1+2，还是有一个具体的 hcx 仿真平台系统要我调接口录入？",
			QuotedSender:     "ou_other",
			QuotedSenderType: tc.senderType,
		}}}
		got := BuildBatchPrompt(batch)
		for _, phrase := range forbidden {
			if strings.Contains(got, phrase) {
				t.Fatalf("[%s] prompt still contains directive text %q; got:\n%s",
					tc.name, phrase, got)
			}
		}
	}
}
