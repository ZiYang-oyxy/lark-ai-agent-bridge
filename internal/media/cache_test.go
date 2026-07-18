package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type downloadFixture struct {
	name        string
	contentType string
	body        io.ReadCloser
	err         error
}

type fixtureDownloader struct {
	mu       sync.Mutex
	fixtures map[string]downloadFixture
	opens    []string
}

func (d *fixtureDownloader) Open(_ context.Context, ref Ref) (Download, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.opens = append(d.opens, ref.FileKey)
	fixture := d.fixtures[ref.FileKey]
	if fixture.err != nil {
		return Download{}, fixture.err
	}
	return Download{Body: fixture.body, Name: fixture.name, ContentType: fixture.contentType}, nil
}

func fixture(name, contentType string, data []byte) downloadFixture {
	return downloadFixture{name: name, contentType: contentType, body: io.NopCloser(bytes.NewReader(data))}
}

func testLimits() Limits {
	return Limits{MaxFileBytes: 1024, MaxBatchBytes: 4096, MaxFiles: 10, CacheQuotaBytes: 8192}
}

func pngFixture() []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 24)...)
}

func TestCacheStoresContentAddressedFileAndLeasesPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "media")
	data := pngFixture()
	downloader := &fixtureDownloader{fixtures: map[string]downloadFixture{
		"image": fixture("../../private name.PNG", "image/png", data),
	}}
	cache := NewCache(root, testLimits())

	resolution := cache.Resolve(context.Background(), downloader, []Ref{{MessageID: "m1", FileKey: "image", Kind: "image", Name: "ignored.png"}})
	if len(resolution.Failures) != 0 || len(resolution.Attachments) != 1 {
		t.Fatalf("resolution = %+v", resolution)
	}
	attachment := resolution.Attachments[0]
	wantHash := sha256.Sum256(data)
	if attachment.SHA256 != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("SHA256 = %q", attachment.SHA256)
	}
	if attachment.MIME != "image/png" || attachment.Size != int64(len(data)) {
		t.Fatalf("attachment metadata = %+v", attachment)
	}
	if filepath.Base(attachment.Path) != attachment.SHA256+".png" {
		t.Fatalf("unsafe cache basename %q", filepath.Base(attachment.Path))
	}
	if strings.Contains(attachment.Path, "private name") || !filepath.IsAbs(attachment.Path) {
		t.Fatalf("unsafe cache path %q", attachment.Path)
	}
	assertMode(t, root, 0o700)
	assertMode(t, attachment.Path, 0o600)
	if _, ok := cache.PendingPaths()[attachment.Path]; !ok {
		t.Fatalf("attachment path is not leased")
	}
	resolution.Release()
	resolution.Release()
	if got := cache.PendingPaths(); len(got) != 0 {
		t.Fatalf("leases after idempotent release = %v", got)
	}
}

func TestCacheRejectsOversizeWhileStreamingAndRemovesTemporaryFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "media")
	reader := &recordingReader{remaining: 64*1024 + 1}
	downloader := &fixtureDownloader{fixtures: map[string]downloadFixture{
		"large": {name: "large.txt", contentType: "text/plain", body: io.NopCloser(reader)},
	}}
	limits := testLimits()
	limits.MaxFileBytes = 64 * 1024
	cache := NewCache(root, limits)

	resolution := cache.Resolve(context.Background(), downloader, []Ref{{MessageID: "m1", FileKey: "large", Kind: "file", Name: "large.txt"}})
	if len(resolution.Attachments) != 0 || len(resolution.Failures) != 1 || resolution.Failures[0].Code != "file_too_large" {
		t.Fatalf("resolution = %+v", resolution)
	}
	if reader.maxRequest > 32*1024 {
		t.Fatalf("reader requested %d bytes at once; want streaming reads", reader.maxRequest)
	}
	if reader.total != limits.MaxFileBytes+1 {
		t.Fatalf("reader consumed %d bytes, want %d", reader.total, limits.MaxFileBytes+1)
	}
	if files := regularFiles(t, root); len(files) != 0 {
		t.Fatalf("incomplete files remain: %v", files)
	}
}

func TestCacheRejectsDeclaredAndDetectedContentMismatch(t *testing.T) {
	downloader := &fixtureDownloader{fixtures: map[string]downloadFixture{
		"fake-image": fixture("fake.png", "image/png", []byte("plain text pretending to be an image")),
		"fake-text":  fixture("fake.txt", "text/plain", pngFixture()),
	}}
	cache := NewCache(filepath.Join(t.TempDir(), "media"), testLimits())

	resolution := cache.Resolve(context.Background(), downloader, []Ref{
		{MessageID: "m1", FileKey: "fake-image", Kind: "image", Name: "fake.png"},
		{MessageID: "m1", FileKey: "fake-text", Kind: "file", Name: "fake.txt"},
	})
	if len(resolution.Attachments) != 0 || len(resolution.Failures) != 2 {
		t.Fatalf("resolution = %+v", resolution)
	}
	for _, failure := range resolution.Failures {
		if failure.Code != "content_mismatch" {
			t.Fatalf("failure = %+v", failure)
		}
	}
}

