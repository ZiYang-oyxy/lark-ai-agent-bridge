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
)

// Usage is the normalized context-window occupancy for one session. OK is false
// whenever the file is missing, unreadable, mismatched, or carries no usable
// percentage — callers must fall back to their prior display in that case.
type Usage struct {
	OK            bool
	UsedPercent   int
	TotalTokens   int
	ContextWindow int
	Reason        Reason
}

type Reason string

const (
	ReasonMissing  Reason = "missing"
	ReasonInvalid  Reason = "invalid"
	ReasonMismatch Reason = "mismatch"
	ReasonEmpty    Reason = "empty"
)

type record struct {
	SessionID      string   `json:"session_id"`
	UsedPercentage *float64 `json:"used_percentage"`
	TotalTokens    *int     `json:"total_tokens"`
	ContextTokens  *int     `json:"context_tokens"`
	ContextWindow  *int     `json:"context_window_size"`
}

// Read locates <dir>/<sessionID>.json, validates its session_id, and returns a
// normalized Usage. Any failure or all-null usage yields Usage{OK: false}.
func Read(dir, sessionID string) Usage {
	dir = strings.TrimSpace(dir)
	sessionID = strings.TrimSpace(sessionID)
	if dir == "" || sessionID == "" {
		return Usage{}
	}
	if sessionID != filepath.Base(sessionID) || strings.ContainsRune(sessionID, filepath.Separator) {
		return Usage{}
	}
	raw, err := os.ReadFile(filepath.Join(dir, sessionID+".json"))
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
	u := Usage{}
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
		return Usage{Reason: ReasonEmpty}
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
