package bridge

import (
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
	path := "/absolute/cache/sha-report.txt"
	prompt := BuildBatchPrompt(session.Batch{Inputs: []session.Input{{
		Sender: "alice", Time: time.Date(2026, 7, 18, 9, 10, 11, 0, time.UTC),
		Attachments: []media.Attachment{{Path: path, MIME: "text/plain"}},
	}}})
	if !strings.Contains(prompt, "[text-file] "+path) || strings.Count(prompt, path) != 1 {
		t.Fatalf("attachment-only prompt = %q", prompt)
	}
	if strings.Contains(prompt, "file body must not be inlined") {
		t.Fatalf("prompt inlined file body: %q", prompt)
	}
}
