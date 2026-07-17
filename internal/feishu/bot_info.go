package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type BotInfo struct {
	AppName string
	OpenID  string
}

type BotInfoClient struct {
	appID     string
	appSecret string
	baseURL   string
	http      HTTPDoer

	tokenMu sync.Mutex
	token   tenantToken
}

func NewBotInfoClient(appID, appSecret string) *BotInfoClient {
	return &BotInfoClient{
		appID:     appID,
		appSecret: appSecret,
		baseURL:   defaultFeishuOpenAPIBaseURL,
		http:      http.DefaultClient,
	}
}

func FetchBotInfo(ctx context.Context, appID, appSecret string) (BotInfo, error) {
	return NewBotInfoClient(appID, appSecret).Get(ctx)
}

func FetchBotOpenID(ctx context.Context, appID, appSecret string) (string, error) {
	info, err := FetchBotInfo(ctx, appID, appSecret)
	if err != nil {
		return "", err
	}
	if info.OpenID == "" {
		return "", fmt.Errorf("feishu bot info returned empty open_id")
	}
	return info.OpenID, nil
}

func (c *BotInfoClient) Get(ctx context.Context) (BotInfo, error) {
	if c == nil {
		return BotInfo{}, fmt.Errorf("feishu bot info client unavailable")
	}
	if c.appID == "" || c.appSecret == "" {
		return BotInfo{}, fmt.Errorf("missing feishu app credentials for bot info")
	}
	token, err := c.tenantAccessToken(ctx)
	if err != nil {
		return BotInfo{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.baseURL, "/")+"/open-apis/bot/v3/info", nil)
	if err != nil {
		return BotInfo{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return BotInfo{}, err
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return BotInfo{}, readErr
	}
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Bot  struct {
			AppName string `json:"app_name"`
			OpenID  string `json:"open_id"`
		} `json:"bot"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return BotInfo{}, fmt.Errorf("decode bot info response: %w", err)
	}
	if resp.StatusCode >= 400 || out.Code != 0 {
		return BotInfo{}, &FeishuAPIError{HTTPStatus: resp.StatusCode, Code: out.Code, Message: out.Msg}
	}
	if out.Bot.OpenID == "" {
		return BotInfo{}, fmt.Errorf("feishu bot info returned empty open_id")
	}
	return BotInfo{AppName: out.Bot.AppName, OpenID: out.Bot.OpenID}, nil
}

func (c *BotInfoClient) tenantAccessToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	if c.token.Value != "" && time.Until(c.token.ExpiresAt) > 2*time.Minute {
		value := c.token.Value
		c.tokenMu.Unlock()
		return value, nil
	}
	c.tokenMu.Unlock()

	payload, err := json.Marshal(map[string]string{"app_id": c.appID, "app_secret": c.appSecret})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.baseURL, "/")+"/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return "", readErr
	}
	var out struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int64  `json:"expire"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", fmt.Errorf("decode tenant token response: %w", err)
	}
	if resp.StatusCode >= 400 || out.Code != 0 || out.TenantAccessToken == "" {
		return "", &FeishuAPIError{HTTPStatus: resp.StatusCode, Code: out.Code, Message: out.Msg}
	}
	c.tokenMu.Lock()
	c.token = tenantToken{Value: out.TenantAccessToken, ExpiresAt: time.Now().Add(time.Duration(out.Expire) * time.Second)}
	c.tokenMu.Unlock()
	return out.TenantAccessToken, nil
}

func (c *BotInfoClient) httpClient() HTTPDoer {
	if c.http != nil {
		return c.http
	}
	return http.DefaultClient
}
