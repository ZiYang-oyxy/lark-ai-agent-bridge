package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"lark-agent-bridge/internal/card"
)

type rawElementContentProbeStage uint8

const (
	rawElementContentProbePrepareCard rawElementContentProbeStage = iota
	rawElementContentProbeCreateCard
	rawElementContentProbeMarshalRequest
	rawElementContentProbeGetToken
	rawElementContentProbeBuildRequest
	rawElementContentProbeSendRequest
)

func rawElementContentProbeFailureMessage(stage rawElementContentProbeStage) string {
	switch stage {
	case rawElementContentProbePrepareCard:
		return "raw element-content probe failed while preparing the probe card"
	case rawElementContentProbeCreateCard:
		return "raw element-content probe failed while creating the probe card"
	case rawElementContentProbeMarshalRequest:
		return "raw element-content probe failed while encoding the request"
	case rawElementContentProbeGetToken:
		return "raw element-content probe failed while obtaining a tenant token"
	case rawElementContentProbeBuildRequest:
		return "raw element-content probe failed while building the request"
	case rawElementContentProbeSendRequest:
		return "raw element-content probe failed while sending the request"
	default:
		return "raw element-content probe failed at an unknown stage"
	}
}

func rawElementContentProbeFailureMessages() []string {
	stages := []rawElementContentProbeStage{
		rawElementContentProbePrepareCard,
		rawElementContentProbeCreateCard,
		rawElementContentProbeMarshalRequest,
		rawElementContentProbeGetToken,
		rawElementContentProbeBuildRequest,
		rawElementContentProbeSendRequest,
	}
	messages := make([]string, 0, len(stages))
	for _, stage := range stages {
		messages = append(messages, rawElementContentProbeFailureMessage(stage))
	}
	return messages
}

func rawElementContentProbeMetadataFailureMessage(status int) string {
	return fmt.Sprintf("raw element-content probe failed while decoding response metadata: http=%d", status)
}

func TestRawElementContentProbeFailureMessagesExcludeSensitiveData(t *testing.T) {
	sensitive := []string{"card-sensitive-id", "https://sensitive.example", "server message sentinel", "tenant-token-sentinel"}
	messages := append(rawElementContentProbeFailureMessages(), rawElementContentProbeMetadataFailureMessage(599))
	for _, message := range messages {
		for _, value := range sensitive {
			if strings.Contains(message, value) {
				t.Fatalf("probe failure message leaked %q: %q", value, message)
			}
		}
	}
}

func realCardKitSmokeClient(t *testing.T) *CardKitClient {
	t.Helper()
	if os.Getenv("E2E_REAL_CARDKIT") != "1" {
		t.Skip("set E2E_REAL_CARDKIT=1 with LARK_APP_ID/LARK_APP_SECRET to run real CardKit smoke")
	}
	appID := os.Getenv("LARK_APP_ID")
	appSecret := os.Getenv("LARK_APP_SECRET")
	if appID == "" || appSecret == "" {
		t.Skip("LARK_APP_ID and LARK_APP_SECRET are required for real CardKit smoke")
	}
	return NewCardKitClient(appID, appSecret)
}

func realCardKitReplyTarget(t *testing.T) string {
	t.Helper()
	messageID := strings.TrimSpace(os.Getenv("E2E_REAL_CARDKIT_REPLY_TO_MESSAGE_ID"))
	if messageID == "" {
		t.Skip("E2E_REAL_CARDKIT_REPLY_TO_MESSAGE_ID is required for the real CardKit native answer smoke")
	}
	return messageID
}

