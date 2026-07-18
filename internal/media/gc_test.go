package media

import (
	"bytes"
	"context"
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
	cache := NewCache(root, Limits{MaxFileBytes: 1024, CacheQuotaBytes: 8192})
	downloader := &blockingGCDownloader{opened: make(chan struct{}), release: make(chan struct{})}
	resolutionDone := make(chan Resolution, 1)
	go func() {
		resolutionDone <- cache.Resolve(context.Background(), downloader, []Ref{{MessageID: "m1", FileKey: "image", Kind: "image", Name: "image.png"}})
	}()
	<-downloader.opened

	sweepDone := make(chan error, 1)
	go func() {
		_, err := NewGC(cache, time.Nanosecond).Sweep(nil)
		sweepDone <- err
	}()
	select {
	case err := <-sweepDone:
		t.Fatalf("sweep completed before Resolve obtained a pending lease: %v", err)
	default:
	}
	close(downloader.release)
	resolution := <-resolutionDone
	defer resolution.Release()
	if len(resolution.Attachments) != 1 || len(resolution.Failures) != 0 {
		t.Fatalf("resolution = %+v", resolution)
	}
	if err := <-sweepDone; err != nil {
		t.Fatal(err)
	}
	assertGCPresent(t, resolution.Attachments[0].Path)
}

type blockingGCDownloader struct {
	opened  chan struct{}
	release chan struct{}
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
