package feishu

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultFeishuOpenAPIBaseURL = "https://open.feishu.cn"
	defaultCardKitGlobalQPS     = 20
)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type CardKitCreateRequest struct {
	Card map[string]any
}

type CardKitCreateResult struct {
	CardID string
}

type CardKitReplyRequest struct {
	ReplyToMessageID string
	CardID           string
	UUID             string
}

type CardKitReplyResult struct {
	MessageID string
	ThreadID  string
	ChatID    string
}

type CardKitUpdateCardRequest struct {
	Card     map[string]any
	CardID   string
	Sequence int
	UUID     string
}

type CardKitUpdateSettingsRequest struct {
	CardID   string
	Settings map[string]any
	Sequence int
	UUID     string
}

type CardKitClientAPI interface {
	CreateCard(ctx context.Context, req CardKitCreateRequest) (CardKitCreateResult, error)
	ReplyCard(ctx context.Context, req CardKitReplyRequest) (CardKitReplyResult, error)
	UpdateCard(ctx context.Context, req CardKitUpdateCardRequest) error
	UpdateSettings(ctx context.Context, req CardKitUpdateSettingsRequest) error
}

type CardKitClient struct {
	appID     string
	appSecret string
	baseURL   string
	http      HTTPDoer
	limiter   *serialRateLimiter

	tokenMu sync.Mutex
	token   tenantToken
}

type tenantToken struct {
	Value     string
	ExpiresAt time.Time
}

type FeishuAPIError struct {
	HTTPStatus int
	Code       int
	Message    string
}

func (e *FeishuAPIError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("feishu api failed: http_status=%d code=%d msg=%s", e.HTTPStatus, e.Code, e.Message)
}

func NewCardKitClient(appID, appSecret string) *CardKitClient {
	return &CardKitClient{
		appID:     appID,
		appSecret: appSecret,
		baseURL:   defaultFeishuOpenAPIBaseURL,
		http:      http.DefaultClient,
		limiter:   newSerialRateLimiter(time.Second / defaultCardKitGlobalQPS),
	}
}

func (c *CardKitClient) CreateCard(ctx context.Context, req CardKitCreateRequest) (CardKitCreateResult, error) {
	card, err := json.Marshal(req.Card)
	if err != nil {
		return CardKitCreateResult{}, fmt.Errorf("marshal card json: %w", err)
	}
	body := map[string]string{"type": "card_json", "data": string(card)}
	var out struct {
		CardID string `json:"card_id"`
	}
	if err := c.doTenantJSON(ctx, http.MethodPost, "/open-apis/cardkit/v1/cards", body, &out); err != nil {
		return CardKitCreateResult{}, err
	}
	return CardKitCreateResult{CardID: out.CardID}, nil
}

func (c *CardKitClient) ReplyCard(ctx context.Context, req CardKitReplyRequest) (CardKitReplyResult, error) {
	content, err := json.Marshal(map[string]any{
		"type": "card",
		"data": map[string]string{"card_id": req.CardID},
	})
	if err != nil {
		return CardKitReplyResult{}, fmt.Errorf("marshal card content: %w", err)
	}
	body := map[string]any{
		"msg_type":        "interactive",
		"content":         string(content),
		"reply_in_thread": true,
		"uuid":            req.UUID,
	}
	path := "/open-apis/im/v1/messages/" + url.PathEscape(req.ReplyToMessageID) + "/reply"
	var out struct {
		MessageID string `json:"message_id"`
		ThreadID  string `json:"thread_id"`
		ChatID    string `json:"chat_id"`
	}
	if err := c.doTenantJSON(ctx, http.MethodPost, path, body, &out); err != nil {
		return CardKitReplyResult{}, err
	}
	return CardKitReplyResult{MessageID: out.MessageID, ThreadID: out.ThreadID, ChatID: out.ChatID}, nil
}

func (c *CardKitClient) UpdateCard(ctx context.Context, req CardKitUpdateCardRequest) error {
	card, err := json.Marshal(req.Card)
	if err != nil {
		return fmt.Errorf("marshal card json: %w", err)
	}
	body := map[string]any{
		"card":     map[string]string{"type": "card_json", "data": string(card)},
		"uuid":     req.UUID,
		"sequence": req.Sequence,
	}
	path := "/open-apis/cardkit/v1/cards/" + url.PathEscape(req.CardID)
	return c.doTenantJSON(ctx, http.MethodPut, path, body, nil)
}

