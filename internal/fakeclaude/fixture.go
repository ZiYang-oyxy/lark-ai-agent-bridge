// Package fakeclaude drives a deterministic fake Claude/Codex agent from
// JSON fixtures. It replaces two historical bespoke implementations:
//   - the shell shim heredoc in scripts/e2e/lib/server.sh, which matched
//     markers embedded in argv and printed hard-coded NDJSON.
//   - the simulateRunner in cmd/lark-agent-bridge/main.go, which returned
//     three hard-coded segments regardless of prompt.
//
// A fixture declares:
//   - name        : human label for evidence.
//   - match       : how to trigger (any marker substring in argv, or a prompt
//                   substring).
//   - emit        : NDJSON lines to write to stdout, in order. Each line is
//                   either a Claude "stream/result/assistant" event or a Codex
//                   "response" event. Consumers parse them as Claude does today.
//                   Template placeholders ${marker}, ${image_name} are
//                   substituted per invocation.
//   - write_image : when true, write a 1x1 test png to CWD before emit. The
//                   filename is image_name (default "e2e-output-${marker}.png").
//                   Reproduces the shim's write_test_image side-effect for the
//                   bridge_image_* / *_OUTPUT_IMAGE fixture family.
//   - image_name  : template for the image filename, allows ${marker}. Only
//                   read when write_image=true.
//   - hang        : when true, block after emit (equivalent to shim's
//                   `exec sleep 300`). Consumers block forever until the
//                   process is killed by the parent. Reproduces the
//                   *_E2E_BLOCK / native_text_stream_stop semantics.
//   - post_delay  : optional trailing sleep (seconds) after emit. Distinct
//                   from hang (which is unbounded).
//
// A single fixture directory can hold any number of files; ResolveFirst
// evaluates fixtures in registration order and returns the first match. When
// no fixture matches, a Default fixture (typed "result" with FAKE_E2E_STARTED)
// is used so simulate paths never silently fail.
//
// The parser and matcher are the source of truth for both the L1 in-process
// runner (Go) and the L2 out-of-process fake binary (Go shell tool). Neither
// side re-encodes marker semantics.
package fakeclaude

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// FixtureMatch describes how to decide whether a fixture applies to an
// incoming agent invocation. Only one of MarkerPattern / PromptContains is
// required; when both are set, both must match.
type FixtureMatch struct {
	// MarkerPattern is a regexp over the last E2E_* token in argv. Used to
	// mirror the historical shell shim marker-based dispatch.
	MarkerPattern string `json:"marker_pattern,omitempty"`
	// PromptContains is a plain substring the last positional argv (prompt)
	// must include. Used for prompt-anchored fixtures (e.g. the pre-flight
	// canary "Reply with exactly OK. Do not use tools.").
	PromptContains string `json:"prompt_contains,omitempty"`
}

// FixtureEmit is one NDJSON line the fixture should write to stdout. Line is
// the raw JSON payload; consumers parse it exactly like a real agent's
// streaming output. DelaySec is the pause before writing this line, letting
// fixtures reproduce debouncing / preview thresholds without ad hoc sleeps.
type FixtureEmit struct {
	Line     string  `json:"line"`
	DelaySec float64 `json:"delay_sec,omitempty"`
}

// Fixture is one deterministic scenario.
type Fixture struct {
	Name       string        `json:"name"`
	Match      FixtureMatch  `json:"match"`
	Emit       []FixtureEmit `json:"emit"`
	PostDelay  float64       `json:"post_delay_sec,omitempty"`
	// WriteImage: when true, materialise a 1x1 test png into CWD before emit.
	// Reproduces the shim's write_test_image side-effect.
	WriteImage bool `json:"write_image,omitempty"`
	// ImageName template; ${marker} substituted. Default "e2e-output-${marker}.png".
	ImageName string `json:"image_name,omitempty"`
	// Hang: block indefinitely after emit (shim's exec sleep 300). Distinct
	// from a finite post_delay; the caller is expected to kill this process.
	Hang bool `json:"hang,omitempty"`
	// SourcePath is populated by LoadDir; not serialised.
	SourcePath string `json:"-"`
}

// LoadDir reads every *.json file under dir and returns fixtures in filename
// order (stable dispatch). Files that fail to parse cause an error rather than
// being silently skipped -- fixture correctness is a hard contract, not a soft
// preference.
func LoadDir(dir string) ([]Fixture, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("fakeclaude: read fixture dir %q: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	fixtures := make([]Fixture, 0, len(names))
	for _, name := range names {
		p := filepath.Join(dir, name)
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("fakeclaude: read %q: %w", p, err)
		}
		var f Fixture
		if err := json.Unmarshal(data, &f); err != nil {
			return nil, fmt.Errorf("fakeclaude: parse %q: %w", p, err)
		}
		if strings.TrimSpace(f.Name) == "" {
			return nil, fmt.Errorf("fakeclaude: %q missing name", p)
		}
		if f.Match.MarkerPattern == "" && f.Match.PromptContains == "" {
			return nil, fmt.Errorf("fakeclaude: %q must set match.marker_pattern or match.prompt_contains", p)
		}
		if _, err := regexp.Compile(f.Match.MarkerPattern); f.Match.MarkerPattern != "" && err != nil {
			return nil, fmt.Errorf("fakeclaude: %q invalid marker_pattern: %w", p, err)
		}
		for i, e := range f.Emit {
			if strings.TrimSpace(e.Line) == "" {
				return nil, fmt.Errorf("fakeclaude: %q emit[%d] empty line", p, i)
			}
		}
		f.SourcePath = p
		fixtures = append(fixtures, f)
	}
	return fixtures, nil
}

