package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGCSweepDeletesOnlyExpiredUnreferencedRegularFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "media")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	old := writeGCFile(t, root, "old.txt", "old", time.Now().Add(-73*time.Hour))
	live := writeGCFile(t, root, "live.txt", "live", time.Now().Add(-73*time.Hour))
	recent := writeGCFile(t, root, "recent.txt", "recent", time.Now().Add(-time.Hour))
	outside := writeGCFile(t, t.TempDir(), "outside.txt", "outside", time.Now().Add(-73*time.Hour))
	if err := os.Symlink(outside, filepath.Join(root, "outside-link")); err != nil {
		t.Fatal(err)
	}

	cache := NewCache(root, Limits{CacheQuotaBytes: 1024})
	if _, err := NewGC(cache, 72*time.Hour).Sweep(map[string]struct{}{live: {}}); err != nil {
		t.Fatal(err)
	}
	assertGCAbsent(t, old)
	assertGCPresent(t, live)
	assertGCPresent(t, recent)
	assertGCPresent(t, outside)
	if info, err := os.Lstat(filepath.Join(root, "outside-link")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink = %#v, err=%v; want untouched symlink", info, err)
	}
}

func TestGCSweepEvictsOldestUnreferencedFilesForQuota(t *testing.T) {
	root := filepath.Join(t.TempDir(), "media")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	oldest := writeGCFile(t, root, "oldest.txt", "1111", time.Now().Add(-3*time.Hour))
	newer := writeGCFile(t, root, "newer.txt", "2222", time.Now().Add(-2*time.Hour))
	live := writeGCFile(t, root, "live.txt", "3333", time.Now().Add(-time.Hour))

	cache := NewCache(root, Limits{CacheQuotaBytes: 8})
	if _, err := NewGC(cache, 7*24*time.Hour).Sweep(map[string]struct{}{live: {}}); err != nil {
		t.Fatal(err)
	}
	assertGCAbsent(t, oldest)
	assertGCPresent(t, newer)
	assertGCPresent(t, live)
}

func TestGCSweepPreservesPendingLeaseDuringConcurrentResolve(t *testing.T) {
	root := filepath.Join(t.TempDir(), "media")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	data := pngFixture()
	digest := sha256.Sum256(data)
	finalPath := filepath.Join(root, hex.EncodeToString(digest[:])+".png")
	if err := os.WriteFile(finalPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(finalPath, old, old); err != nil {
		t.Fatal(err)
	}
	cache := NewCache(root, Limits{MaxFileBytes: 1024, CacheQuotaBytes: 8192})
	downloader := &blockingGCDownloader{opened: make(chan struct{}), release: make(chan struct{})}
	resolutionDone := make(chan Resolution, 1)
	go func() {
		resolutionDone <- cache.Resolve(context.Background(), downloader, []Ref{{MessageID: "m1", FileKey: "image", Kind: "image", Name: "image.png"}})
	}()
	<-downloader.opened

	atCandidate := make(chan struct{})
	allowDeleteCheck := make(chan struct{})
	gc := NewGC(cache, time.Nanosecond)
	gc.beforeDelete = func(path string) {
		if path != finalPath {
			return
		}
		close(atCandidate)
		<-allowDeleteCheck
	}
	sweepDone := make(chan error, 1)
	go func() {
		_, err := gc.Sweep(nil)
		sweepDone <- err
	}()
	<-atCandidate

	// The download and commit must proceed while GC is paused at the candidate,
	// proving GC does not hold a cache-wide mutex across its filesystem work.
	close(downloader.release)
	resolution := <-resolutionDone
	defer resolution.Release()
	if len(resolution.Attachments) != 1 || len(resolution.Failures) != 0 {
		t.Fatalf("resolution = %+v", resolution)
	}
	close(allowDeleteCheck)
	if err := <-sweepDone; err != nil {
		t.Fatal(err)
	}
	assertGCPresent(t, finalPath)
}

func TestGCSweepDoesNotDeleteInProgressDownloadTempFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "media")
	data := append(pngFixture(), bytes.Repeat([]byte{0}, 512-len(pngFixture()))...)
	body := &tempBlockingReader{first: data, blocked: make(chan struct{}), release: make(chan struct{})}
	downloader := &fixtureDownloader{fixtures: map[string]downloadFixture{
		"image": {name: "image.png", contentType: "image/png", body: io.NopCloser(body)},
	}}
	cache := NewCache(root, Limits{MaxFileBytes: 1024, CacheQuotaBytes: 8192})
	resolutionDone := make(chan Resolution, 1)
	go func() {
		resolutionDone <- cache.Resolve(context.Background(), downloader, []Ref{{MessageID: "m1", FileKey: "image", Kind: "image", Name: "image.png"}})
	}()
	<-body.blocked

	sweepDone := make(chan error, 1)
	go func() {
		_, err := NewGC(cache, time.Nanosecond).Sweep(nil)
		sweepDone <- err
	}()
	select {
	case err := <-sweepDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("sweep blocked behind in-progress download")
	}
	if temps, err := filepath.Glob(filepath.Join(root, ".download-*.tmp")); err != nil || len(temps) != 1 {
		t.Fatalf("download temp files = %v, err=%v; want one protected temp", temps, err)
	}

	close(body.release)
	resolution := <-resolutionDone
	defer resolution.Release()
	if len(resolution.Attachments) != 1 || len(resolution.Failures) != 0 {
		t.Fatalf("resolution = %+v", resolution)
	}
}

