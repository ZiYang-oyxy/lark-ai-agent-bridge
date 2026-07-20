package feishu

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAccessInfoClientFetchesOwnerAndPagedChats(t *testing.T) {
	fake := &fakeAccessInfoHTTP{}
	client := &AccessInfoClient{BaseURL: "https://open.feishu.test", Client: fake, Tokens: staticTenantToken("tenant")}
	owner, err := client.GetOwner(context.Background(), "cli_app")
	if err != nil || owner != "ou_owner" {
		t.Fatalf("owner=%q err=%v", owner, err)
	}
	chats, err := client.ListChats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 2 || chats[0].ID != "oc_1" || chats[1].Name != "Group 2" {
		t.Fatalf("chats = %#v", chats)
	}
	if fake.chatCalls != 2 {
		t.Fatalf("chat calls = %d, want 2", fake.chatCalls)
	}
}

type fakeAccessInfoHTTP struct{ chatCalls int }

func (f *fakeAccessInfoHTTP) Do(req *http.Request) (*http.Response, error) {
	body := `{"code":0,"data":{"app":{"owner":{"owner_id":"ou_owner"}}}}`
	if strings.HasSuffix(req.URL.Path, "/im/v1/chats") {
		f.chatCalls++
		if req.URL.Query().Get("page_token") == "next" {
			body = `{"code":0,"data":{"items":[{"chat_id":"oc_2","name":"Group 2"}],"has_more":false}}`
		} else {
			body = `{"code":0,"data":{"items":[{"chat_id":"oc_1","name":"Group 1"}],"has_more":true,"page_token":"next"}}`
		}
	}
	if req.Header.Get("Authorization") != "Bearer tenant" {
		body = `{"code":999,"msg":"missing token"}`
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}