func TestRealCardKitElementContentRequestShape(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := realCardKitSmokeClient(t)
	prepared, err := card.PrepareLarkCard(card.Event{Type: "stream", Streaming: true, SessionID: "cardkit-element-content-probe"})
	if err != nil {
		t.Fatal(rawElementContentProbeFailureMessage(rawElementContentProbePrepareCard))
	}
	created, err := client.CreateCard(ctx, CardKitCreateRequest{Prepared: &prepared})
	if err != nil {
		t.Fatal(rawElementContentProbeFailureMessage(rawElementContentProbeCreateCard))
	}

	body, err := json.Marshal(map[string]any{"content": "native probe", "sequence": 1, "uuid": "cardkit-element-content-probe-1"})
	if err != nil {
		t.Fatal(rawElementContentProbeFailureMessage(rawElementContentProbeMarshalRequest))
	}
	token, err := client.tokens.Token(ctx)
	if err != nil {
		t.Fatal(rawElementContentProbeFailureMessage(rawElementContentProbeGetToken))
	}
	endpoint := defaultFeishuOpenAPIBaseURL + "/open-apis/cardkit/v1/cards/" + url.PathEscape(created.CardID) + "/elements/answer/content"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(rawElementContentProbeFailureMessage(rawElementContentProbeBuildRequest))
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(rawElementContentProbeFailureMessage(rawElementContentProbeSendRequest))
	}
	defer resp.Body.Close()
	var envelope struct {
		Code int `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&envelope); err != nil {
		t.Fatal(rawElementContentProbeMetadataFailureMessage(resp.StatusCode))
	}

	// 2026-07-18: no endpoint-specific encoded-body ceiling or proven-unapplied
	// response code is frozen. This probe must be run with deliberate credentials,
	// then its non-sensitive status/code/byte evidence must be reviewed before
	// adding production constants or enabling UpdateElementContent.
	t.Fatalf("raw element-content probe observed http=%d code=%d encoded_bytes=%d; evidence is not frozen, native streaming remains disabled", resp.StatusCode, envelope.Code, len(body))
}

func TestRealCardKitNativeAnswerStream(t *testing.T) {
	runRealCardKitNativeAnswerStream(t)
}

func runRealCardKitNativeAnswerStream(t *testing.T) {
	t.Helper()
	client := realCardKitSmokeClient(t)
	replyToMessageID := realCardKitReplyTarget(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	streaming, err := card.PrepareLarkCard(card.Event{
		Type:      "stream",
		Streaming: true,
		SessionID: "cardkit-native-answer-smoke",
	})
	if err != nil {
		t.Fatal("native answer smoke failed while preparing the streaming card")
	}
	created, err := client.CreateCard(ctx, CardKitCreateRequest{Prepared: &streaming})
	if err != nil {
		t.Fatal("native answer smoke failed while creating the streaming card")
	}
	if _, err := client.ReplyCard(ctx, CardKitReplyRequest{
		ReplyToMessageID: replyToMessageID,
		CardID:           created.CardID,
		UUID:             stableUUID("cardkit-native-answer-smoke-reply", replyToMessageID, created.CardID),
	}); err != nil {
		t.Fatal("native answer smoke failed while replying with the streaming card")
	}

	for _, update := range []struct {
		content  string
		sequence int
	}{
		{content: "native-smoke-one", sequence: 1},
		{content: "native-smoke-two", sequence: 2},
	} {
		if err := client.UpdateElementContent(ctx, CardKitUpdateElementContentRequest{
			CardID:    created.CardID,
			ElementID: "answer",
			Content:   update.content,
			Sequence:  update.sequence,
			UUID:      stableUUID("cardkit-native-answer-smoke-answer", created.CardID, fmt.Sprint(update.sequence)),
		}); err != nil {
			t.Fatal("native answer smoke failed while updating answer content")
		}
	}

	terminal, err := card.PrepareLarkCard(card.Event{
		Type:      "result",
		SessionID: "cardkit-native-answer-smoke",
		Segments:  []card.Segment{{Kind: card.SegmentText, Text: "native-smoke-two"}},
	})
	if err != nil {
		t.Fatal("native answer smoke failed while preparing the final card")
	}
	if err := client.UpdateCard(ctx, CardKitUpdateCardRequest{
		CardID:   created.CardID,
		Prepared: &terminal,
		Sequence: 3,
		UUID:     stableUUID("cardkit-native-answer-smoke-final", created.CardID, "3"),
	}); err != nil {
		t.Fatal("native answer smoke failed while sending the final card")
	}
}

func TestRealCardKitCreatesBridgeCard(t *testing.T) {
	client := realCardKitSmokeClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	events := []card.Event{
		{
			Type:      "workdir_confirm",
			SessionID: "claude:real-cardkit-smoke:confirm",
			Segments:  []card.Segment{{Kind: card.SegmentText, Text: "Workdir does not exist: /tmp/lark-agent-bridge-cardkit-smoke"}},
			Actions:   card.WorkDirCreateActions("/tmp/lark-agent-bridge-cardkit-smoke"),
			Meta:      card.Meta{Agent: "claude", Model: "smoke", RunTokens: 1, TotalTokens: 1, User: "smoke-user", IP: "192.0.2.1", WorkDir: "/tmp/lark-agent-bridge-cardkit-smoke", Status: "running"},
		},
		{
			Type:      "workdir_created",
			SessionID: "claude:real-cardkit-smoke:created",
			Segments:  []card.Segment{{Kind: card.SegmentText, Text: "Workdir created: /tmp/lark-agent-bridge-cardkit-smoke"}},
			Actions:   card.WorkDirActions("/tmp/lark-agent-bridge-cardkit-smoke", true),
			Meta:      card.Meta{WorkDir: "/tmp/lark-agent-bridge-cardkit-smoke"},
		},
		{
			Type:      "workdir_cancelled",
			SessionID: "claude:real-cardkit-smoke:cancelled",
			Segments:  []card.Segment{{Kind: card.SegmentText, Text: "Workdir creation cancelled: /tmp/lark-agent-bridge-cardkit-smoke"}},
			Actions:   card.WorkDirActions("/tmp/lark-agent-bridge-cardkit-smoke", true),
			Meta:      card.Meta{WorkDir: "/tmp/lark-agent-bridge-cardkit-smoke"},
		},
	}
	for _, event := range events {
		payload := card.BuildLarkCard(event)
		if _, err := client.CreateCard(ctx, CardKitCreateRequest{Card: payload}); err != nil {
			t.Fatalf("CreateCard %s bridge payload error: %v", event.Type, err)
		}
	}
}
