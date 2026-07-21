package workspace

import (
	"os"
	"path/filepath"
	"strings"
)

// Resolution is the discriminated result of ResolveWorkingDirectory.
type Resolution struct {
	OK          bool   // format + blacklist passed
	Exists      bool   // realpath resolved to an existing directory
	Realpath    string // resolved absolute path (set when Exists); else the cleaned abs path
	UserVisible string // Chinese message when !OK
}

// ResolveWorkingDirectory validates a user-supplied path against format rules
// and a high-risk blacklist, expanding ~ against home. It does NOT reject a
// well-formed, non-blacklisted path that merely doesn't exist yet — that case
// returns OK=true, Exists=false so the caller can offer the create-confirm card.
func ResolveWorkingDirectory(input, home string) Resolution {
	input = strings.TrimSpace(input)
	if input == "" {
		return Resolution{UserVisible: "路径不能为空。"}
	}
	expanded := input
	if input == "~" || strings.HasPrefix(input, "~/") {
		expanded = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(input, "~"), "/"))
	} else if !filepath.IsAbs(input) {
		return Resolution{UserVisible: "请使用绝对路径或 ~ 开头的路径。"}
	}
	cleaned := filepath.Clean(expanded)

	// realpath if it exists; otherwise resolve the longest existing ancestor and
	// re-append the missing tail. This keeps the blacklist comparison on a
	// symlink-resolved footing even for not-yet-created paths (e.g. on macOS
	// "/tmp/new" must still be seen as a child of "/private/tmp").
	real := cleaned
	exists := false
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		real = resolved
		if info, statErr := os.Stat(real); statErr == nil {
			if !info.IsDir() {
				return Resolution{UserVisible: "目标不是目录。"}
			}
			exists = true
		}
	} else {
		real = resolveExistingPrefix(cleaned)
	}

	if reason := classifyHighRisk(real, home); reason != "" {
		return Resolution{UserVisible: reason}
	}
	return Resolution{OK: true, Exists: exists, Realpath: real}
}

// resolveExistingPrefix walks up p to the longest ancestor that exists,
// resolves that ancestor through symlinks, and re-joins the missing tail. This
// yields a canonical prefix for paths that do not yet exist, so blacklist
// checks are not defeated by an unresolved symlinked parent (e.g. /tmp/new when
// /tmp -> /private/tmp).
func resolveExistingPrefix(p string) string {
	tail := ""
	cur := p
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			if tail == "" {
				return resolved
			}
			return filepath.Join(resolved, tail)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// reached the root without finding an existing ancestor
			return filepath.Clean(p)
		}
		tail = filepath.Join(filepath.Base(cur), tail)
		cur = parent
	}
}

// canonical resolves p through symlinks so that comparisons happen on the same
// footing as the realpath produced by ResolveWorkingDirectory. On macOS several
// blacklist entries are themselves symlinks (/etc -> /private/etc, /tmp ->
// /private/tmp, /var -> /private/var); without this normalization a user-typed
// "/etc" would resolve to "/private/etc" and slip past a literal "/etc" entry.
// If p cannot be resolved (e.g. does not exist), the cleaned form is returned.
func canonical(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return filepath.Clean(p)
}

// classifyHighRisk returns a non-empty Chinese reason when path is too broad or
// dangerous to use as a working directory. Empty string means allowed. path is
// expected to already be a realpath (or cleaned abs path when nonexistent);
// blacklist entries are canonicalized the same way before comparison.
func classifyHighRisk(path, home string) string {
	// filesystem root
	if path == filepath.Dir(path) {
		return "拒绝：不能使用文件系统根目录。"
	}
	home = canonical(home)
	if path == home {
		return "拒绝：不能直接使用 home 根目录。"
	}
	if path == filepath.Dir(home) {
		return "拒绝：不能使用用户目录的父目录。"
	}
	for _, sub := range []string{"Desktop", "Downloads"} {
		if path == canonical(filepath.Join(home, sub)) {
			return "拒绝：范围过大的目录（Desktop/Downloads）。"
		}
	}
	for _, t := range []string{"/tmp", "/private/tmp", os.TempDir()} {
		if path == canonical(t) {
			return "拒绝：不能使用临时目录根。"
		}
	}
	systemDirs := []string{"/bin", "/etc", "/usr", "/var", "/System", "/Library", "/Applications", "/private", "/sbin"}
	for _, d := range systemDirs {
		if path == canonical(d) {
			return "拒绝：不能使用系统目录。"
		}
	}
	// /Volumes and its direct children (e.g. /Volumes/foo) are too broad.
	if path == "/Volumes" || filepath.Dir(path) == "/Volumes" {
		return "拒绝：不能使用卷根目录。"
	}
	return ""
}
