package update

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const defaultCacheTTL = 10 * time.Minute

type CheckResult struct {
	Manifest            Manifest
	Asset               Asset
	UpdateAvailable     bool
	UnsupportedPlatform bool
}

type Staged struct {
	Path  string
	Asset Asset
}

type Client struct {
	ManifestURL string
	// PrereleaseURL is the manifest for the opt-in prerelease (rc) channel. When
	// empty, prerelease mode falls back to the stable ManifestURL.
	PrereleaseURL string
	// Prerelease reports whether the developer prerelease channel is active. It
	// is consulted live on every Check so a /.devel toggle takes effect without
	// reconstructing the client. Nil means "always stable".
	Prerelease func() bool
	HTTP       *http.Client
	Now        func() time.Time
	GOOS       string
	GOARCH     string
	CacheTTL   time.Duration

	mu     sync.Mutex
	cached map[string]cacheEntry // keyed by resolved manifest URL
}

type cacheEntry struct {
	manifest Manifest
	at       time.Time
}

func NewClient(manifestURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Client{
		ManifestURL: manifestURL,
		HTTP:        httpClient,
		Now:         time.Now,
		GOOS:        runtime.GOOS,
		GOARCH:      runtime.GOARCH,
		CacheTTL:    defaultCacheTTL,
	}
}

func (c *Client) Invalidate() {
	c.mu.Lock()
	c.cached = nil
	c.mu.Unlock()
}

// prerelease reports whether the opt-in prerelease channel is currently active.
func (c *Client) prerelease() bool {
	return c.Prerelease != nil && c.Prerelease()
}

// activeURL resolves the manifest URL for the current channel. In prerelease
// mode it uses an explicit PrereleaseURL when set, otherwise it derives the
// prerelease manifest from the stable URL by swapping the "-stable-" channel
// segment for "-prerelease-" — so enabling developer mode needs no extra
// configuration when the two channels follow the standard naming. If neither
// applies it degrades to stable rather than erroring (a missing prerelease
// channel simply means "nothing newer").
func (c *Client) activeURL() string {
	if !c.prerelease() {
		return c.ManifestURL
	}
	if url := strings.TrimSpace(c.PrereleaseURL); url != "" {
		return url
	}
	if derived, ok := derivePrereleaseURL(c.ManifestURL); ok {
		return derived
	}
	return c.ManifestURL
}

// derivePrereleaseURL turns a stable manifest URL into its prerelease sibling
// by replacing the "-stable-" segment with "-prerelease-". Returns false when
// the URL does not contain that segment, so the caller can fall back safely.
func derivePrereleaseURL(stableURL string) (string, bool) {
	const stableSeg = "-stable-"
	if !strings.Contains(stableURL, stableSeg) {
		return "", false
	}
	return strings.Replace(stableURL, stableSeg, "-prerelease-", 1), true
}

func (c *Client) Check(ctx context.Context, currentVersion string) (CheckResult, error) {
	prerelease := c.prerelease()
	manifest, err := c.manifest(ctx, c.activeURL())
	if err != nil {
		return CheckResult{}, err
	}
	asset, ok := manifest.Asset(c.GOOS, c.GOARCH)
	if !ok || !supportedPlatform(c.GOOS, c.GOARCH) {
		return CheckResult{Manifest: manifest, UnsupportedPlatform: true}, nil
	}
	// Stable channel keeps the strict gate (rc versions fail to parse and never
	// qualify). Prerelease channel uses the rc-aware comparator so rc builds can
	// be offered and ordered.
	compare := CompareStable
	if prerelease {
		compare = CompareAllowingPrerelease
	}
	comparison, err := compare(manifest.Version, currentVersion)
	if err != nil {
		return CheckResult{}, fmt.Errorf("compare update versions: %w", err)
	}
	return CheckResult{Manifest: manifest, Asset: asset, UpdateAvailable: comparison > 0}, nil
}