func (c *CardKitClient) UpdateSettings(ctx context.Context, req CardKitUpdateSettingsRequest) error {
	settings, err := json.Marshal(req.Settings)
	if err != nil {
		return fmt.Errorf("marshal card settings: %w", err)
	}
	body := map[string]any{"settings": string(settings), "uuid": req.UUID, "sequence": req.Sequence}
	path := "/open-apis/cardkit/v1/cards/" + url.PathEscape(req.CardID) + "/settings"
	return c.doTenantJSON(ctx, http.MethodPatch, path, body, nil)
}

func (c *CardKitClient) doTenantJSON(ctx context.Context, method, path string, body any, out any) error {
	if c == nil {
		return fmt.Errorf("feishu cardkit client unavailable")
	}
	if c.appID == "" || c.appSecret == "" {
		return fmt.Errorf("missing feishu app credentials for cardkit")
	}
	token, err := c.tenantAccessToken(ctx)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal feishu api request: %w", err)
	}
	return c.doWithRetry(ctx, method, path, token, payload, out)
}

func (c *CardKitClient) doWithRetry(ctx context.Context, method, path, token string, payload []byte, out any) error {
	const maxAttempts = 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if c.limiter != nil {
			if err := c.limiter.Wait(ctx); err != nil {
				return err
			}
		}
		err := c.doOnce(ctx, method, path, token, payload, out)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isRetryableFeishuError(err) || attempt == maxAttempts {
			break
		}
		if sleepErr := sleepContext(ctx, time.Duration(attempt)*500*time.Millisecond); sleepErr != nil {
			return sleepErr
		}
	}
	return lastErr
}

func (c *CardKitClient) doOnce(ctx context.Context, method, path, token string, payload []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.baseURL, "/")+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if readErr != nil {
		return readErr
	}
	var envelope struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return fmt.Errorf("decode feishu api response: %w", err)
	}
	if resp.StatusCode >= 400 || envelope.Code != 0 {
		return &FeishuAPIError{HTTPStatus: resp.StatusCode, Code: envelope.Code, Message: envelope.Msg}
	}
	if out != nil && len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("decode feishu api data: %w", err)
		}
	}
	return nil
}

func (c *CardKitClient) tenantAccessToken(ctx context.Context) (string, error) {
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
	expiresAt := time.Now().Add(time.Duration(out.Expire) * time.Second)
	c.tokenMu.Lock()
	c.token = tenantToken{Value: out.TenantAccessToken, ExpiresAt: expiresAt}
	c.tokenMu.Unlock()
	return out.TenantAccessToken, nil
}

func (c *CardKitClient) httpClient() HTTPDoer {
	if c.http != nil {
		return c.http
	}
	return http.DefaultClient
}

func isRetryableFeishuError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *FeishuAPIError
	if !errors.As(err, &apiErr) {
		return true
	}
	if apiErr.HTTPStatus == http.StatusTooManyRequests || apiErr.HTTPStatus >= 500 {
		return true
	}
	if isInvalidCardIDFeishuError(apiErr) {
		return true
	}
	switch apiErr.Code {
	case 200400, 230020, 300120:
		return true
	default:
		return false
	}
}

func isInvalidCardIDFeishuError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *FeishuAPIError
	if !errors.As(err, &apiErr) {
		return false
	}
	normalized := strings.NewReplacer("_", "", " ", "", "-", "").Replace(strings.ToLower(apiErr.Message))
	return apiErr.Code == 230099 &&
		strings.Contains(normalized, "cardid") &&
		strings.Contains(normalized, "invalid")
}

type serialRateLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func newSerialRateLimiter(interval time.Duration) *serialRateLimiter {
	if interval <= 0 {
		return nil
	}
	return &serialRateLimiter{interval: interval}
}

func (l *serialRateLimiter) Wait(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	now := time.Now()
	waitUntil := now
	if l.next.After(now) {
		waitUntil = l.next
	}
	l.next = waitUntil.Add(l.interval)
	l.mu.Unlock()
	if delay := time.Until(waitUntil); delay > 0 {
		return sleepContext(ctx, delay)
	}
	return nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func stableUUID(prefix string, parts ...string) string {
	sum := sha1.Sum([]byte(prefix + ":" + strings.Join(parts, ":")))
	var uuid [16]byte
	copy(uuid[:], sum[:16])
	uuid[6] = (uuid[6] & 0x0f) | 0x50
	uuid[8] = (uuid[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		uuid[0], uuid[1], uuid[2], uuid[3],
		uuid[4], uuid[5],
		uuid[6], uuid[7],
		uuid[8], uuid[9],
		uuid[10], uuid[11], uuid[12], uuid[13], uuid[14], uuid[15],
	)
}
