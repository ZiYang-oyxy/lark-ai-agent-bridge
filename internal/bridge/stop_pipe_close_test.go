//go:build unix

package bridge

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/bridgeinstructions"
)

// TestCLIExecRunnerStopUnblocksWhenChildIgnoresSIGTERM 复现 "点停止停不了" 的根因场景:
// 子进程忽略 SIGTERM 且不主动关 stdout,历史上 parseClaudeStream 会阻塞在 pipe read,
// 而 cmd.Wait 还没启动 → WaitDelay 的 SIGKILL 兜底进不来,整个链路卡死几十秒到几分钟
// (真实 audit 见过 111s)。修复后:ctx.Done + stopGracePeriod 到期时 Runner 主动 Close
// pipe,parseClaudeStream 立刻返回,cmd.Wait 才能进入并让 WaitDelay 强杀。
//
// 该测试用一个 shell 脚本模拟这个行为:trap 忽略 TERM,输出一行 stream JSON 让 scanner
// 已经开始读,然后 sleep 很久。取消 ctx 后 Run 必须在 ~stopGracePeriod (加余量) 内返回,
// 而不是等 30s。
func TestCLIExecRunnerStopUnblocksWhenChildIgnoresSIGTERM(t *testing.T) {
	runtime, err := bridgeinstructions.NewRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	// 脚本:模拟 Node wrapper 起 grandchild 独立持有 stdout writer 的情况。父 shell
	// 起一个 background subshell,该 subshell 继承 stdout FD 并 sleep 300s;父 shell
	// 输出一行 stream 后自己 exec 一个 sleep 也继续持有 stdout。SIGTERM 打进 pgroup
	// 时 sleep 会被杀,但 shell 主进程 fork 出去的 grandchild 已经 reparent 到 init
	// (setsid 隔离)、且仍持有 pipe writer,导致 parseClaudeStream 的 scanner 永远读
	// 不到 EOF。这就是历史注释里 "grandchild reparent 到 init 后 scanner 阻塞" 的形态。
	script := `#!/bin/sh
# 用 setsid 让 background subshell 逃出 pgroup,即使 kill(-pgid) 也打不到它。
setsid sh -c 'sleep 300' >&1 2>&1 &
printf '%s\n' '{"type":"assistant","message":{"model":"fake","content":[{"type":"text","text":"streaming..."}]}}'
# 父进程也 sleep,拿到 SIGTERM 就退。但 background subshell 仍持有 stdout writer。
sleep 300
`
	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// 起子进程后,给它 300ms 输出首行 stream,再触发 cancel 模拟用户点停止。
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, _ = (CLIExecRunner{Instructions: runtime}).Run(ctx, AgentRunRequest{
		Kind:                      agent.Claude,
		Bin:                       bin,
		Prompt:                    "hang",
		BridgeInstructionsVersion: bridgeinstructions.CurrentVersion,
	})
	elapsed := time.Since(start)

	// 上限 = 输出等待 300ms + stopGracePeriod(pipe close 兜底) + WaitDelay(SIGKILL 兜底) + 余量。
	// 修复前会跑满 sleep 300 秒,这里 20s 内必须返回。
	limit := 300*time.Millisecond + stopGracePeriod + stopGracePeriod + 10*time.Second
	if elapsed > limit {
		t.Fatalf("Run took %s, want <= %s (stop-cancel fallback not effective)", elapsed, limit)
	}
}
