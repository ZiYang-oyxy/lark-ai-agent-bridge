package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Download is a streaming media response. The caller owns Body.
type Download struct {
	Body        io.ReadCloser
	Name        string
	ContentType string
}

// Downloader opens one Feishu resource without buffering its body.
type Downloader interface {
	Open(context.Context, Ref) (Download, error)
}

// Resolution contains accepted attachments and per-resource failures.
type Resolution struct {
	Attachments []Attachment
	Failures    []Failure
	Release     func()
}

// Cache stores validated resources by content hash and tracks paths being
// handed from resolution to durable session storage.
type Cache struct {
	root   string
	limits Limits

	mu         sync.Mutex
	pending    map[string]int
	generation uint64
	deleting   map[string]chan struct{}
}

// NewCache constructs a media cache. The directory is created lazily.
func NewCache(root string, limits Limits) *Cache {
	return &Cache{root: root, limits: limits, pending: make(map[string]int), deleting: make(map[string]chan struct{})}
}

// Resolve streams, validates and stores refs. Failure of one resource does
// not discard other accepted resources.
func (c *Cache) Resolve(ctx context.Context, downloader Downloader, refs []Ref) Resolution {
	result := Resolution{Release: func() {}}
	batchBytes := int64(0)
	batchFull := false
	processed := 0

	for _, ref := range refs {
		if batchFull {
			result.Failures = append(result.Failures, failure(ref, "batch_too_large", "attachment batch byte limit reached"))
			continue
		}
		if c.limits.MaxFiles > 0 && processed >= c.limits.MaxFiles {
			result.Failures = append(result.Failures, failure(ref, "too_many_files", "attachment count limit reached"))
			continue
		}
		processed++

		attachment, failed := c.resolveOne(ctx, downloader, ref)
		if failed != nil {
			result.Failures = append(result.Failures, *failed)
			continue
		}
		if c.limits.MaxBatchBytes > 0 && batchBytes+attachment.Size > c.limits.MaxBatchBytes {
			c.releasePending(attachment.Path)
			result.Failures = append(result.Failures, failure(ref, "batch_too_large", "attachment batch byte limit reached"))
			batchFull = true
			continue
		}
		batchBytes += attachment.Size
		result.Attachments = append(result.Attachments, attachment)
	}

	paths := make([]string, 0, len(result.Attachments))
	for _, attachment := range result.Attachments {
		paths = append(paths, attachment.Path)
	}

	var once sync.Once
	result.Release = func() {
		once.Do(func() {
			for _, path := range paths {
				c.releasePending(path)
			}
		})
	}
	return result
}

// PendingPaths returns a copy of paths protected by live resolve leases.
func (c *Cache) PendingPaths() map[string]struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	paths := make(map[string]struct{}, len(c.pending))
	for path := range c.pending {
		paths[path] = struct{}{}
	}
	return paths
}

