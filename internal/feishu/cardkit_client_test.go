package feishu

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type staticTokenSource struct{}

func (staticTokenSource) Token(context.Context) (string, error) { return "token", nil }

type captureCardKitHTTP struct {
	bodies []map[string]any
}

func (f *captureCardKitHTTP) Do(req *http.Request) (*http.Response, error) {
	var body map[string]any
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return nil, err
	}
	f.bodies = append(f.bodies, body)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"code":0,"msg":"ok","data":{"message_id":"reply"}}`)),
	}, nil
}

func TestCardKitClientForwardsReplyInThread(t *testing.T) {
	for _, want := range []bool{false, true} {
		t.Run(map[bool]string{false: "chat", true: "topic"}[want], func(t *testing.T) {
			httpClient := &captureCardKitHTTP{}
			client := NewCardKitClientWithTokenSource(staticTokenSource{})
			client.http = httpClient
			client.limiter = nil
			if _, err := client.ReplyCard(context.Background(), CardKitReplyRequest{ReplyToMessageID: "source", CardID: "card", UUID: "uuid", ReplyInThread: want}); err != nil {
				t.Fatal(err)
			}
			if len(httpClient.bodies) != 1 || httpClient.bodies[0]["reply_in_thread"] != want {
				t.Fatalf("request bodies = %#v, want reply_in_thread=%t", httpClient.bodies, want)
			}
		})
	}
}
