package bridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
)

type participationStub struct {
	participated bool
	markErr      error
	marks        int
	touches      int
}

func (s *participationStub) Has(string, string) bool { return s.participated }
func (s *participationStub) Mark(string, string, time.Time) error {
	s.marks++
	if s.markErr == nil {
		s.participated = true
	}
	return s.markErr
}
func (s *participationStub) Touch(string, string, time.Time) error {
	s.touches++
	return nil
}

func TestServiceAcceptsParticipatedTopicFollowupAndTouchesStore(t *testing.T) {
	cfg := testConfig(t)
	store, _ := testPreferenceStore(t, config.RuntimePreference{Model: "default", Effort: "low", GroupMessageMode: config.GroupMessageModeParticipatedTopics}, cfg.AllowedModels)
	renderer := card.NewFakeRenderer()
	topics := &participationStub{participated: true}
	svc := NewService(cfg, renderer, newFakeRunner(), audit.NewRecorder())
	svc.Preferences = store
	svc.TopicParticipation = topics

	err := svc.HandleMessage(context.Background(), Message{ID: "m1", ChatID: "chat", ThreadID: "thread", Sender: "user", Text: "/help", IsGroup: true, Time: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if topics.touches != 1 || len(renderer.Events()) != 1 || len(renderer.Events()[0].Segments) == 0 || renderer.Events()[0].Segments[0].Text == "" {
		t.Fatalf("touches=%d events=%#v", topics.touches, renderer.Events())
	}
}

func TestServiceMarksAcceptedMentionBeforeCommandHandling(t *testing.T) {
	cfg := testConfig(t)
	topics := &participationStub{}
	svc := NewService(cfg, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.TopicParticipation = topics
	err := svc.HandleMessage(context.Background(), Message{ID: "m1", ChatID: "chat", ThreadID: "thread", Sender: "user", Text: "", IsGroup: true, Mentioned: true, Time: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if topics.marks != 1 || !topics.participated {
		t.Fatalf("topics = %#v", topics)
	}
}

func TestServiceContinuesMentionWhenParticipationWriteFails(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	topics := &participationStub{markErr: errors.New("disk full")}
	recorder := audit.NewRecorder()
	svc := NewService(cfg, renderer, newFakeRunner(), recorder)
	svc.TopicParticipation = topics
	err := svc.HandleMessage(context.Background(), Message{ID: "m1", ChatID: "chat", ThreadID: "thread", Sender: "user", Text: "/help", IsGroup: true, Mentioned: true, Time: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if topics.participated || len(renderer.Events()) != 2 {
		t.Fatalf("topics=%#v events=%#v", topics, renderer.Events())
	}
	if !auditContainsAction(recorder.Events(), "topic_participation_save_failed") {
		t.Fatalf("audit = %#v", recorder.Events())
	}
}
