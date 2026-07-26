package bridge

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// codexTranscriptModel reads the runtime model recorded by this exact Codex
// session. exec --json does not expose it, but Codex writes it before the turn
// begins in its session transcript.
func codexTranscriptModel(home, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	home = strings.TrimSpace(home)
	if home == "" {
		home = strings.TrimSpace(os.Getenv("CODEX_HOME"))
	}
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		home = filepath.Join(userHome, ".codex")
	}

	var model string
	_ = filepath.WalkDir(filepath.Join(home, "sessions"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.Contains(entry.Name(), sessionID) || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		model = codexTranscriptFileModel(path)
		if model != "" {
			return fs.SkipAll
		}
		return nil
	})
	return model
}

func codexTranscriptFileModel(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var model string
	for scanner.Scan() {
		var record struct {
			Type    string         `json:"type"`
			Payload map[string]any `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			continue
		}
		if next := codexTranscriptRecordModel(record.Type, record.Payload); next != "" {
			model = next
		}
	}
	return model
}

func codexTranscriptRecordModel(recordType string, payload map[string]any) string {
	if payload == nil {
		return ""
	}
	switch recordType {
	case "turn_context":
		return codexString(payload["model"])
	case "event_msg":
		if codexString(payload["type"]) != "thread_settings_applied" {
			return ""
		}
		return codexString(codexRecord(payload["thread_settings"])["model"])
	default:
		return ""
	}
}
