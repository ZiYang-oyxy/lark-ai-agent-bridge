package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

type tenantTokenInvalidator interface {
	Invalidate(string)
}

// CachedTenantTokenSource serializes refreshes and reuses a token until one
// minute before its advertised expiry.
type CachedTenantTokenSource struct {
	appID     string
	appSecret string
	baseURL   string
	http      HTTPDoer

	mu      sync.Mutex
	token   tenantToken
	refresh *tenantTokenRefresh
}

type tenantToken struct {
	Value     string
	ExpiresAt time.Time
}

type tenantTokenRefresh struct {
	done  chan struct{}
	value string
	err   error
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
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	if s.token.Value != "" && time.Until(s.token.ExpiresAt) > time.Minute {
		value := s.token.Value
		s.mu.Unlock()
		return value, nil
	}
	if active := s.refresh; active != nil {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-active.done:
			if err := ctx.Err(); err != nil {
				return "", err
			}
			return active.value, active.err
		}
	}
	active := &tenantTokenRefresh{done: make(chan struct{})}
	s.refresh = active
	s.mu.Unlock()

	token, err := s.refreshToken(ctx)
	s.mu.Lock()
	if err == nil {
		s.token = token
		active.value = token.Value
	}
	active.err = err
	s.refresh = nil
	close(active.done)
	s.mu.Unlock()
	return active.value, active.err
}

// Invalidate removes value from the cache if it is still the current token.
// Matching the value prevents a delayed rejected request from evicting a token
// another request has already refreshed.
func (s *CachedTenantTokenSource) Invalidate(value string) {
	if s == nil || value == "" {
		return
	}
	s.mu.Lock()
	if s.token.Value == value {
		s.token = tenantToken{}
	}
	s.mu.Unlock()
}

func retryRejectedTenantToken(ctx context.Context, source TenantTokenSource, request func(string) error) error {
	token, err := source.Token(ctx)
	if err != nil {
		return err
	}
	err = request(token)
	if !isRejectedTenantToken(err) {
		return err
	}
	invalidator, ok := source.(tenantTokenInvalidator)
	if !ok {
		return err
	}
	invalidator.Invalidate(token)
	freshToken, err := source.Token(ctx)
	if err != nil {
		return err
	}
	return request(freshToken)
}

func isRejectedTenantToken(err error) bool {
	var apiErr *FeishuAPIError
	return errors.As(err, &apiErr) && apiErr.Code == 99991663
}

func (s *CachedTenantTokenSource) refreshToken(ctx context.Context) (tenantToken, error) {
	payload, err := json.Marshal(map[string]string{"app_id": s.appID, "app_secret": s.appSecret})
	if err != nil {
		return tenantToken{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.baseURL, "/")+"/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(payload))
	if err != nil {
		return tenantToken{}, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	client := s.http
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return tenantToken{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tenantToken{}, err
	}
	var out struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int64  `json:"expire"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return tenantToken{}, fmt.Errorf("decode tenant token response: %w", err)
	}
	if resp.StatusCode >= 400 || out.Code != 0 || out.TenantAccessToken == "" {
		return tenantToken{}, &FeishuAPIError{HTTPStatus: resp.StatusCode, Code: out.Code, Message: out.Msg}
	}
	return tenantToken{
		Value:     out.TenantAccessToken,
		ExpiresAt: time.Now().Add(time.Duration(out.Expire) * time.Second),
	}, nil
}