func (c *Client) manifest(ctx context.Context, manifestURL string) (Manifest, error) {
	if err := validateHTTPS(manifestURL); err != nil {
		return Manifest{}, fmt.Errorf("invalid manifest URL: %w", err)
	}
	now := c.Now()
	c.mu.Lock()
	if entry, ok := c.cached[manifestURL]; ok && now.Sub(entry.at) < c.cacheTTL() {
		cached := entry.manifest
		c.mu.Unlock()
		return cached, nil
	}
	c.mu.Unlock()

	requestCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	response, err := c.doGET(requestCtx, manifestURL)
	if err != nil {
		return Manifest{}, fmt.Errorf("fetch update manifest: %w", err)
	}
	defer response.Body.Close()
	manifest, err := ParseManifest(response.Body)
	if err != nil {
		return Manifest{}, err
	}
	c.mu.Lock()
	if c.cached == nil {
		c.cached = make(map[string]cacheEntry)
	}
	c.cached[manifestURL] = cacheEntry{manifest: manifest, at: now}
	c.mu.Unlock()
	return manifest, nil
}

func (c *Client) ReleaseNotes(ctx context.Context, manifest Manifest) (string, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	response, err := c.doGET(requestCtx, manifest.ReleaseNotesURL)
	if err != nil {
		return "", fmt.Errorf("fetch release notes: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxReleaseNotesBytes+1))
	if err != nil {
		return "", fmt.Errorf("read release notes: %w", err)
	}
	if len(data) > MaxReleaseNotesBytes {
		return "", errors.New("release notes exceed 64 KiB")
	}
	if !utf8.Valid(data) {
		return "", errors.New("release notes are not UTF-8")
	}
	return string(data), nil
}

func (c *Client) Stage(ctx context.Context, asset Asset, dir string) (staged Staged, err error) {
	if err := validateHTTPS(asset.URL); err != nil {
		return Staged{}, err
	}
	if asset.Size <= 0 || asset.Size > MaxBinaryBytes {
		return Staged{}, errors.New("binary size is outside the allowed range")
	}
	requestCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	response, err := c.doGET(requestCtx, asset.URL)
	if err != nil {
		return Staged{}, fmt.Errorf("download update binary: %w", err)
	}
	defer response.Body.Close()
	file, err := os.CreateTemp(dir, ".lark-agent-bridge-update-*")
	if err != nil {
		return Staged{}, fmt.Errorf("create staged update: %w", err)
	}
	path := file.Name()
	defer func() {
		_ = file.Close()
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(response.Body, asset.Size+1))
	if err != nil {
		return Staged{}, fmt.Errorf("write staged update: %w", err)
	}
	if written != asset.Size {
		return Staged{}, fmt.Errorf("binary size %d does not match manifest %d", written, asset.Size)
	}
	actualHash := fmt.Sprintf("%x", hasher.Sum(nil))
	if !strings.EqualFold(actualHash, asset.SHA256) {
		return Staged{}, errors.New("binary sha256 does not match manifest")
	}
	if err := file.Sync(); err != nil {
		return Staged{}, fmt.Errorf("sync staged update: %w", err)
	}
	if err := file.Close(); err != nil {
		return Staged{}, fmt.Errorf("close staged update: %w", err)
	}
	return Staged{Path: filepath.Clean(path), Asset: asset}, nil
}

func (c *Client) doGET(ctx context.Context, rawURL string) (*http.Response, error) {
	if err := validateHTTPS(rawURL); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	httpClient := *c.HTTP
	previousRedirectPolicy := httpClient.CheckRedirect
	httpClient.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) > 3 {
			return errors.New("too many HTTPS redirects")
		}
		if err := validateHTTPS(next.URL.String()); err != nil {
			return fmt.Errorf("unsafe redirect: %w", err)
		}
		if previousRedirectPolicy != nil {
			return previousRedirectPolicy(next, via)
		}
		return nil
	}
	response, err := httpClient.Do(request)
	if err != nil {
		safeURL := rawURL
		if parsed, parseErr := url.Parse(rawURL); parseErr == nil {
			parsed.RawQuery = ""
			parsed.Fragment = ""
			safeURL = parsed.String()
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("GET %s failed: %w", safeURL, ctx.Err())
		}
		return nil, fmt.Errorf("GET %s failed", safeURL)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, fmt.Errorf("unexpected HTTP status %d", response.StatusCode)
	}
	return response, nil
}

func (c *Client) cacheTTL() time.Duration {
	if c.CacheTTL <= 0 {
		return defaultCacheTTL
	}
	return c.CacheTTL
}

func supportedPlatform(goos, goarch string) bool {
	return (goos == "linux" && goarch == "amd64") || (goos == "darwin" && goarch == "arm64")
}
