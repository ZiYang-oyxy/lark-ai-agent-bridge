package media

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// GC sweeps one cache while preserving durable session references supplied by
// its caller. Callers must pass a detached snapshot, such as
// session.Manager.LiveAttachmentPaths, rather than holding a session lock.
type GC struct {
	cache     *Cache
	retention time.Duration
	// beforeDelete is a package-private deterministic test seam. Production
	// callers leave it nil.
	beforeDelete func(string)
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
	// The pending snapshot and generation are copied under a short cache lock.
	// No cache or manager mutex is held during walk, stat, or delete.
	pending, generation := g.cache.pendingSnapshot()
	live := copyPaths(durableLive)
	for path := range pending {
		live[path] = struct{}{}
	}
	return g.sweepCache(g.cache.root, g.cache.limits.CacheQuotaBytes, g.retention, live, generation)
}

type cacheFile struct {
	path    string
	size    int64
	modTime time.Time
	temp    bool
	removed bool
}

func (g *GC) sweepCache(root string, quota int64, retention time.Duration, live map[string]struct{}, generation uint64) (SweepResult, error) {
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
			if entry.temp {
				continue
			}
			if _, keep := live[entry.path]; keep || !entry.modTime.Before(cutoff) {
				continue
			}
			removed, stale, nextGeneration, err := g.removeCandidate(root, entry.path, generation)
			if err != nil {
				return result, err
			}
			if stale {
				return result, nil
			}
			generation = nextGeneration
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
			if entry.temp {
				continue
			}
			if _, keep := live[entry.path]; keep {
				continue
			}
			removed, stale, nextGeneration, err := g.removeCandidate(root, entry.path, generation)
			if err != nil {
				return result, err
			}
			if stale {
				return result, nil
			}
			generation = nextGeneration
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

func (g *GC) removeCandidate(root, path string, generation uint64) (bool, bool, uint64, error) {
	if g.beforeDelete != nil {
		g.beforeDelete(path)
	}
	claim, done := g.cache.claimDelete(path, generation)
	switch claim {
	case deleteProtected:
		return false, false, generation, nil
	case deleteStale:
		return false, true, generation, nil
	}

	// The per-path claim prevents in-process Resolve commits from leasing this
	// path until deletion finishes, without holding c.mu across Lstat/Remove.
	// Threat model: the cache root is bridge-private. A hostile local process can
	// still replace directory entries between Lstat and Remove; Remove does not
	// follow a replacement symlink, but fully eliminating that filesystem TOCTOU
	// would require dirfd/openat-style platform-specific operations.
	removed, err := removeRegularCacheFile(root, path)
	nextGeneration, unchanged := g.cache.finishDelete(path, done, generation)
	if err != nil {
		return false, !unchanged, nextGeneration, err
	}
	return removed, !unchanged, nextGeneration, nil
}

func cacheUsage(root string) (int64, error) {
	_, entries, usage, err := cacheFiles(root)
	_ = entries
	return usage, err
}

func cacheUsageAfterCommit(root, finalPath string) (int64, error) {
	_, entries, usage, err := cacheFiles(root)
	if err != nil {
		return 0, err
	}
	absoluteFinal, err := filepath.Abs(finalPath)
	if err != nil {
		return 0, err
	}
	absoluteFinal = filepath.Clean(absoluteFinal)
	for _, entry := range entries {
		if entry.path == absoluteFinal {
			usage -= entry.size
			break
		}
	}
	return usage, nil
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
		files = append(files, cacheFile{path: path, size: info.Size(), modTime: info.ModTime(), temp: isDownloadTemp(path)})
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

func isDownloadTemp(path string) bool {
	// Resolve owns cleanup of these files on every success/error path. Skipping
	// them keeps GC from racing an active stream while still counting their bytes
	// toward quota through cacheFiles.
	name := filepath.Base(path)
	return strings.HasPrefix(name, ".download-") && strings.HasSuffix(name, ".tmp")
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
