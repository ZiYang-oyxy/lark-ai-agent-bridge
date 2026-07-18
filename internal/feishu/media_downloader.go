package feishu

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"

	"lark-agent-bridge/internal/media"
)

// MediaDownloader streams Feishu message resources directly from OpenAPI.
type MediaDownloader struct {
	BaseURL string
	Client  *http.Client
	Tokens  TenantTokenSource
}

func (d *MediaDownloader) Open(ctx context.Context, ref media.Ref) (media.Download, error) {
	if d == nil || d.Tokens == nil {
		return media.Download{}, fmt.Errorf("feishu media downloader unavailable")
	}
	if ref.MessageID == "" || ref.FileKey == "" {
		return media.Download{}, fmt.Errorf("missing feishu media resource identity")
	}
	if ref.Kind != "image" && ref.Kind != "file" {
		return media.Download{}, fmt.Errorf("unsupported feishu media resource kind %q", ref.Kind)
	}
	token, err := d.Tokens.Token(ctx)
	if err != nil {
		return media.Download{}, err
	}
	baseURL := d.BaseURL
	if baseURL == "" {
		baseURL = defaultFeishuOpenAPIBaseURL
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/open-apis/im/v1/messages/" +
		url.PathEscape(ref.MessageID) + "/resources/" + url.PathEscape(ref.FileKey) +
		"?type=" + url.QueryEscape(ref.Kind)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return media.Download{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := d.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return media.Download{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if readErr != nil {
			return media.Download{}, fmt.Errorf("read feishu media error response: %w", readErr)
		}
		return media.Download{}, fmt.Errorf("feishu media download failed: http_status=%d body=%q", resp.StatusCode, body)
	}

	return media.Download{
		Body:        resp.Body,
		Name:        contentDispositionFilename(resp.Header.Get("Content-Disposition")),
		ContentType: resp.Header.Get("Content-Type"),
	}, nil
}

func contentDispositionFilename(value string) string {
	_, params, err := mime.ParseMediaType(value)
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(params["filename"])
	if name == "" {
		return ""
	}
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	if name == "." || name == "/" {
		return ""
	}
	return name
}
