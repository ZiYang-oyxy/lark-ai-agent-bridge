package media

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// GC sweeps one cache while preserving durable session references supplied by
// its caller. Callers must pass a detached snapshot, such as
// session.Manager.LiveAttachmentPaths, rather than holding a session lock.
type GC struct {
	cache     *Cache
	retention time.Duration
	mu        sync.Mutex
}

// SweepResult reports the observable storage effect of one sweep.
type SweepResult struct {
	RemovedFiles int
	RemovedBytes int64
	UsageBytes   int64
}

func NewGC(cache *Cache, retention time.Duration) *GC {
	return &GC{cache: cache, retention: retention}
}

// Sweep deletes only unreferenced regular files inside the cache root. It
// first applies retention and then evicts remaining unreferenced files oldest
// first until the cache quota is met. Live and pending paths are never deleted.
func (g *GC) Sweep(durableLive map[string]struct{}) (SweepResult, error) {
	if g == nil || g.cache == nil {
		return SweepResult{}, fmt.Errorf("media gc is not configured")
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	// Coordinate with Resolve so a just-renamed file obtains its pending lease
	// before this sweep can observe it. c.mu is held only by PendingPaths while
	// copying the set, never during the subsequent filesystem walk.
	g.cache.ioMu.Lock()
	defer g.cache.ioMu.Unlock()
	live := copyPaths(durableLive)
	for path := range g.cache.PendingPaths() {
		live[path] = struct{}{}
	}
	return sweepCache(g.cache.root, g.cache.limits.CacheQuotaBytes, g.retention, live)
}

type cacheFile struct {
	path    string
	size    int64
	modTime time.Time
	removed bool
}

func sweepCache(root string, quota int64, retention time.Duration, live map[string]struct{}) (SweepResult, error) {
	root, entries, usage, err := cacheFiles(root)
	if err != nil {
		return SweepResult{}, err
	}
	live = absolutePaths(live)
	result := SweepResult{UsageBytes: usage}

	if retention > 0 {
		cutoff := time.Now().Add(-retention)
		for i := range entries {
			entry := &entries[i]
			if _, keep := live[entry.path]; keep || !entry.modTime.Before(cutoff) {
				continue
			}
			removed, err := removeRegularCacheFile(root, entry.path)
			if err != nil {
				return result, err
			}
			if removed {
				entry.removed = true
				result.RemovedFiles++
				result.RemovedBytes += entry.size
				result.UsageBytes -= entry.size
			}
		}
	}

	if quota > 0 && result.UsageBytes > quota {
		for i := range entries {
			entry := &entries[i]
			if result.UsageBytes <= quota {
				break
			}
			if entry.removed {
				continue
			}
			if _, keep := live[entry.path]; keep {
				continue
			}
			removed, err := removeRegularCacheFile(root, entry.path)
			if err != nil {
				return result, err
			}
			if removed {
				entry.removed = true
				result.RemovedFiles++
				result.RemovedBytes += entry.size
				result.UsageBytes -= entry.size
			}
		}
	}
	return result, nil
}

func cacheUsage(root string) (int64, error) {
	_, entries, usage, err := cacheFiles(root)
	_ = entries
	return usage, err
}

func cacheFiles(root string) (string, []cacheFile, int64, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", nil, 0, err
	}
	info, err := os.Lstat(absoluteRoot)
	if os.IsNotExist(err) {
		return absoluteRoot, nil, 0, nil
	}
	if err != nil {
		return "", nil, 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", nil, 0, fmt.Errorf("unsafe media cache root: %s", absoluteRoot)
	}

	var files []cacheFile
	var usage int64
	err = filepath.WalkDir(absoluteRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == absoluteRoot || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !pathInsideRoot(absoluteRoot, path) {
			return fmt.Errorf("media cache walk escaped root: %s", path)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		files = append(files, cacheFile{path: path, size: info.Size(), modTime: info.ModTime()})
		usage += info.Size()
		return nil
	})
	if err != nil {
		return "", nil, 0, err
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].modTime.Equal(files[j].modTime) {
			return files[i].path < files[j].path
		}
		return files[i].modTime.Before(files[j].modTime)
	})
	return absoluteRoot, files, usage, nil
}

func removeRegularCacheFile(root, path string) (bool, error) {
	if !pathInsideRoot(root, path) {
		return false, fmt.Errorf("refusing to remove outside cache root: %s", path)
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}

func pathInsideRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func copyPaths(paths map[string]struct{}) map[string]struct{} {
	result := make(map[string]struct{}, len(paths))
	for path := range paths {
		result[path] = struct{}{}
	}
	return result
}

func absolutePaths(paths map[string]struct{}) map[string]struct{} {
	result := make(map[string]struct{}, len(paths))
	for path := range paths {
		if absolute, err := filepath.Abs(path); err == nil {
			result[filepath.Clean(absolute)] = struct{}{}
		}
	}
	return result
}
