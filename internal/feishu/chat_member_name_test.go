package feishu

import (
	"context"
	"errors"
	"testing"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type chatMembersNameStub struct {
	responses []*larkim.GetChatMembersResp
	err       error
	calls     int
}

func (s *chatMembersNameStub) Get(context.Context, *larkim.GetChatMembersReq, ...larkcore.RequestOptionFunc) (*larkim.GetChatMembersResp, error) {
	index := s.calls
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	if index >= len(s.responses) {
		return nil, nil
	}
	return s.responses[index], nil
}

func chatMembersNameResponse(items map[string]string, hasMore bool, pageToken string) *larkim.GetChatMembersResp {
	members := make([]*larkim.ListMember, 0, len(items))
	for id, name := range items {
		id, name := id, name
		members = append(members, &larkim.ListMember{MemberId: &id, Name: &name})
	}
	return &larkim.GetChatMembersResp{
		CodeError: larkcore.CodeError{Code: 0},
		Data: &larkim.GetChatMembersRespData{
			Items:     members,
			HasMore:   &hasMore,
			PageToken: &pageToken,
		},
	}
}

func TestChatMemberNameResolverFindsMemberOnLaterPageAndCaches(t *testing.T) {
	api := &chatMembersNameStub{responses: []*larkim.GetChatMembersResp{
		chatMembersNameResponse(map[string]string{"ou_other": "Other"}, true, "page-2"),
		chatMembersNameResponse(map[string]string{"ou_target": "李俊Bot-Mike"}, false, ""),
	}}
	resolver := &ChatMemberNameResolver{api: api, cacheLimit: 8, now: time.Now}
	for range 2 {
		name, err := resolver.ResolveChatMemberName(t.Context(), "oc_chat", "ou_target")
		if err != nil || name != "李俊Bot-Mike" {
			t.Fatalf("name=%q err=%v", name, err)
		}
	}
	if api.calls != 2 {
		t.Fatalf("API calls=%d, want two pages once", api.calls)
	}
}

func TestChatMemberNameResolverNegativeCacheExpires(t *testing.T) {
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	api := &chatMembersNameStub{err: errors.New("denied")}
	resolver := &ChatMemberNameResolver{api: api, cacheLimit: 8, now: func() time.Time { return now }}

	for range 2 {
		if _, err := resolver.ResolveChatMemberName(t.Context(), "oc_chat", "ou_target"); err == nil {
			t.Fatal("expected lookup failure")
		}
	}
	if api.calls != 1 {
		t.Fatalf("negative cache API calls=%d, want 1", api.calls)
	}
	now = now.Add(chatMemberNameFailureTTL)
	if _, err := resolver.ResolveChatMemberName(t.Context(), "oc_chat", "ou_target"); err == nil {
		t.Fatal("expected lookup failure after cache expiry")
	}
	if api.calls != 2 {
		t.Fatalf("expired negative cache API calls=%d, want 2", api.calls)
	}
}

func TestChatMemberNameResolverBoundsCache(t *testing.T) {
	api := &chatMembersNameStub{responses: []*larkim.GetChatMembersResp{
		chatMembersNameResponse(map[string]string{"ou_1": "One"}, false, ""),
		chatMembersNameResponse(map[string]string{"ou_2": "Two"}, false, ""),
		chatMembersNameResponse(map[string]string{"ou_3": "Three"}, false, ""),
	}}
	resolver := &ChatMemberNameResolver{api: api, cacheLimit: 2, now: time.Now}
	for _, id := range []string{"ou_1", "ou_2", "ou_3"} {
		if _, err := resolver.ResolveChatMemberName(t.Context(), "oc_chat", id); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(resolver.cache); got != 2 {
		t.Fatalf("cache entries=%d, want 2", got)
	}
}

func TestChatMemberNameResolverValidatesIdentity(t *testing.T) {
	resolver := &ChatMemberNameResolver{}
	if _, err := resolver.ResolveChatMemberName(t.Context(), "", "ou_target"); err == nil {
		t.Fatal("missing chat ID must fail")
	}
	if _, err := resolver.ResolveChatMemberName(t.Context(), "oc_chat", ""); err == nil {
		t.Fatal("missing open ID must fail")
	}
}
