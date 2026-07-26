package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/session"
)

func TestRunChecksRuntimeConfig(t *testing.T) {
	workDir := canonicalTempDir(t)
	auditPath := filepath.Join(workDir, ".lark-agent-bridge", "audit.jsonl")
	storePath := filepath.Join(workDir, ".lark-agent-bridge", "sessions.json")
	preferencePath := filepath.Join(workDir, ".lark-agent-bridge", "preferences.json")
	replyPath := filepath.Join(workDir, ".lark-agent-bridge", "replies.json")
	cachePath := filepath.Join(workDir, ".lark-agent-bridge", "media")
	t.Setenv("E2E_CALLBACK_ADDR", ":18080")

	checks := Run(config.Config{
		DefaultAgent:        "claude",
		DefaultWorkDir:      workDir,
		CardUpdateEvery:     time.Second,
		CardHeartbeatEvery:  15 * time.Second,
		InteractionTimeout:  2 * time.Second,
		CardMaxChars:        12000,
		CardMinDeltaChars:   30,
		CardPreviewMaxChars: 2000,
		AuditLogPath:        auditPath,
		SessionStorePath:    storePath,
		PreferenceStorePath: preferencePath,
		ReplyStorePath:      replyPath,
		Model:               "default",
		Effort:              "low",
		AllowedModels:       []string{"default", "sonnet", "opus", "haiku"},
		MediaCacheDir:       cachePath,
	})

	assertCheck(t, checks, "default_agent", true, "claude")
	assertCheck(t, checks, "default_workdir", true, workDir)
	assertCheck(t, checks, "audit_log", true, auditPath)
	assertCheck(t, checks, "session_store", true, storePath)
	assertCheck(t, checks, "preference_store", true, preferencePath)
	assertCheck(t, checks, "reply_store", true, replyPath)
	assertCheck(t, checks, "media_cache", true, cachePath)
	assertCheck(t, checks, "E2E_CALLBACK_ADDR", true, ":18080")
	assertCheck(t, checks, "card_update_every", true, "1s")
	assertCheck(t, checks, "card_heartbeat_every", true, "15s")
	assertCheck(t, checks, "interaction_timeout", true, "2s")
	assertCheck(t, checks, "card_max_chars", true, "12000")
	assertCheck(t, checks, "card_min_delta_chars", true, "30")
	assertCheck(t, checks, "card_preview_max_chars", true, "2000")

	if _, err := os.Stat(auditPath); err != nil {
		t.Fatalf("audit log was not created: %v", err)
	}
	info, err := os.Stat(filepath.Dir(storePath))
	if err != nil {
		t.Fatalf("session store parent was not created: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("session store parent permissions = %o, want 700", got)
	}
	if _, err := os.Stat(storePath); !os.IsNotExist(err) {
		t.Fatalf("session store probe unexpectedly created store: %v", err)
	}
	if probes, err := filepath.Glob(filepath.Join(cachePath, ".media-cache-probe-*")); err != nil || len(probes) != 0 {
		t.Fatalf("media cache probe files = %q, err = %v; want none", probes, err)
	}
}

func TestAgentsConfigCheckReportsCodexConfigurationOwnership(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".lark-agent-bridge", "agents.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	doc := `{"schema_version":1,"agents":[{"kind":"claude"},{"kind":"codex","bins":[{"label":"cx3","path":"bin/cx3"}]}]}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	check := agentsConfigCheck(config.Config{AgentsConfigPath: path})
	if !check.OK || check.Warning || !strings.Contains(check.Detail, "claude,codex") || !strings.Contains(check.Detail, "executable configuration") {
		t.Fatalf("check = %#v", check)
	}
}

func TestCodexDeveloperInstructionsCheckRejectsTopLevelConflictsWithoutLeakingValues(t *testing.T) {
	for name, content := range map[string]string{
		"basic":     `developer_instructions = "PRIVATE_PERSONA"`,
		"literal":   `developer_instructions = '''PRIVATE_PERSONA'''`,
		"multiline": "developer_instructions = \"\"\"\nPRIVATE_PERSONA\n\"\"\"",
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg := config.Config{AgentsConfigPath: writeCodexAgentsConfig(t, home)}
			check := codexDeveloperInstructionsCheck(cfg)
			if check.OK || check.Warning || !strings.Contains(check.Detail, "conflict") {
				t.Fatalf("check = %#v", check)
			}
			if strings.Contains(check.Detail, "PRIVATE_PERSONA") || strings.Contains(Summary([]Check{check}), "PRIVATE_PERSONA") {
				t.Fatalf("check leaked configured instructions: %#v", check)
			}
		})
	}
}