// Invocation is the subset of an agent invocation the matcher looks at.
// Prompt is the last positional argv, Marker is the last E2E_[A-Za-z0-9_-]+
// token found in the joined argv (matches the shell shim's `grep -Eo`).
type Invocation struct {
	Argv   []string
	Prompt string
	Marker string
}

// NewInvocation derives an Invocation from argv, extracting the last E2E_*
// marker so callers don't repeat the shell shim logic.
var markerRe = regexp.MustCompile(`E2E_[A-Za-z0-9_-]+`)

func NewInvocation(argv []string) Invocation {
	inv := Invocation{Argv: argv}
	if len(argv) > 0 {
		inv.Prompt = argv[len(argv)-1]
	}
	joined := strings.Join(argv, " ")
	matches := markerRe.FindAllString(joined, -1)
	if len(matches) > 0 {
		inv.Marker = matches[len(matches)-1]
	}
	return inv
}

// Matches reports whether a fixture applies to an invocation.
func (f Fixture) Matches(inv Invocation) bool {
	if f.Match.MarkerPattern != "" {
		re, err := regexp.Compile(f.Match.MarkerPattern)
		if err != nil || !re.MatchString(inv.Marker) {
			return false
		}
	}
	if f.Match.PromptContains != "" {
		if !strings.Contains(inv.Prompt, f.Match.PromptContains) {
			return false
		}
	}
	return true
}

// Resolve returns the first fixture that matches the invocation. When nothing
// matches it returns Default(), so callers never have to nil-check.
func Resolve(fixtures []Fixture, inv Invocation) Fixture {
	for _, f := range fixtures {
		if f.Matches(inv) {
			return f
		}
	}
	return Default(inv)
}

// Default is the "no fixture matched" fallback. Historically the shell shim
// printed `FAKE_E2E_STARTED [<marker>]` result and exited 0; we preserve that
// exact behaviour so any case that didn't declare a fixture (yet) keeps
// working.
func Default(inv Invocation) Fixture {
	result := "FAKE_E2E_STARTED"
	if inv.Marker != "" {
		result = "FAKE_E2E_STARTED " + inv.Marker
	}
	payload := struct {
		Type   string `json:"type"`
		Result string `json:"result"`
		Model  string `json:"model"`
		Usage  struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		SessionID string `json:"session_id"`
	}{
		Type:      "result",
		Result:    result,
		Model:     "fake-claude-e2e",
		SessionID: "fake-e2e-session",
	}
	payload.Usage.OutputTokens = 1
	line, _ := json.Marshal(payload)
	return Fixture{
		Name:  "default",
		Match: FixtureMatch{},
		Emit:  []FixtureEmit{{Line: string(line)}},
	}
}

// ResolvedImageName returns the on-disk filename this fixture would write when
// WriteImage is true, with the ${marker} placeholder filled from inv. When
// WriteImage is false, returns "".
func (f Fixture) ResolvedImageName(inv Invocation) string {
	if !f.WriteImage {
		return ""
	}
	tpl := f.ImageName
	if tpl == "" {
		tpl = "e2e-output-${marker}.png"
	}
	return renderTemplate(tpl, inv, "")
}

// RenderEmit returns emit line i with ${marker} / ${image_name} substituted.
// image_name is resolved per fixture (see ResolvedImageName).
func (f Fixture) RenderEmit(i int, inv Invocation) string {
	if i < 0 || i >= len(f.Emit) {
		return ""
	}
	return renderTemplate(f.Emit[i].Line, inv, f.ResolvedImageName(inv))
}

// renderTemplate does ${marker} / ${image_name} substitution. Kept intentionally
// tiny — full text/template is overkill for two placeholders and would drag
// escaping complications into fixture JSON authoring.
func renderTemplate(s string, inv Invocation, imageName string) string {
	s = strings.ReplaceAll(s, "${marker}", inv.Marker)
	s = strings.ReplaceAll(s, "${image_name}", imageName)
	return s
}

// testImagePNGBase64 is the 1x1 png the shim used, matches the original
// heredoc's inline base64 literal byte-for-byte so evidence checks (audit sha /
// image size) stay identical.
const testImagePNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

// TestImagePNG returns the decoded 1x1 png bytes. Callers write it to CWD to
// reproduce the shim's write_test_image side-effect.
func TestImagePNG() ([]byte, error) {
	return base64.StdEncoding.DecodeString(testImagePNGBase64)
}

// ValidateInstruction verifies the shell shim's original invariant:
// the instruction file passed via --append-system-prompt-file must exist and
// contain "Feishu Bridge Runtime Instructions"; the prompt itself must NOT
// contain that string (would indicate instruction leaked into user prompt).
// Callers that don't want this check (e.g. the OK preflight probe) skip it.
func ValidateInstruction(argv []string) error {
	var instrFile string
	prev := ""
	for _, a := range argv {
		if prev == "--append-system-prompt-file" {
			instrFile = a
		}
		prev = a
	}
	if instrFile == "" {
		return errors.New("fakeclaude: --append-system-prompt-file missing")
	}
	data, err := os.ReadFile(instrFile)
	if err != nil {
		return fmt.Errorf("fakeclaude: read instruction file %q: %w", instrFile, err)
	}
	if !strings.Contains(string(data), "Feishu Bridge Runtime Instructions") {
		return fmt.Errorf("fakeclaude: instruction file %q missing marker", instrFile)
	}
	if len(argv) > 0 {
		last := argv[len(argv)-1]
		if strings.Contains(last, "Feishu Bridge Runtime Instructions") {
			return errors.New("fakeclaude: runtime instructions leaked into user prompt")
		}
	}
	return nil
}