func TestCacheAcceptsGenericFeishuFileContentTypeAfterSniffing(t *testing.T) {
	data := []byte("plain text from a Feishu file resource\n")
	downloader := &fixtureDownloader{fixtures: map[string]downloadFixture{
		"notes": fixture("notes.txt", "application/octet-stream", data),
	}}
	cache := NewCache(filepath.Join(t.TempDir(), "media"), testLimits())

	resolution := cache.Resolve(context.Background(), downloader, []Ref{{MessageID: "m1", FileKey: "notes", Kind: "file", Name: "notes.txt"}})
	defer resolution.Release()
	if len(resolution.Failures) != 0 || len(resolution.Attachments) != 1 {
		t.Fatalf("resolution = %+v", resolution)
	}
	if got := resolution.Attachments[0].MIME; got != "text/plain" {
		t.Fatalf("canonical MIME = %q, want text/plain", got)
	}
}

func TestCacheAcceptsFeishuCSVTransportMIMEAfterSniffing(t *testing.T) {
	data := []byte("kind,marker\nmedia,e2e\n")
	downloader := &fixtureDownloader{fixtures: map[string]downloadFixture{
		"csv": fixture("sample.csv", "application/x-xls", data),
	}}
	cache := NewCache(filepath.Join(t.TempDir(), "media"), testLimits())

	resolution := cache.Resolve(context.Background(), downloader, []Ref{{MessageID: "m1", FileKey: "csv", Kind: "file", Name: "sample.csv"}})
	defer resolution.Release()
	if len(resolution.Failures) != 0 || len(resolution.Attachments) != 1 {
		t.Fatalf("resolution = %+v", resolution)
	}
	if got := resolution.Attachments[0].MIME; got != "text/csv" {
		t.Fatalf("canonical MIME = %q, want text/csv", got)
	}
}

func TestCacheStillRejectsGenericFeishuFileContentMismatch(t *testing.T) {
	downloader := &fixtureDownloader{fixtures: map[string]downloadFixture{
		"forged": fixture("forged.png", "application/octet-stream", []byte("plain text disguised as PNG")),
	}}
	cache := NewCache(filepath.Join(t.TempDir(), "media"), testLimits())

	resolution := cache.Resolve(context.Background(), downloader, []Ref{{MessageID: "m1", FileKey: "forged", Kind: "file", Name: "forged.png"}})
	defer resolution.Release()
	if len(resolution.Attachments) != 0 || len(resolution.Failures) != 1 || resolution.Failures[0].Code != "content_mismatch" {
		t.Fatalf("resolution = %+v", resolution)
	}
}

func TestCacheAllowsPartialSuccessAndDerivesImageExtensionWithoutName(t *testing.T) {
	downloader := &fixtureDownloader{fixtures: map[string]downloadFixture{
		"image": fixture("", "image/png", pngFixture()),
		"bad":   fixture("bad.txt", "text/plain", []byte{'a', 0, 'b', 1, 'c'}),
	}}
	cache := NewCache(filepath.Join(t.TempDir(), "media"), testLimits())

	resolution := cache.Resolve(context.Background(), downloader, []Ref{
		{MessageID: "m1", FileKey: "image", Kind: "image"},
		{MessageID: "m1", FileKey: "bad", Kind: "file", Name: "bad.txt"},
	})
	defer resolution.Release()
	if len(resolution.Attachments) != 1 || len(resolution.Failures) != 1 {
		t.Fatalf("resolution = %+v", resolution)
	}
	if filepath.Ext(resolution.Attachments[0].Path) != ".png" {
		t.Fatalf("derived path = %q", resolution.Attachments[0].Path)
	}
	if resolution.Failures[0].Code != "binary_text" {
		t.Fatalf("failure = %+v", resolution.Failures[0])
	}
}

func TestCacheEnforcesBatchBytesAndFileCount(t *testing.T) {
	downloader := &fixtureDownloader{fixtures: map[string]downloadFixture{
		"one":   fixture("one.txt", "text/plain", []byte("1234")),
		"two":   fixture("two.txt", "text/plain", []byte("5678")),
		"three": fixture("three.txt", "text/plain", []byte("9")),
	}}
	limits := testLimits()
	limits.MaxBatchBytes = 5
	limits.MaxFiles = 2
	cache := NewCache(filepath.Join(t.TempDir(), "media"), limits)

	resolution := cache.Resolve(context.Background(), downloader, []Ref{
		{MessageID: "m1", FileKey: "one", Kind: "file", Name: "one.txt"},
		{MessageID: "m1", FileKey: "two", Kind: "file", Name: "two.txt"},
		{MessageID: "m1", FileKey: "three", Kind: "file", Name: "three.txt"},
	})
	defer resolution.Release()
	if len(resolution.Attachments) != 1 || len(resolution.Failures) != 2 {
		t.Fatalf("resolution = %+v", resolution)
	}
	if resolution.Failures[0].Code != "batch_too_large" || resolution.Failures[1].Code != "batch_too_large" {
		t.Fatalf("failures = %+v", resolution.Failures)
	}
	if got := downloader.opens; strings.Join(got, ",") != "one,two" {
		t.Fatalf("opened resources = %v", got)
	}
	if got := cache.PendingPaths(); len(got) != 1 {
		t.Fatalf("pending paths = %v, want only accepted attachment lease", got)
	}
	resolution.Release()
	if got := cache.PendingPaths(); len(got) != 0 {
		t.Fatalf("pending paths after release = %v, want none", got)
	}
}