func (c *Cache) resolveOne(ctx context.Context, downloader Downloader, ref Ref) (Attachment, *Failure) {
	if err := c.checkQuotaBeforeDownload(); err != nil {
		if quota, ok := err.(*QuotaError); ok {
			failed := failure(ref, string(QuotaExceeded), quota.Error())
			return Attachment{}, &failed
		}
		failed := failure(ref, "cache_write_failed", err.Error())
		return Attachment{}, &failed
	}
	if err := ctx.Err(); err != nil {
		failed := failure(ref, "download_failed", err.Error())
		return Attachment{}, &failed
	}
	download, err := downloader.Open(ctx, ref)
	if err != nil {
		failed := failure(ref, "download_failed", err.Error())
		return Attachment{}, &failed
	}
	if download.Body == nil {
		failed := failure(ref, "download_failed", "empty response body")
		return Attachment{}, &failed
	}
	defer download.Body.Close()

	name := strings.TrimSpace(download.Name)
	if name == "" {
		name = strings.TrimSpace(ref.Name)
	}
	declared, failed := validateDeclared(ref, name, download.ContentType)
	if failed != nil {
		return Attachment{}, failed
	}

	prefix := make([]byte, 512)
	n, readErr := io.ReadFull(download.Body, prefix)
	if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		failed := failure(ref, "download_failed", readErr.Error())
		return Attachment{}, &failed
	}
	prefix = prefix[:n]
	detected, _, err := mime.ParseMediaType(http.DetectContentType(prefix))
	if err == nil && isTextMIME(declared) && detected == "application/octet-stream" && looksBinaryText(prefix) {
		failed := failure(ref, "binary_text", "text attachment contains binary control bytes")
		return Attachment{}, &failed
	}
	if err != nil || !contentMatches(declared, strings.ToLower(detected), prefix) {
		failed := failure(ref, "content_mismatch", fmt.Sprintf("declared %s, detected %s", declared, detected))
		return Attachment{}, &failed
	}
	if isTextMIME(declared) && looksBinaryText(prefix) {
		failed := failure(ref, "binary_text", "text attachment contains binary control bytes")
		return Attachment{}, &failed
	}

	if err := os.MkdirAll(c.root, 0o700); err != nil {
		failed := failure(ref, "cache_write_failed", err.Error())
		return Attachment{}, &failed
	}
	if err := os.Chmod(c.root, 0o700); err != nil {
		failed := failure(ref, "cache_write_failed", err.Error())
		return Attachment{}, &failed
	}
	tmp, err := os.CreateTemp(c.root, ".download-*.tmp")
	if err != nil {
		failed := failure(ref, "cache_write_failed", err.Error())
		return Attachment{}, &failed
	}
	tmpName := tmp.Name()
	complete := false
	defer func() {
		_ = tmp.Close()
		if !complete {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		failed := failure(ref, "cache_write_failed", err.Error())
		return Attachment{}, &failed
	}

	hasher := sha256.New()
	reader := io.MultiReader(bytes.NewReader(prefix), download.Body)
	if c.limits.MaxFileBytes > 0 {
		reader = io.LimitReader(reader, c.limits.MaxFileBytes+1)
	}
	size, err := io.Copy(io.MultiWriter(tmp, hasher), reader)
	if err != nil {
		failed := failure(ref, "download_failed", err.Error())
		return Attachment{}, &failed
	}
	if c.limits.MaxFileBytes > 0 && size > c.limits.MaxFileBytes {
		failed := failure(ref, "file_too_large", "attachment exceeds per-file byte limit")
		return Attachment{}, &failed
	}
	if err := tmp.Sync(); err != nil {
		failed := failure(ref, "cache_write_failed", err.Error())
		return Attachment{}, &failed
	}
	if err := tmp.Close(); err != nil {
		failed := failure(ref, "cache_write_failed", err.Error())
		return Attachment{}, &failed
	}

	digest := hex.EncodeToString(hasher.Sum(nil))
	extension := extensionForMIME(declared)
	if extension == "" {
		failed := failure(ref, "content_mismatch", "detected media type has no safe extension")
		return Attachment{}, &failed
	}
	finalPath := filepath.Join(c.root, digest+extension)
	if err := c.admitFinalPath(finalPath); err != nil {
		if quota, ok := err.(*QuotaError); ok {
			failed := failure(ref, string(QuotaExceeded), quota.Error())
			return Attachment{}, &failed
		}
		failed := failure(ref, "cache_write_failed", err.Error())
		return Attachment{}, &failed
	}
	if err := os.Rename(tmpName, finalPath); err != nil {
		c.releasePending(finalPath)
		failed := failure(ref, "cache_write_failed", err.Error())
		return Attachment{}, &failed
	}
	complete = true
	return Attachment{Ref: ref, Path: finalPath, MIME: declared, SHA256: digest, Size: size}, nil
}

// FailureCode is the stable, machine-readable media failure identifier.
type FailureCode string

const QuotaExceeded FailureCode = "quota_exceeded"

// QuotaError describes a pre-download cache admission rejection.
type QuotaError struct {
	Usage int64
	Limit int64
}

func (e *QuotaError) Error() string {
	return fmt.Sprintf("media cache quota exceeded: usage=%d limit=%d", e.Usage, e.Limit)
}

