// Package contextusage reads the per-session context-window usage that Claude
// Code and Codex export as local JSON files (see lark-agent-workspace
// config/ai/docs/context-usage.md). It is read-only and side-effect free.
package contextusage

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Usage is the normalized context-window occupancy for one session. OK is false
// whenever the file is missing, unreadable, mismatched, or carries no usable
// percentage — callers must fall back to their prior display in that case.
type Usage struct {
	OK            bool
	UsedPercent   int
	TotalTokens   int
	ContextWindow int
	Model         string
	Reason        Reason
}

type Reason string

const (
	ReasonMissing  Reason = "missing"
	ReasonInvalid  Reason = "invalid"
	ReasonMismatch Reason = "mismatch"
	ReasonEmpty    Reason = "empty"
	ReasonStale    Reason = "stale"
)

type record struct {
	SessionID      string   `json:"session_id"`
	UsedPercentage *float64 `json:"used_percentage"`
	TotalTokens    *int     `json:"total_tokens"`
	ContextTokens  *int     `json:"context_tokens"`
	ContextWindow  *int     `json:"context_window_size"`
	Model          string   `json:"model"`
	CWD            string   `json:"cwd"`
	UpdatedAt      *int64   `json:"updated_at"`
}

// Read locates <dir>/<sessionID>.json, validates its session_id, and returns a
// normalized Usage. Any failure or all-null usage yields Usage{OK: false}.
func Read(dir, sessionID string) Usage {
	return read(dir, sessionID, time.Time{})
}

// ReadAfter is Read with a freshness requirement for terminal run metadata.
// Millisecond precision matches the workspace sidecar contract.
func ReadAfter(dir, sessionID string, notBefore time.Time) Usage {
	return read(dir, sessionID, notBefore)
}

// ReadLatestForWorkDir returns the newest valid record whose canonical cwd
// matches workDir. It is the sidecar contract's fallback for the brief period
// before a new runtime session ID is known; callers must present it as an
// approximate value until an exact-session record becomes available.
func ReadLatestForWorkDir(dir, workDir string) Usage {
	dir = strings.TrimSpace(dir)
	wantCWD := canonicalWorkDir(workDir)
	if dir == "" || wantCWD == "" {
		return Usage{}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Usage{Reason: ReasonMissing}
	}
	var (
		latest   Usage
		latestAt int64
	)
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var rec record
		if json.Unmarshal(raw, &rec) != nil || canonicalWorkDir(rec.CWD) != wantCWD {
			continue
		}
		sessionID := strings.TrimSpace(rec.SessionID)
		if sessionID == "" || entry.Name() != sessionID+".json" {
			continue
		}
		u := read(dir, sessionID, time.Time{})
		if !u.OK {
			continue
		}
		updatedAt := info.ModTime().UnixMilli()
		if rec.UpdatedAt != nil {
			updatedAt = *rec.UpdatedAt
		}
		if latest.OK && updatedAt <= latestAt {
			continue
		}
		latest = u
		latestAt = updatedAt
	}
	if !latest.OK {
		return Usage{Reason: ReasonMissing}
	}
	return latest
}

func canonicalWorkDir(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved)
	}
	return abs
}

func read(dir, sessionID string, notBefore time.Time) Usage {
	dir = strings.TrimSpace(dir)
	sessionID = strings.TrimSpace(sessionID)
	if dir == "" || sessionID == "" {
		return Usage{}
	}
	if sessionID != filepath.Base(sessionID) || strings.ContainsRune(sessionID, filepath.Separator) {
		return Usage{}
	}
	path := filepath.Join(dir, sessionID+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Usage{Reason: ReasonMissing}
		}
		return Usage{Reason: ReasonInvalid}
	}
	var rec record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return Usage{Reason: ReasonInvalid}
	}
	if strings.TrimSpace(rec.SessionID) != sessionID {
		return Usage{Reason: ReasonMismatch}
	}
	model := strings.TrimSpace(rec.Model)
	if !notBefore.IsZero() {
		updatedAt := int64(0)
		if rec.UpdatedAt != nil {
			updatedAt = *rec.UpdatedAt
		} else if info, statErr := os.Stat(path); statErr == nil {
			updatedAt = info.ModTime().UnixMilli()
		}
		if updatedAt < notBefore.UnixMilli() {
			return Usage{Model: model, Reason: ReasonStale}
		}
	}
	u := Usage{Model: model}
	if rec.ContextTokens != nil && *rec.ContextTokens > 0 {
		u.TotalTokens = *rec.ContextTokens
	} else if rec.TotalTokens != nil && *rec.TotalTokens > 0 {
		u.TotalTokens = *rec.TotalTokens
	}
	if rec.ContextWindow != nil && *rec.ContextWindow > 0 {
		u.ContextWindow = *rec.ContextWindow
	}
	switch {
	case rec.UsedPercentage != nil:
		u.UsedPercent = clampPercent(int(*rec.UsedPercentage))
		u.OK = true
	case u.TotalTokens > 0 && u.ContextWindow > 0:
		u.UsedPercent = clampPercent(u.TotalTokens * 100 / u.ContextWindow)
		u.OK = true
	default:
		return Usage{Model: model, Reason: ReasonEmpty}
	}
	return u
}

func clampPercent(n int) int {
	if n < 0 {
		return 0
	}
	if n > 100 {
		return 100
	}
	return n
}