func TestCodexDeveloperInstructionsCheckAllowsMissingNestedAndUnrelatedConfig(t *testing.T) {
	for name, content := range map[string]*string{
		"missing":   nil,
		"unrelated": stringPointer("model = \"gpt-5\"\n"),
		"nested":    stringPointer("[agents.reviewer]\ndeveloper_instructions = \"subagent only\"\n"),
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			if content != nil {
				if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(*content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			check := codexDeveloperInstructionsCheck(config.Config{AgentsConfigPath: writeCodexAgentsConfig(t, home)})
			if !check.OK || check.Warning {
				t.Fatalf("check = %#v", check)
			}
		})
	}
}

func TestCodexDeveloperInstructionsCheckIgnoresMultilineStringContents(t *testing.T) {
	home := t.TempDir()
	content := `banner = """
developer_instructions = "STRING_CONTENT_ONLY"
[not-a-table]
"""
model = "gpt-5"
`
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	check := codexDeveloperInstructionsCheck(config.Config{AgentsConfigPath: writeCodexAgentsConfig(t, home)})
	if !check.OK || check.Warning || strings.Contains(check.Detail, "STRING_CONTENT_ONLY") {
		t.Fatalf("check = %#v", check)
	}
}

func TestCodexDeveloperInstructionsCheckFindsConflictAfterMultilineString(t *testing.T) {
	home := t.TempDir()
	content := `banner = '''
[not-a-table]
'''
developer_instructions = "PRIVATE_AFTER_BANNER"
`
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	check := codexDeveloperInstructionsCheck(config.Config{AgentsConfigPath: writeCodexAgentsConfig(t, home)})
	if check.OK || check.Warning || !strings.Contains(check.Detail, "conflict") || strings.Contains(check.Detail, "PRIVATE_AFTER_BANNER") {
		t.Fatalf("check = %#v", check)
	}
}

func TestCodexDeveloperInstructionsCheckIgnoresArrayMultilineStringContents(t *testing.T) {
	home := t.TempDir()
	content := `banner = [
"""
developer_instructions = "ARRAY_STRING_CONTENT"
[not-a-table]
""",
]
model = "gpt-5"
`
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	check := codexDeveloperInstructionsCheck(config.Config{AgentsConfigPath: writeCodexAgentsConfig(t, home)})
	if !check.OK || check.Warning || strings.Contains(check.Detail, "ARRAY_STRING_CONTENT") {
		t.Fatalf("check = %#v", check)
	}
}

func TestCodexDeveloperInstructionsCheckFindsConflictAfterArrayMultilineString(t *testing.T) {
	home := t.TempDir()
	content := `banner = [
'''
[not-a-table]
''',
]
developer_instructions = "PRIVATE_AFTER_ARRAY"
`
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	check := codexDeveloperInstructionsCheck(config.Config{AgentsConfigPath: writeCodexAgentsConfig(t, home)})
	if check.OK || check.Warning || !strings.Contains(check.Detail, "conflict") || strings.Contains(check.Detail, "PRIVATE_AFTER_ARRAY") {
		t.Fatalf("check = %#v", check)
	}
}

func TestCodexDeveloperInstructionsCheckFindsConflictAfterNestedArray(t *testing.T) {
	home := t.TempDir()
	content := "matrix = [\n  [1, 2],\n  [3, 4],\n]\ndeveloper_instructions = \"PRIVATE_AFTER_MATRIX\"\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	check := codexDeveloperInstructionsCheck(config.Config{AgentsConfigPath: writeCodexAgentsConfig(t, home)})
	if check.OK || !strings.Contains(check.Detail, "conflict") || strings.Contains(check.Detail, "PRIVATE_AFTER_MATRIX") {
		t.Fatalf("check = %#v", check)
	}
}