func (c *Cache) checkQuotaBeforeDownload() error {
	if c.limits.CacheQuotaBytes <= 0 {
		return nil
	}
	usage, err := cacheUsage(c.root)
	if err != nil {
		return err
	}
	if usage >= c.limits.CacheQuotaBytes {
		return &QuotaError{Usage: usage, Limit: c.limits.CacheQuotaBytes}
	}
	return nil
}

func (c *Cache) admitFinalPath(path string) error {
	for {
		generation := c.pendingGeneration()
		if c.limits.CacheQuotaBytes > 0 {
			usage, err := cacheUsageAfterCommit(c.root, path)
			if err != nil {
				return err
			}
			if usage > c.limits.CacheQuotaBytes {
				if c.pendingGeneration() != generation {
					continue
				}
				return &QuotaError{Usage: usage, Limit: c.limits.CacheQuotaBytes}
			}
		}
		acquired, wait := c.acquirePendingAtGeneration(path, generation)
		if acquired {
			return nil
		}
		if wait != nil {
			<-wait
		}
	}
}

func (c *Cache) releasePending(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending[path] <= 1 {
		delete(c.pending, path)
	} else {
		c.pending[path]--
	}
	c.generation++
}

func (c *Cache) pendingGeneration() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generation
}

func (c *Cache) pendingSnapshot() (map[string]struct{}, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	paths := make(map[string]struct{}, len(c.pending))
	for path := range c.pending {
		paths[path] = struct{}{}
	}
	return paths, c.generation
}

func (c *Cache) acquirePendingAtGeneration(path string, generation uint64) (bool, <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation != generation {
		return false, nil
	}
	if done := c.deleting[path]; done != nil {
		return false, done
	}
	c.pending[path]++
	c.generation++
	return true, nil
}

type deleteClaim int

const (
	deleteProtected deleteClaim = iota
	deleteStale
	deleteAcquired
)

func (c *Cache) claimDelete(path string, generation uint64) (deleteClaim, chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation != generation {
		return deleteStale, nil
	}
	if c.pending[path] > 0 || c.deleting[path] != nil {
		return deleteProtected, nil
	}
	done := make(chan struct{})
	c.deleting[path] = done
	return deleteAcquired, done
}

func (c *Cache) finishDelete(path string, done chan struct{}, generation uint64) (uint64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	unchanged := c.generation == generation
	if c.deleting[path] == done {
		delete(c.deleting, path)
		close(done)
	}
	c.generation++
	return c.generation, unchanged
}

func validateDeclared(ref Ref, name, contentType string) (string, *Failure) {
	if name != "" {
		canonical, err := Validate(name, contentType)
		if err != nil {
			failed := failure(ref, "unsupported_media", err.Error())
			return "", &failed
		}
		return canonical, nil
	}
	declared, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		failed := failure(ref, "unsupported_media", err.Error())
		return "", &failed
	}
	declared = strings.ToLower(declared)
	if ref.Kind != "image" || extensionForMIME(declared) == "" || !strings.HasPrefix(declared, "image/") {
		failed := failure(ref, "unsupported_media", "resource without filename must be an allowlisted image")
		return "", &failed
	}
	return declared, nil
}

func contentMatches(declared, detected string, prefix []byte) bool {
	if isTextMIME(declared) {
		return strings.HasPrefix(detected, "text/plain")
	}
	return declared == detected && len(prefix) > 0
}

func isTextMIME(value string) bool {
	switch value {
	case "text/plain", "text/markdown", "application/json", "text/csv":
		return true
	default:
		return false
	}
}

func looksBinaryText(sample []byte) bool {
	if len(sample) == 0 {
		return false
	}
	controls := 0
	for _, value := range sample {
		if value == 0 {
			return true
		}
		if value < 0x20 && value != '\t' && value != '\n' && value != '\r' {
			controls++
		}
	}
	return controls*20 > len(sample)
}

func extensionForMIME(value string) string {
	switch value {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "text/plain":
		return ".txt"
	case "text/markdown":
		return ".md"
	case "application/json":
		return ".json"
	case "text/csv":
		return ".csv"
	default:
		return ""
	}
}

func failure(ref Ref, code, detail string) Failure {
	return Failure{Ref: ref, Code: code, Detail: detail}
}
