package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type KnownChat struct {
	ID   string
	Name string
}

type ScopeState string

const (
	ScopePresent ScopeState = "present"
	ScopeMissing ScopeState = "missing"
	ScopeUnknown ScopeState = "unknown"
)

type AccessInfoClient struct {
	BaseURL string
	Client  HTTPDoer
	Tokens  TenantTokenSource
}

func (c *AccessInfoClient) GetOwner(ctx context.Context, appID string) (string, error) {
	if c == nil || c.Tokens == nil || appID == "" {
		return "", fmt.Errorf("feishu access info client unavailable")
	}
	endpoint := c.baseURL() + "/open-apis/application/v6/applications/" + url.PathEscape(appID) + "?lang=zh_cn&user_id_type=open_id"
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			App struct {
				Owner struct {
					OwnerID string `json:"owner_id"`
				} `json:"owner"`
			} `json:"app"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, endpoint, &out); err != nil {
		return "", err
	}
	if out.Code != 0 || out.Data.App.Owner.OwnerID == "" {
		return "", &FeishuAPIError{Code: out.Code, Message: out.Msg}
	}
	return out.Data.App.Owner.OwnerID, nil
}

func (c *AccessInfoClient) InspectTenantScope(ctx context.Context, appID, scope string) (ScopeState, error) {
	if c == nil || c.Tokens == nil || strings.TrimSpace(appID) == "" || strings.TrimSpace(scope) == "" {
		return ScopeUnknown, fmt.Errorf("feishu access info client unavailable")
	}
	endpoint := c.baseURL() + "/open-apis/application/v6/applications/" + url.PathEscape(appID) + "?lang=zh_cn&user_id_type=open_id"
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			App struct {
				Scopes []struct {
					Scope string `json:"scope"`
				} `json:"scopes"`
			} `json:"app"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, endpoint, &out); err != nil {
		return ScopeUnknown, err
	}
	if out.Code != 0 {
		return ScopeUnknown, &FeishuAPIError{Code: out.Code, Message: out.Msg}
	}
	for _, granted := range out.Data.App.Scopes {
		if granted.Scope == scope {
			return ScopePresent, nil
		}
	}
	return ScopeMissing, nil
}

func (c *AccessInfoClient) ListChats(ctx context.Context) ([]KnownChat, error) {
	if c == nil || c.Tokens == nil {
		return nil, fmt.Errorf("feishu access info client unavailable")
	}
	var chats []KnownChat
	pageToken := ""
	for page := 0; page < 5; page++ {
		query := url.Values{"page_size": {"100"}}
		if pageToken != "" {
			query.Set("page_token", pageToken)
		}
		var out struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
			Data struct {
				Items []struct {
					ChatID string `json:"chat_id"`
					Name   string `json:"name"`
				} `json:"items"`
				HasMore   bool   `json:"has_more"`
				PageToken string `json:"page_token"`
			} `json:"data"`
		}
		if err := c.getJSON(ctx, c.baseURL()+"/open-apis/im/v1/chats?"+query.Encode(), &out); err != nil {
			return nil, err
		}
		if out.Code != 0 {
			return nil, &FeishuAPIError{Code: out.Code, Message: out.Msg}
		}
		for _, item := range out.Data.Items {
			if item.ChatID == "" {
				continue
			}
			name := item.Name
			if name == "" {
				name = "(无名)"
			}
			chats = append(chats, KnownChat{ID: item.ChatID, Name: name})
		}
		if !out.Data.HasMore || out.Data.PageToken == "" {
			break
		}
		pageToken = out.Data.PageToken
	}
	return chats, nil
}

func (c *AccessInfoClient) getJSON(ctx context.Context, endpoint string, out any) error {
	token, err := c.Tokens.Token(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := c.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode feishu access response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("feishu access API http status %d", resp.StatusCode)
	}
	return nil
}

func (c *AccessInfoClient) baseURL() string {
	base := c.BaseURL
	if base == "" {
		base = defaultFeishuOpenAPIBaseURL
	}
	return strings.TrimRight(base, "/")
}
