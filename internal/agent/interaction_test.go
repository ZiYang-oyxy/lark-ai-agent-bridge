package agent

import "testing"

func TestDetectAuthorization(t *testing.T) {
	got := DetectInteraction("Tool permission required\nAllow once\nReject")
	if got.Kind != InteractionAuthorization {
		t.Fatalf("kind = %s, want authorization", got.Kind)
	}
	if len(got.Options) != 4 {
		t.Fatalf("options = %#v", got.Options)
	}
}

func TestDetectCodexYoloBannerIsNotAuthorization(t *testing.T) {
	got := DetectInteraction(`╭─────────────────────────────────────────────────╮
│ >_ OpenAI Codex (v0.144.3)                      │
│ model:       gpt-5.5 xhigh   /model to change   │
│ directory:   /private/tmp/lark-agent-bridge-e2e │
│ permissions: YOLO mode                          │
╰─────────────────────────────────────────────────╯

›`)
	if got.Kind != InteractionNone {
		t.Fatalf("kind = %s, want none", got.Kind)
	}
}

func TestDetectChoice(t *testing.T) {
	got := DetectInteraction("请选择:\n1. repo top3\n2. AI only")
	if got.Kind != InteractionChoice {
		t.Fatalf("kind = %s, want choice", got.Kind)
	}
	if len(got.Options) != 2 {
		t.Fatalf("options = %#v", got.Options)
	}
}

func TestDetectClaudeTrustFolderChoice(t *testing.T) {
	got := DetectInteraction(`Quick safety check: Is this a project you created or one you trust?

 ❯ 1. Yes, I trust this folder
   2. No, exit`)
	if got.Kind != InteractionChoice {
		t.Fatalf("kind = %s, want choice", got.Kind)
	}
	if len(got.Options) != 2 || got.Options[0] != "Yes, I trust this folder" {
		t.Fatalf("options = %#v", got.Options)
	}
}

func TestDetectCodexTrustDirectoryChoice(t *testing.T) {
	got := DetectInteraction(`Do you trust the contents of this directory?

› 1. Yes, continue
  2. No, quit`)
	if got.Kind != InteractionChoice {
		t.Fatalf("kind = %s, want choice", got.Kind)
	}
	if len(got.Options) != 2 || got.Options[0] != "Yes, continue" {
		t.Fatalf("options = %#v", got.Options)
	}
}

func TestDetectCodexUpdateChoice(t *testing.T) {
	got := DetectInteraction(`✨ Update available! 0.144.3 -> 0.144.5

› 1. Update now (runs brew upgrade --cask codex)
  2. Skip
  3. Skip until next version

Press enter to continue`)
	if got.Kind != InteractionChoice {
		t.Fatalf("kind = %s, want choice", got.Kind)
	}
	if len(got.Options) != 3 || got.Options[1] != "Skip" {
		t.Fatalf("options = %#v", got.Options)
	}
}

func TestDetectResumeCandidatesWithSpaceSeparatedList(t *testing.T) {
	got := DetectInteraction("resume session\n1 34ccac3d 0s ago query\n2 def456 1m ago inspect")
	if got.Kind != InteractionResume {
		t.Fatalf("kind = %s, want resume", got.Kind)
	}
	if len(got.Options) != 2 {
		t.Fatalf("options = %#v, want 2 candidates", got.Options)
	}
	if got.Options[0] != "34ccac3d 0s ago query" {
		t.Fatalf("first option = %q", got.Options[0])
	}
}

func TestDetectResumeRequiresCandidates(t *testing.T) {
	got := DetectInteraction("I will resume the previous session context and continue the task.")
	if got.Kind != InteractionNone {
		t.Fatalf("kind = %s, want none", got.Kind)
	}
}

func TestDetectReady(t *testing.T) {
	if !DetectReady("done\n>") {
		t.Fatal("expected prompt marker to be ready")
	}
	if !DetectReady("gpt-5.5 xhigh · /tmp/work · Context 0% used\n› Implement {feature}\n\ngpt-5.5 xhigh · /tmp/work · Context 0% used") {
		t.Fatal("expected Codex TUI prompt marker to be ready")
	}
	if !DetectReady("Ready for input") {
		t.Fatal("expected ready marker")
	}
	if DetectReady("Tool permission required\nAllow once") {
		t.Fatal("authorization prompt must not be treated as ready")
	}
	if DetectReady("Do you trust the contents of this directory?\n› 1. Yes, continue\n2. No, quit") {
		t.Fatal("Codex trust prompt must not be treated as ready")
	}
}
