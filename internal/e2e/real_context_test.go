package e2e

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const (
	testClaudeMarker = "E2E_REAL_CONTEXT_CLAUDE_TESTMARKER"
	testCodexMarker  = "E2E_REAL_CONTEXT_CODEX_TESTMARKER"
)

func TestRealContextScenarioPassesForClaudeAndCodexColoredCards(t *testing.T) {
	driver := newRealContextFake()
	result := runRealContextTest(t, driver)
	if result.Status != "passed" {
		t.Fatalf("result = %#v", result)
	}
	want := []string{
		"/agent-mode claude",
		"/new 不要调用工具，只回复 " + testClaudeMarker,
		"/agent-mode codex",
		"/new 不要调用工具，只回复 " + testCodexMarker,
	}
	if strings.Join(driver.sent, "\n") != strings.Join(want, "\n") {
		t.Fatalf("sent = %#v, want %#v", driver.sent, want)
	}
}

func TestRealContextScenarioRejectsMissingVisibleCardFields(t *testing.T) {
	tests := []struct {
		name      string
		missing   string
		assertion string
	}{
		{"claude marker", "claude_marker", "claude_marker_visible"},
		{"claude agent", "claude_agent", "claude_agent_visible"},
		{"claude context", "claude_context", "claude_context_visible"},
		{"codex marker", "codex_marker", "codex_marker_visible"},
		{"codex agent", "codex_agent", "codex_agent_visible"},
		{"codex context", "codex_context", "codex_context_visible"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := newRealContextFake()
			driver.missing = tt.missing
			result := runRealContextTest(t, driver)
			if result.Status != "failed" || result.Failure == nil || result.Failure.Class != FailureAssertion || result.Failure.Assertion != tt.assertion {
				t.Fatalf("result = %#v, want assertion %s", result, tt.assertion)
			}
		})
	}
}

func TestRealContextScenarioStopsOnlyNonterminalCanary(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			driver := newRealContextFake()
			driver.timeoutSource = agent + "-source"
			result := runRealContextTest(t, driver)
			if result.Status != "failed" || result.Failure == nil || result.Failure.Class != FailureTimeout || result.Failure.Step != "run_"+agent {
				t.Fatalf("result = %#v", result)
			}
			if got := countStrings(driver.sent, "/stop"); got != 1 {
				t.Fatalf("cleanup stop count = %d, want 1; sent=%#v", got, driver.sent)
			}
		})
	}

	driver := newRealContextFake()
	result := runRealContextTest(t, driver)
	if result.Status != "passed" || countStrings(driver.sent, "/stop") != 0 {
		t.Fatalf("successful result/sent = %#v/%#v", result, driver.sent)
	}
}

func runRealContextTest(t *testing.T, driver *realContextFake) Result {
	t.Helper()
	evidence, err := NewEvidence(t.TempDir(), "real-context-test", Deployment{SchemaVersion: 1, Transaction: "/state/deployments/txn.1", CandidatePID: 42, SourceCommit: "abc", BinarySHA256: "sha256:abc", Workspace: "/workspace", StateDir: "/state", FixtureDir: "/state/fixtures"}, "real_context")
	if err != nil {
		t.Fatal(err)
	}
	rc := &RunContext{
		Deployment: Deployment{CandidatePID: 42},
		Config:     Config{StepTimeoutMS: 1000},
		Evidence:   evidence,
		Drivers:    Drivers{Messenger: driver, Replies: driver, Audit: driver},
		Values: map[string]string{
			"claude_marker": testClaudeMarker,
			"codex_marker":  testCodexMarker,
		},
	}
	return NewRunner().Run(context.Background(), RealContextScenario(), rc)
}

type realContextFake struct {
	sent          []string
	mark          int64
	missing       string
	timeoutSource string
}

func newRealContextFake() *realContextFake { return &realContextFake{} }

func (f *realContextFake) SendText(_ context.Context, body string) (string, *Failure) {
	f.sent = append(f.sent, body)
	switch {
	case body == "/agent-mode claude":
		return "select-claude", nil
	case body == "/agent-mode codex":
		return "select-codex", nil
	case strings.Contains(body, testClaudeMarker):
		return "claude-source", nil
	case strings.Contains(body, testCodexMarker):
		return "codex-source", nil
	case body == "/stop":
		return "stop-source", nil
	default:
		return "unexpected-source", nil
	}
}

func (f *realContextFake) Mark(context.Context) (int64, *Failure) {
	f.mark++
	return f.mark, nil
}

func (f *realContextFake) Wait(_ context.Context, _ int64, match AuditMatch) (AuditEvent, *Failure) {
	if match.Source == f.timeoutSource {
		return AuditEvent{}, &Failure{Class: FailureTimeout, Message: "terminal result timeout"}
	}
	raw, _ := json.Marshal(map[string]string{"Action": match.Action, "SessionID": match.Source, "Detail": match.Detail})
	return AuditEvent{Action: match.Action, SessionID: match.Source, Detail: match.Detail, Raw: raw}, nil
}

func (f *realContextFake) WaitReply(_ context.Context, _ int64, source string) (Reply, *Failure) {
	switch source {
	case "select-claude":
		return Reply{MessageID: source, Raw: []byte(`{"text":"Claude"}`)}, nil
	case "select-codex":
		return Reply{MessageID: source, Raw: []byte(`{"text":"Codex"}`)}, nil
	case "claude-source":
		return Reply{MessageID: source, Raw: []byte(`{"text":"` + f.cardText("claude") + `"}`)}, nil
	case "codex-source":
		return Reply{MessageID: source, Raw: []byte(`{"text":"` + f.cardText("codex") + `"}`)}, nil
	default:
		return Reply{}, &Failure{Class: FailureHarness, Message: "unexpected reply source"}
	}
}

func (f *realContextFake) ContainsVisibleText(reply Reply, expected string) (bool, string, *Failure) {
	actual := string(reply.Raw)
	return strings.Contains(actual, expected), actual, nil
}

func (f *realContextFake) cardText(agent string) string {
	marker := testClaudeMarker
	label := "🍊 a1b2c3"
	if agent == "codex" {
		marker = testCodexMarker
		label = "⚙️ 019283"
	}
	parts := []string{marker, label, "🟢 ctx: 42%"}
	filtered := parts[:0]
	for index, value := range parts {
		key := agent + "_" + []string{"marker", "agent", "context"}[index]
		if f.missing != key {
			filtered = append(filtered, value)
		}
	}
	return strings.Join(filtered, " | ")
}

func countStrings(values []string, want string) int {
	count := 0
	for _, value := range values {
		if value == want {
			count++
		}
	}
	return count
}
