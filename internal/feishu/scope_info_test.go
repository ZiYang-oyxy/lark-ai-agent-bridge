package feishu

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type scopeInfoHTTP struct {
	status int
	body   string
}

func (f scopeInfoHTTP) Do(*http.Request) (*http.Response, error) {
	status := f.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(f.body))}, nil
}

func TestAccessInfoClientInspectsTenantScope(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		want    ScopeState
		wantErr bool
	}{
		{name: "present", body: `{"code":0,"data":{"app":{"scopes":[{"scope":"im:message:send_as_bot"},{"scope":"im:message.group_msg"}]}}}`, want: ScopePresent},
		{name: "missing", body: `{"code":0,"data":{"app":{"scopes":[{"scope":"im:message:send_as_bot"}]}}}`, want: ScopeMissing},
		{name: "api failure", body: `{"code":999,"msg":"denied"}`, want: ScopeUnknown, wantErr: true},
		{name: "malformed", body: `{`, want: ScopeUnknown, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &AccessInfoClient{BaseURL: "https://open.feishu.test", Client: scopeInfoHTTP{body: tc.body}, Tokens: staticTenantToken("tenant")}
			got, err := client.InspectTenantScope(context.Background(), "cli_app", "im:message.group_msg")
			if got != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("InspectTenantScope = %q, %v", got, err)
			}
		})
	}
}
