package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/buildinfo"
	"lark-agent-bridge/internal/card"
)

type codexRPCMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *codexRPCError  `json:"error"`
}

type codexRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func runCodexCompact(ctx context.Context, req AgentRunRequest, bin string) (AgentRunResult, error) {
	threadID := strings.TrimSpace(req.AgentSessionID)
	cmd := exec.CommandContext(ctx, bin, "app-server", "--listen", "stdio://")
	configureProcessGroup(cmd)
	env := agent.AgentEnv(agent.Codex, req.Home)
	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
		cmd.Env = childEnv(req.WorkDir, env)
	} else if len(env) > 0 {
		cmd.Env = childEnv("", env)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return AgentRunResult{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return AgentRunResult{}, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return AgentRunResult{}, err
	}

	enc := json.NewEncoder(stdin)
	dec := json.NewDecoder(stdout)
	fail := func(cause error) (AgentRunResult, error) {
		_ = stdin.Close()
		_ = cmd.Wait()
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return AgentRunResult{}, fmt.Errorf("codex compact: %w: %s", cause, detail)
		}
		return AgentRunResult{}, fmt.Errorf("codex compact: %w", cause)
	}

	version := strings.TrimSpace(buildinfo.Version)
	if version == "" {
		version = "dev"
	}
	if err := enc.Encode(map[string]any{
		"method": "initialize", "id": 1,
		"params": map[string]any{"clientInfo": map[string]string{"name": "lark-ai-agent-bridge", "title": "Lark AI Agent Bridge", "version": version}},
	}); err != nil {
		return fail(err)
	}
	if err := waitCodexRPCResponse(dec, 1); err != nil {
		return fail(err)
	}
	if err := enc.Encode(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return fail(err)
	}
	if err := enc.Encode(map[string]any{"method": "thread/resume", "id": 2, "params": map[string]string{"threadId": threadID}}); err != nil {
		return fail(err)
	}
	if err := waitCodexRPCResponse(dec, 2); err != nil {
		return fail(err)
	}
	if err := enc.Encode(map[string]any{"method": "thread/compact/start", "id": 3, "params": map[string]string{"threadId": threadID}}); err != nil {
		return fail(err)
	}
	if err := waitCodexCompactCompletion(dec); err != nil {
		return fail(err)
	}
	if err := stdin.Close(); err != nil {
		return fail(err)
	}
	if err := cmd.Wait(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return AgentRunResult{}, fmt.Errorf("codex compact process: %w: %s", err, detail)
		}
		return AgentRunResult{}, fmt.Errorf("codex compact process: %w", err)
	}
	return AgentRunResult{
		AgentSessionID: threadID,
		Segments:       []card.Segment{{Kind: card.SegmentText, Text: "Codex 上下文压缩完成。"}},
	}, nil
}

func waitCodexRPCResponse(dec *json.Decoder, id int) error {
	for {
		msg, err := decodeCodexRPCMessage(dec)
		if err != nil {
			return err
		}
		messageID, ok := codexRPCMessageID(msg.ID)
		if !ok || messageID != id {
			continue
		}
		if msg.Error != nil {
			return fmt.Errorf("rpc %d failed (%d): %s", id, msg.Error.Code, msg.Error.Message)
		}
		return nil
	}
}

func waitCodexCompactCompletion(dec *json.Decoder) error {
	accepted := false
	itemCompleted := false
	for {
		msg, err := decodeCodexRPCMessage(dec)
		if err != nil {
			return err
		}
		if messageID, ok := codexRPCMessageID(msg.ID); ok && messageID == 3 {
			if msg.Error != nil {
				return fmt.Errorf("compact request failed (%d): %s", msg.Error.Code, msg.Error.Message)
			}
			accepted = true
			continue
		}
		switch msg.Method {
		case "item/completed":
			var params struct {
				Item struct {
					Type string `json:"type"`
				} `json:"item"`
			}
			if json.Unmarshal(msg.Params, &params) == nil && params.Item.Type == "contextCompaction" {
				itemCompleted = true
			}
		case "turn/completed":
			var params struct {
				Turn struct {
					Status string `json:"status"`
					Error  any    `json:"error"`
				} `json:"turn"`
			}
			if err := json.Unmarshal(msg.Params, &params); err != nil {
				return fmt.Errorf("decode compact completion: %w", err)
			}
			if params.Turn.Status != "completed" {
				return fmt.Errorf("compact turn ended with status %q", params.Turn.Status)
			}
			if !accepted || !itemCompleted {
				return errors.New("compact turn completed without accepted request and contextCompaction item")
			}
			return nil
		}
	}
}

func decodeCodexRPCMessage(dec *json.Decoder) (codexRPCMessage, error) {
	var msg codexRPCMessage
	if err := dec.Decode(&msg); err != nil {
		if errors.Is(err, io.EOF) {
			return msg, errors.New("app-server stream ended before compact completion")
		}
		return msg, fmt.Errorf("decode app-server message: %w", err)
	}
	return msg, nil
}

func codexRPCMessageID(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var id int
	if err := json.Unmarshal(raw, &id); err != nil {
		return 0, false
	}
	return id, true
}