func TestCodexDeveloperInstructionsCheckDecodesQuotedTopLevelKey(t *testing.T) {
	home := t.TempDir()
	content := `"developer\u005finstructions" = "PRIVATE_QUOTED_KEY"`
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	check := codexDeveloperInstructionsCheck(config.Config{AgentsConfigPath: writeCodexAgentsConfig(t, home)})
	if check.OK || !strings.Contains(check.Detail, "conflict") || strings.Contains(check.Detail, "PRIVATE_QUOTED_KEY") {
		t.Fatalf("check = %#v", check)
	}
}

func TestCodexDeveloperInstructionsCheckIgnoresEqualsInsideQuotedKey(t *testing.T) {
	home := t.TempDir()
	content := `"developer_instructions=disabled" = "value"`
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	check := codexDeveloperInstructionsCheck(config.Config{AgentsConfigPath: writeCodexAgentsConfig(t, home)})
	if !check.OK || check.Warning {
		t.Fatalf("check = %#v", check)
	}
}

func writeCodexAgentsConfig(t *testing.T, home string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".lark-agent-bridge", "agents.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	doc := `{"schema_version":1,"agents":[{"kind":"codex","homes":[{"label":"explicit","path":` + strconv.Quote(home) + `}]}]}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func stringPointer(value string) *string { return &value }

func TestReplyStoreWritableRejectsMalformedMappingWithoutLeakingContents(t *testing.T) {
	workDir := canonicalTempDir(t)
	storeDir := filepath.Join(workDir, ".lark-agent-bridge")
	if err := os.Mkdir(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(storeDir, "replies.json")
	const secret = "DO_NOT_PRINT_REPLY_SECRET"
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"latest_by_scope":{"":{"CardID":"`+secret+`"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := doctorTestConfig(workDir, filepath.Join(workDir, "sessions.json"))
	cfg.ReplyStorePath = path
	check := findCheck(t, Run(cfg), "reply_store")
	if check.OK || strings.Contains(check.Detail, secret) || strings.Contains(Summary([]Check{check}), secret) {
		t.Fatalf("malformed reply check leaked or passed: %#v", check)
	}
}

func TestReplyStoreWritableRejectsUnreadableStoreShape(t *testing.T) {
	workDir := canonicalTempDir(t)
	storeDir := filepath.Join(workDir, ".lark-agent-bridge")
	if err := os.Mkdir(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(storeDir, "replies.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := doctorTestConfig(workDir, filepath.Join(workDir, "sessions.json"))
	cfg.ReplyStorePath = path
	if check := findCheck(t, Run(cfg), "reply_store"); check.OK || !strings.Contains(check.Detail, "not_regular") {
		t.Fatalf("reply store shape check = %#v", check)
	}
}

func TestPreferenceStoreWritableValidatesExistingSnapshotWithoutLeakingContents(t *testing.T) {
	workDir := canonicalTempDir(t)
	path := filepath.Join(workDir, ".lark-agent-bridge", "preferences.json")
	store, err := config.OpenPreferenceStore(path, config.RuntimePreference{Model: "default", Effort: "low"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(config.RuntimePreference{Model: "opus", Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	cfg := doctorTestConfig(workDir, filepath.Join(workDir, ".lark-agent-bridge", "sessions.json"))
	cfg.PreferenceStorePath = path
	if check := findCheck(t, Run(cfg), "preference_store"); !check.OK {
		t.Fatalf("preference_store = %#v, want success", check)
	}
	const secret = "DO_NOT_PRINT_PREFERENCE_SECRET"
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"override":{"model":"`+secret+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	check := findCheck(t, Run(cfg), "preference_store")
	if check.OK || strings.Contains(check.Detail, secret) || strings.Contains(Summary([]Check{check}), secret) {
		t.Fatalf("malformed preference check leaked or passed: %#v", check)
	}
}

func TestPreferenceStoreWritableAcceptsConfiguredCodexPreference(t *testing.T) {
	workDir := canonicalTempDir(t)
	agentsPath := filepath.Join(workDir, ".lark-agent-bridge", "agents.json")
	if err := os.MkdirAll(filepath.Dir(agentsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentsPath, []byte(`{"schema_version":1,"agents":[{"kind":"claude"},{"kind":"codex"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	agents, err := config.LoadAgentsConfig(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workDir, ".lark-agent-bridge", "preferences.json")
	store, err := config.OpenPreferenceStore(path, config.RuntimePreference{Model: "default", Effort: "low"}, nil, agents.Agents...)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(config.RuntimePreference{Model: "default", Effort: "low", Agent: "codex"}); err != nil {
		t.Fatal(err)
	}
	cfg := doctorTestConfig(workDir, filepath.Join(workDir, ".lark-agent-bridge", "sessions.json"))
	cfg.PreferenceStorePath = path
	cfg.AgentsConfigPath = agentsPath
	if check := findCheck(t, Run(cfg), "preference_store"); !check.OK {
		t.Fatalf("preference_store = %#v, want configured codex preference to pass", check)
	}
}

func TestClaudeWrapperPreflightUsesBoundedHarmlessInvocation(t *testing.T) {
	dir := canonicalTempDir(t)
	argsPath := filepath.Join(dir, "args")
	pwdPath := filepath.Join(dir, "pwd")
	bin := writeDoctorExecutable(t, dir, `#!/bin/sh
printf '%s\n' "$@" > "$DOCTOR_ARGS_FILE"
pwd > "$DOCTOR_PWD_FILE"
printf '%s\n' '{"type":"result","result":"PRIVATE_OUTPUT_MUST_NOT_APPEAR"}'
`)
	t.Setenv("DOCTOR_ARGS_FILE", argsPath)
	t.Setenv("DOCTOR_PWD_FILE", pwdPath)
	cfg := doctorTestConfig(dir, filepath.Join(dir, "sessions.json"))
	cfg.ClaudeBin = bin
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	check := ClaudeWrapperPreflight(ctx, cfg)
	if !check.OK || check.Warning || check.Name != "wrapper-preflight" || strings.Contains(check.Detail, "PRIVATE_OUTPUT") {
		t.Fatalf("preflight check = %#v", check)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.ReplaceAll(strings.TrimSpace(string(args)), "\n", " ")
	if !strings.Contains(joined, "-p --output-format stream-json --verbose --effort low") || !strings.Contains(joined, "Reply with exactly OK") {
		t.Fatalf("preflight args = %q", joined)
	}
	pwd, err := os.ReadFile(pwdPath)
	if err != nil || strings.TrimSpace(string(pwd)) != dir {
		t.Fatalf("preflight pwd = %q, err=%v, want %q", pwd, err, dir)
	}
}

func TestEffectiveWrapperPreflightUsesSelectedCodexPreset(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	stdinPath := filepath.Join(dir, "stdin")
	homePath := filepath.Join(dir, "home")
	codexHome := filepath.Join(dir, "codex-home")
	if err := os.Mkdir(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	codexBin := writeDoctorExecutable(t, dir, `#!/bin/sh
printf '%s\n' "$@" > "$DOCTOR_ARGS_FILE"
cat > "$DOCTOR_STDIN_FILE"
printf '%s' "${CODEX_HOME:-}" > "$DOCTOR_HOME_FILE"
`)
	failingClaude := filepath.Join(dir, "failing-claude")
	if err := os.WriteFile(failingClaude, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	agentsPath := filepath.Join(dir, "agents.json")
	agentsJSON := fmt.Sprintf(`{"schema_version":1,"agents":[{"kind":"claude"},{"kind":"codex","homes":[{"label":"workspace","path":%q}],"bins":[{"label":"cx2","path":%q}]}]}`, codexHome, codexBin)
	if err := os.WriteFile(agentsPath, []byte(agentsJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	agents, err := config.LoadAgentsConfig(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	preferencePath := filepath.Join(dir, "preferences.json")
	store, err := config.OpenPreferenceStore(preferencePath, config.RuntimePreference{Model: "default", Effort: "low"}, nil, agents.Agents...)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(config.RuntimePreference{Model: "default", Effort: "low", Agent: "codex", AgentHome: "workspace", AgentBin: "cx2"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCTOR_ARGS_FILE", argsPath)
	t.Setenv("DOCTOR_STDIN_FILE", stdinPath)
	t.Setenv("DOCTOR_HOME_FILE", homePath)
	cfg := doctorTestConfig(dir, filepath.Join(dir, "sessions.json"))
	cfg.ClaudeBin = failingClaude
	cfg.AgentsConfigPath = agentsPath
	cfg.PreferenceStorePath = preferencePath

	check := EffectiveWrapperPreflight(context.Background(), cfg)

	if !check.OK || check.Warning || check.Detail != "passed: codex" {
		t.Fatalf("Codex effective preflight = %#v", check)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if joined := strings.ReplaceAll(strings.TrimSpace(string(args)), "\n", " "); !strings.Contains(joined, "exec --json -") {
		t.Fatalf("Codex preflight args = %q", joined)
	}
	stdin, err := os.ReadFile(stdinPath)
	if err != nil || !strings.Contains(string(stdin), "Reply with exactly OK") {
		t.Fatalf("Codex preflight stdin = %q, err=%v", stdin, err)
	}
	home, err := os.ReadFile(homePath)
	if err != nil || string(home) != codexHome {
		t.Fatalf("Codex preflight home = %q, err=%v", home, err)
	}
}

func TestClaudeWrapperPreflightClassifiesExit78WithoutOutputLeak(t *testing.T) {
	dir := t.TempDir()
	bin := writeDoctorExecutable(t, dir, "#!/bin/sh\nprintf 'PRIVATE_ENV_VALUE' >&2\nexit 78\n")
	cfg := doctorTestConfig(dir, filepath.Join(dir, "sessions.json"))
	cfg.ClaudeBin = bin
	check := ClaudeWrapperPreflight(context.Background(), cfg)
	if !check.OK || !check.Warning || !strings.Contains(check.Detail, "exit 78") || strings.Contains(check.Detail, "PRIVATE_ENV_VALUE") {
		t.Fatalf("exit 78 check = %#v", check)
	}
	strict := RunWithOptions(context.Background(), cfg, RunOptions{WrapperPreflight: true, Strict: true, PreflightTimeout: time.Second})
	strictCheck := findCheck(t, strict, "wrapper-preflight")
	if strictCheck.OK || strictCheck.Warning {
		t.Fatalf("strict wrapper preflight = %#v, want failure", strictCheck)
	}
}

func TestClaudeWrapperPreflightHonorsContextTimeout(t *testing.T) {
	dir := t.TempDir()
	bin := writeDoctorExecutable(t, dir, "#!/bin/sh\nsleep 2\n")
	cfg := doctorTestConfig(dir, filepath.Join(dir, "sessions.json"))
	cfg.ClaudeBin = bin
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	check := ClaudeWrapperPreflight(ctx, cfg)
	if !check.OK || !check.Warning || !strings.Contains(check.Detail, "timed out") {
		t.Fatalf("timeout check = %#v", check)
	}
}

func TestRunReportsMissingWorkdir(t *testing.T) {
	baseDir := t.TempDir()
	missingDir := filepath.Join(baseDir, "missing")

	checks := Run(config.Config{
		DefaultAgent:     "claude",
		DefaultWorkDir:   missingDir,
		CardUpdateEvery:  time.Second,
		CardMaxChars:     12000,
		AuditLogPath:     filepath.Join(baseDir, "audit.jsonl"),
		SessionStorePath: filepath.Join(baseDir, "sessions.json"),
	})

	check := findCheck(t, checks, "default_workdir")
	if check.OK {
		t.Fatalf("default_workdir check OK = true, want false")
	}
}

func TestRunRejectsSymlinkMediaCacheWithoutMutatingTarget(t *testing.T) {
	target := canonicalTempDir(t)
	link := filepath.Join(canonicalTempDir(t), "media-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	cfg := doctorTestConfig(t.TempDir(), filepath.Join(t.TempDir(), "sessions.json"))
	cfg.MediaCacheDir = link
	check := findCheck(t, Run(cfg), "media_cache")
	if check.OK {
		t.Fatalf("media_cache check = %#v, want symlink failure", check)
	}
	if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
		t.Fatalf("media cache target was mutated: entries=%v err=%v", entries, err)
	}
}

func TestRunRejectsIntermediateSymlinkWithoutMutatingTarget(t *testing.T) {
	base := canonicalTempDir(t)
	target := canonicalTempDir(t)
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	cfg := doctorTestConfig(t.TempDir(), filepath.Join(t.TempDir(), "sessions.json"))
	cfg.MediaCacheDir = filepath.Join(link, "media")

	check := findCheck(t, Run(cfg), "media_cache")
	if check.OK {
		t.Fatalf("media_cache check = %#v, want intermediate symlink failure", check)
	}
	if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
		t.Fatalf("intermediate symlink target was mutated: entries=%v err=%v", entries, err)
	}
}

func TestRunRejectsRegularFileMediaCache(t *testing.T) {
	cachePath := filepath.Join(canonicalTempDir(t), "media")
	if err := os.WriteFile(cachePath, []byte("not a cache directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := doctorTestConfig(t.TempDir(), filepath.Join(t.TempDir(), "sessions.json"))
	cfg.MediaCacheDir = cachePath

	check := findCheck(t, Run(cfg), "media_cache")
	if check.OK {
		t.Fatalf("media_cache check = %#v, want regular-file failure", check)
	}
}

func TestRunRejectsMediaCacheWhenParentCannotBeCreated(t *testing.T) {
	blockingFile := filepath.Join(canonicalTempDir(t), "not-a-directory")
	if err := os.WriteFile(blockingFile, []byte("blocks child directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := doctorTestConfig(t.TempDir(), filepath.Join(t.TempDir(), "sessions.json"))
	cfg.MediaCacheDir = filepath.Join(blockingFile, "media")

	check := findCheck(t, Run(cfg), "media_cache")
	if check.OK {
		t.Fatalf("media_cache check = %#v, want parent creation failure", check)
	}
}

func TestRunCreatesPrivateMediaCacheWithoutChangingExistingParent(t *testing.T) {
	workDir := canonicalTempDir(t)
	parent := filepath.Join(workDir, "existing-parent")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	before := filePermissions(t, parent)
	cachePath := filepath.Join(parent, "new", "media")
	cfg := doctorTestConfig(workDir, filepath.Join(workDir, "sessions.json"))
	cfg.MediaCacheDir = cachePath

	check := findCheck(t, Run(cfg), "media_cache")
	if !check.OK {
		t.Fatalf("media_cache check = %#v, want success", check)
	}
	if after := filePermissions(t, parent); after != before {
		t.Fatalf("existing parent permissions changed from %o to %o", before, after)
	}
	for _, path := range []string{filepath.Join(parent, "new"), cachePath} {
		if got := filePermissions(t, path); got != 0o700 {
			t.Fatalf("new media cache directory %q permissions = %o, want 700", path, got)
		}
	}
	if probes, err := filepath.Glob(filepath.Join(cachePath, ".media-cache-probe-*")); err != nil || len(probes) != 0 {
		t.Fatalf("media cache probe files = %q, err = %v; want none", probes, err)
	}
}

func TestRunRejectsInsecureExistingMediaCacheWithoutChangingMode(t *testing.T) {
	for _, mode := range []os.FileMode{0o755, 0o777} {
		t.Run(mode.String(), func(t *testing.T) {
			workDir := canonicalTempDir(t)
			cachePath := filepath.Join(workDir, "media")
			if err := os.Mkdir(cachePath, mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(cachePath, mode); err != nil {
				t.Fatal(err)
			}
			cfg := doctorTestConfig(workDir, filepath.Join(workDir, "sessions.json"))
			cfg.MediaCacheDir = cachePath

			check := findCheck(t, Run(cfg), "media_cache")
			if check.OK {
				t.Fatalf("media_cache check = %#v, want insecure mode failure", check)
			}
			if got := filePermissions(t, cachePath); got != mode {
				t.Fatalf("existing media cache permissions changed from %o to %o", mode, got)
			}
		})
	}
}

func TestRunRejectsMalformedSessionStoreWithoutLeakingContents(t *testing.T) {
	workDir := t.TempDir()
	storePath := filepath.Join(workDir, "sessions.json")
	const secret = "DO_NOT_PRINT_SESSION_SECRET"
	if err := os.WriteFile(storePath, []byte(`{"schema_version":`+secret), 0o600); err != nil {
		t.Fatal(err)
	}

	check := findCheck(t, Run(doctorTestConfig(workDir, storePath)), "session_store")
	if check.OK {
		t.Fatalf("session_store check = %#v, want failure", check)
	}
	if strings.Contains(check.Detail, secret) || strings.Contains(Summary([]Check{check}), secret) {
		t.Fatalf("session store contents leaked: %#v", check)
	}
}

func TestRunRejectsUnsupportedSessionStoreSchema(t *testing.T) {
	workDir := t.TempDir()
	storePath := filepath.Join(workDir, "sessions.json")
	if err := os.WriteFile(storePath, []byte(`{"schema_version":999}`), 0o600); err != nil {
		t.Fatal(err)
	}

	check := findCheck(t, Run(doctorTestConfig(workDir, storePath)), "session_store")
	if check.OK {
		t.Fatalf("session_store check = %#v, want failure", check)
	}
}

func TestRunRejectsInsecureSessionStoreFile(t *testing.T) {
	workDir := t.TempDir()
	storePath := filepath.Join(workDir, "sessions.json")
	if err := session.SaveSnapshot(storePath, session.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(storePath, 0o644); err != nil {
		t.Fatal(err)
	}

	check := findCheck(t, Run(doctorTestConfig(workDir, storePath)), "session_store")
	if check.OK {
		t.Fatalf("session_store check = %#v, want failure", check)
	}
}

func TestSessionStoreRelativePathDoesNotMutateCurrentDirectoryPermissions(t *testing.T) {
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	if err := os.Chmod(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDir) })
	before := filePermissions(t, cwd)

	check := sessionStoreWritable("sessions.json")

	if check.OK {
		t.Errorf("relative session_store check = %#v, want failure for cwd permissions", check)
	}
	if after := filePermissions(t, cwd); after != before {
		t.Errorf("cwd permissions changed from %o to %o", before, after)
	}
}

func TestSessionCatalogCheckAcceptsMissingAndValidCatalog(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "session-catalog.json")
	if check := sessionCatalogCheck(path); !check.OK {
		t.Fatalf("missing catalog check = %#v", check)
	}
	catalog, err := session.OpenCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	canonical, err := session.CanonicalWorkDir(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Upsert(session.CatalogEntry{SessionID: "session", Agent: agent.Claude, WorkDir: canonical, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if check := sessionCatalogCheck(path); !check.OK {
		t.Fatalf("valid catalog check = %#v", check)
	}
}

func TestSessionCatalogCheckRejectsMalformedAndUnsupportedSchema(t *testing.T) {
	for name, data := range map[string]string{
		"malformed": `{`,
		"schema":    `{"schema_version":99,"entries":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session-catalog.json")
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if check := sessionCatalogCheck(path); check.OK {
				t.Fatalf("catalog check = %#v, want failure", check)
			}
		})
	}
}

func TestSessionStoreRejectsSymlinkParentWithoutMutatingTargetOrLeavingProbe(t *testing.T) {
	target := t.TempDir()
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	link := filepath.Join(parent, "store-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	before := filePermissions(t, target)

	check := sessionStoreWritable(filepath.Join(link, "sessions.json"))

	if check.OK {
		t.Errorf("symlink-parent session_store check = %#v, want failure", check)
	}
	if after := filePermissions(t, target); after != before {
		t.Errorf("symlink target permissions changed from %o to %o", before, after)
	}
	probes, err := filepath.Glob(filepath.Join(target, ".session-store-probe-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(probes) != 0 {
		t.Fatalf("symlink target probe files = %q, want none", probes)
	}
}

func TestRunAcceptsValidExistingSessionStoreAndCleansProbe(t *testing.T) {
	workDir := t.TempDir()
	storePath := filepath.Join(workDir, "store", "sessions.json")
	if err := session.SaveSnapshot(storePath, session.Snapshot{}); err != nil {
		t.Fatal(err)
	}

	check := findCheck(t, Run(doctorTestConfig(workDir, storePath)), "session_store")
	if !check.OK {
		t.Fatalf("session_store check = %#v, want success", check)
	}
	probes, err := filepath.Glob(filepath.Join(filepath.Dir(storePath), ".session-store-probe-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(probes) != 0 {
		t.Fatalf("session store probe files = %q, want none", probes)
	}
}

func TestRunCreatesSharedAuditAndSessionParentSecurely(t *testing.T) {
	workDir := t.TempDir()
	parent := filepath.Join(workDir, ".lark-agent-bridge")
	checks := Run(config.Config{
		DefaultAgent:       "claude",
		DefaultWorkDir:     workDir,
		CardUpdateEvery:    time.Second,
		InteractionTimeout: time.Second,
		CardMaxChars:       12000,
		AuditLogPath:       filepath.Join(parent, "audit.jsonl"),
		SessionStorePath:   filepath.Join(parent, "sessions.json"),
	})

	if !findCheck(t, checks, "audit_log").OK || !findCheck(t, checks, "session_store").OK {
		t.Fatalf("checks = %#v, want secure shared parent", checks)
	}
	if got := filePermissions(t, parent); got != 0o700 {
		t.Fatalf("shared parent permissions = %o, want 700", got)
	}
}

func TestAuditLogWritableCreatesSecureParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit", "audit.jsonl")

	check := auditLogWritable(path)

	if !check.OK {
		t.Fatalf("audit log check = %#v, want success", check)
	}
	if got := filePermissions(t, filepath.Dir(path)); got != 0o700 {
		t.Fatalf("audit parent permissions = %o, want 700", got)
	}
}

func TestAuditLogWritablePreservesExistingCustomDirectoryPermissions(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	before := filePermissions(t, dir)
	path := filepath.Join(dir, "audit.jsonl")

	check := auditLogWritable(path)

	if !check.OK {
		t.Fatalf("audit log check = %#v, want success", check)
	}
	if after := filePermissions(t, dir); after != before {
		t.Fatalf("custom audit directory permissions changed from %o to %o", before, after)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("audit log was not created: %v", err)
	}
}

func filePermissions(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func doctorTestConfig(workDir, storePath string) config.Config {
	return config.Config{
		DefaultAgent:        "claude",
		DefaultWorkDir:      workDir,
		CardUpdateEvery:     time.Second,
		InteractionTimeout:  time.Second,
		CardMaxChars:        12000,
		AuditLogPath:        filepath.Join(workDir, "audit.jsonl"),
		SessionStorePath:    storePath,
		PreferenceStorePath: filepath.Join(workDir, "preferences.json"),
		ReplyStorePath:      filepath.Join(workDir, "replies.json"),
		Model:               "default",
		Effort:              "low",
		AllowedModels:       []string{"default", "sonnet", "opus", "haiku"},
		MediaCacheDir:       filepath.Join(workDir, "media"),
	}
}

func writeDoctorExecutable(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-claude")
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSummaryFormatsChecks(t *testing.T) {
	got := Summary([]Check{
		{Name: "a", OK: true, Detail: "ready"},
		{Name: "c", OK: true, Warning: true, Detail: "warning"},
		{Name: "b", OK: false, Detail: "missing"},
	})
	want := "ok a: ready\nwarn c: warning\nfail b: missing"
	if got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

func assertCheck(t *testing.T, checks []Check, name string, ok bool, detail string) {
	t.Helper()
	check := findCheck(t, checks, name)
	if check.OK != ok || check.Detail != detail {
		t.Fatalf("%s check = (%t, %q), want (%t, %q)", name, check.OK, check.Detail, ok, detail)
	}
}

func findCheck(t *testing.T, checks []Check, name string) Check {
	t.Helper()
	for _, check := range checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("check %q not found in %#v", name, checks)
	return Check{}
}