func TestGCSweepHonorsSameHashPendingRefcounts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "media")
	cache := NewCache(root, Limits{MaxFileBytes: 1024, CacheQuotaBytes: 8192})
	downloader := freshGCDownloader{data: pngFixture()}
	ref := Ref{MessageID: "m1", FileKey: "image", Kind: "image", Name: "image.png"}
	first := cache.Resolve(context.Background(), downloader, []Ref{ref})
	second := cache.Resolve(context.Background(), downloader, []Ref{ref})
	if len(first.Attachments) != 1 || len(second.Attachments) != 1 || first.Attachments[0].Path != second.Attachments[0].Path {
		t.Fatalf("same-hash resolutions = %+v / %+v", first, second)
	}
	path := first.Attachments[0].Path
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	first.Release()
	if _, err := NewGC(cache, time.Nanosecond).Sweep(nil); err != nil {
		t.Fatal(err)
	}
	assertGCPresent(t, path)
	second.Release()
	if _, err := NewGC(cache, time.Nanosecond).Sweep(nil); err != nil {
		t.Fatal(err)
	}
	assertGCAbsent(t, path)
}

func TestGCSweepRejectsSymlinkRootWithoutTouchingTarget(t *testing.T) {
	target := t.TempDir()
	targetFile := writeGCFile(t, target, "keep.txt", "keep", time.Now().Add(-time.Hour))
	root := filepath.Join(t.TempDir(), "media")
	if err := os.Symlink(target, root); err != nil {
		t.Fatal(err)
	}

	if _, err := NewGC(NewCache(root, Limits{CacheQuotaBytes: 1}), time.Nanosecond).Sweep(nil); err == nil {
		t.Fatal("Sweep() error = nil, want unsafe symlink root rejection")
	}
	assertGCPresent(t, targetFile)
}

func TestGCSweepRejectsRegularFileRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "media")
	if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := NewGC(NewCache(root, Limits{CacheQuotaBytes: 1}), time.Nanosecond).Sweep(nil); err == nil {
		t.Fatal("Sweep() error = nil, want regular-file root rejection")
	}
}

type tempBlockingReader struct {
	first   []byte
	sent    bool
	blocked chan struct{}
	release chan struct{}
}

func (r *tempBlockingReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, r.first), nil
	}
	close(r.blocked)
	<-r.release
	return 0, io.EOF
}

type blockingGCDownloader struct {
	opened  chan struct{}
	release chan struct{}
}

type freshGCDownloader struct{ data []byte }

func (d freshGCDownloader) Open(context.Context, Ref) (Download, error) {
	return Download{Body: io.NopCloser(bytes.NewReader(d.data)), Name: "image.png", ContentType: "image/png"}, nil
}

func (d *blockingGCDownloader) Open(context.Context, Ref) (Download, error) {
	close(d.opened)
	<-d.release
	return Download{Body: io.NopCloser(bytes.NewReader(pngFixture())), Name: "image.png", ContentType: "image/png"}, nil
}

func writeGCFile(t *testing.T, dir, name, content string, modTime time.Time) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertGCPresent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("path %s missing: %v", path, err)
	}
}

func assertGCAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("path %s err = %v, want absent", path, err)
	}
}
