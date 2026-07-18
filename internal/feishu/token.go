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

// TenantTokenSource supplies app-scoped tenant access tokens.
type TenantTokenSource interface {
	Token(context.Context) (string, error)
}

// CachedTenantTokenSource serializes refreshes and reuses a token until one
// minute before its advertised expiry.
type CachedTenantTokenSource struct {
	appID     string
	appSecret string
	baseURL   string
	http      HTTPDoer

	mu    sync.Mutex
	token tenantToken
}

type tenantToken struct {
	Value     string
	ExpiresAt time.Time
}

func NewTenantTokenSource(appID, appSecret string) *CachedTenantTokenSource {
	return &CachedTenantTokenSource{
		appID:     appID,
		appSecret: appSecret,
		baseURL:   defaultFeishuOpenAPIBaseURL,
		http:      http.DefaultClient,
	}
}

func (s *CachedTenantTokenSource) Token(ctx context.Context) (string, error) {
	if s == nil || s.appID == "" || s.appSecret == "" {
		return "", fmt.Errorf("missing feishu app credentials for tenant token")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token.Value != "" && time.Until(s.token.ExpiresAt) > time.Minute {
		return s.token.Value, nil
	}

	payload, err := json.Marshal(map[string]string{"app_id": s.appID, "app_secret": s.appSecret})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.baseURL, "/")+"/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	client := s.http
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var out struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int64  `json:"expire"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decode tenant token response: %w", err)
	}
	if resp.StatusCode >= 400 || out.Code != 0 || out.TenantAccessToken == "" {
		return "", &FeishuAPIError{HTTPStatus: resp.StatusCode, Code: out.Code, Message: out.Msg}
	}
	s.token = tenantToken{
		Value:     out.TenantAccessToken,
		ExpiresAt: time.Now().Add(time.Duration(out.Expire) * time.Second),
	}
	return s.token.Value, nil
}