func TestCacheStopsOpeningResourcesAtFileLimit(t *testing.T) {
	downloader := &fixtureDownloader{fixtures: map[string]downloadFixture{
		"one": fixture("one.txt", "text/plain", []byte("1")),
		"two": fixture("two.txt", "text/plain", []byte("2")),
	}}
	limits := testLimits()
	limits.MaxFiles = 1
	cache := NewCache(filepath.Join(t.TempDir(), "media"), limits)

	resolution := cache.Resolve(context.Background(), downloader, []Ref{
		{MessageID: "m1", FileKey: "one", Kind: "file", Name: "one.txt"},
		{MessageID: "m1", FileKey: "two", Kind: "file", Name: "two.txt"},
	})
	defer resolution.Release()
	if len(resolution.Attachments) != 1 || len(resolution.Failures) != 1 || resolution.Failures[0].Code != "too_many_files" {
		t.Fatalf("resolution = %+v", resolution)
	}
	if got := downloader.opens; strings.Join(got, ",") != "one" {
		t.Fatalf("opened resources = %v", got)
	}
}

func TestCacheCountsRejectedResourcesTowardFileLimit(t *testing.T) {
	downloader := &fixtureDownloader{fixtures: map[string]downloadFixture{
		"bad":  fixture("bad.bin", "application/octet-stream", []byte("bad")),
		"good": fixture("good.txt", "text/plain", []byte("good")),
	}}
	limits := testLimits()
	limits.MaxFiles = 1
	cache := NewCache(filepath.Join(t.TempDir(), "media"), limits)

	resolution := cache.Resolve(context.Background(), downloader, []Ref{
		{MessageID: "m1", FileKey: "bad", Kind: "file", Name: "bad.bin"},
		{MessageID: "m1", FileKey: "good", Kind: "file", Name: "good.txt"},
	})
	defer resolution.Release()
	if len(resolution.Attachments) != 0 || len(resolution.Failures) != 2 || resolution.Failures[1].Code != "too_many_files" {
		t.Fatalf("resolution = %+v", resolution)
	}
	if got := downloader.opens; strings.Join(got, ",") != "bad" {
		t.Fatalf("opened resources = %v", got)
	}
}

func TestCacheRejectsBeforeDownloadWhenExistingUsageExceedsQuota(t *testing.T) {
	root := filepath.Join(t.TempDir(), "media")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "live.txt"), []byte("over quota"), 0o600); err != nil {
		t.Fatal(err)
	}
	limits := testLimits()
	limits.CacheQuotaBytes = 1
	downloader := &fixtureDownloader{fixtures: map[string]downloadFixture{
		"new": fixture("new.txt", "text/plain", []byte("new")),
	}}
	cache := NewCache(root, limits)
	gc := NewGC(cache, 72*time.Hour)
	if _, err := gc.Sweep(map[string]struct{}{filepath.Join(root, "live.txt"): {}}); err != nil {
		t.Fatal(err)
	}

	resolution := cache.Resolve(context.Background(), downloader, []Ref{
		{MessageID: "m1", FileKey: "new", Kind: "file", Name: "new.txt"},
		{MessageID: "m1", FileKey: "another", Kind: "file", Name: "another.txt"},
	})
	defer resolution.Release()
	if len(resolution.Attachments) != 0 || len(resolution.Failures) != 2 {
		t.Fatalf("resolution = %+v, want typed quota failure", resolution)
	}
	for _, failed := range resolution.Failures {
		if failed.Code != string(QuotaExceeded) {
			t.Fatalf("failure = %+v, want typed quota failure", failed)
		}
	}
	if len(downloader.opens) != 0 {
		t.Fatalf("downloader opened %v, want quota rejection before download", downloader.opens)
	}
}

type recordingReader struct {
	remaining  int64
	maxRequest int
	total      int64
}

func (r *recordingReader) Read(p []byte) (int, error) {
	if len(p) > r.maxRequest {
		r.maxRequest = len(p)
	}
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > r.remaining {
		n = r.remaining
	}
	for i := int64(0); i < n; i++ {
		p[i] = 'a'
	}
	r.remaining -= n
	r.total += n
	return int(n), nil
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode(%s) = %o, want %o", path, got, want)
	}
}

func regularFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return files
}
