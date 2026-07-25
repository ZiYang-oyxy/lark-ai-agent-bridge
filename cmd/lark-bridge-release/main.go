package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	bridgeupdate "lark-agent-bridge/internal/update"
)

const requiredGoVersion = "go1.26.3"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "prepare":
		return runPrepare(args[1:])
	case "tag":
		return runTag(args[1:])
	case "bundle":
		return runBundle(args[1:])
	case "help", "-h", "--help":
		fmt.Print(releaseUsage())
		return nil
	default:
		return usageError()
	}
}

func runPrepare(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: lark-bridge-release prepare vMAJOR.MINOR.PATCH[-rc.N]")
	}
	tag := args[0]
	canonical, err := canonicalReleaseVersion(tag)
	if err != nil {
		return err
	}
	if err := requireCleanWorktree(); err != nil {
		return err
	}
	if err := requireReleaseNoteTemplate(); err != nil {
		return err
	}
	latest, _ := gitOutput("tag", "--list", "v*", "--sort=-version:refname")
	previous := firstLine(latest)
	if previous != "" {
		previousCanonical, parseErr := canonicalReleaseVersion(previous)
		if parseErr != nil {
			return parseErr
		}
		comparison, compareErr := compareReleaseVersions(canonical, previousCanonical)
		if compareErr != nil || comparison <= 0 {
			return fmt.Errorf("release %s must be newer than %s", tag, previous)
		}
	}
	rangeArg := "HEAD"
	if previous != "" {
		rangeArg = previous + "..HEAD"
	}
	log, err := gitOutput("log", "--format=%s", rangeArg)
	if err != nil {
		return err
	}
	subjects := nonEmptyLines(log)
	path := filepath.Join("docs", "releases", tag+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("release note already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.WriteFile(path, []byte(renderReleaseNotes(tag, subjects)), 0o644); err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

func runTag(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: lark-bridge-release tag vMAJOR.MINOR.PATCH[-rc.N]")
	}
	tag := args[0]
	if _, err := canonicalReleaseVersion(tag); err != nil {
		return err
	}
	if err := requireCleanWorktree(); err != nil {
		return err
	}
	note := filepath.Join("docs", "releases", tag+".md")
	if _, err := os.Stat(note); err != nil {
		return fmt.Errorf("release note is missing: %s", note)
	}
	tracked, err := gitOutput("ls-files", "--error-unmatch", note)
	if err != nil || strings.TrimSpace(tracked) == "" {
		return fmt.Errorf("release note is not tracked: %s", note)
	}
	if err := validateReleaseNoteFile(tag); err != nil {
		return err
	}
	if err := command("go", "test", "./..."); err != nil {
		return fmt.Errorf("release tests: %w", err)
	}
	return command("git", "tag", "-a", tag, "-F", note)
}

func runBundle(args []string) error {
	tag, baseURL, err := parseBundleArgs(args)
	if err != nil {
		return err
	}
	canonical, err := canonicalReleaseVersion(tag)
	if err != nil {
		return err
	}
	if err := validateBaseURL(baseURL); err != nil {
		return err
	}
	if err := requireCleanWorktree(); err != nil {
		return err
	}
	if err := requireExactAnnotatedTag(tag); err != nil {
		return err
	}
	if err := validateReleaseNoteFile(tag); err != nil {
		return err
	}
	goVersion, err := commandOutput("go", "env", "GOVERSION")
	if err != nil {
		return err
	}
	if strings.TrimSpace(goVersion) != requiredGoVersion {
		return fmt.Errorf("release requires %s, got %s", requiredGoVersion, strings.TrimSpace(goVersion))
	}
	commit, err := gitOutput("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	commit = strings.TrimSpace(commit)
	tagTimeRaw, err := gitOutput("for-each-ref", "--format=%(taggerdate:iso-strict)", "refs/tags/"+tag)
	if err != nil {
		return err
	}
	publishedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(tagTimeRaw))
	if err != nil {
		return fmt.Errorf("parse annotated tag time: %w", err)
	}
	versionDir := filepath.Join("dist", tag)
	if err := os.RemoveAll(versionDir); err != nil {
		return err
	}
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		return err
	}
	ldflags := strings.Join([]string{
		"-s", "-w",
		"-X", "lark-agent-bridge/internal/buildinfo.Version=" + canonical,
		"-X", "lark-agent-bridge/internal/buildinfo.Commit=" + commit,
		"-X", "lark-agent-bridge/internal/buildinfo.BuildTime=" + publishedAt.UTC().Format(time.RFC3339),
	}, " ")
	for _, target := range []struct{ goos, goarch string }{{"linux", "amd64"}, {"darwin", "arm64"}} {
		name := fmt.Sprintf("lark-agent-bridge-%s-%s", target.goos, target.goarch)
		output := filepath.Join(versionDir, name)
		if err := commandEnv(map[string]string{"CGO_ENABLED": "0", "GOOS": target.goos, "GOARCH": target.goarch},
			"go", "build", "-trimpath", "-buildvcs=false", "-ldflags", ldflags, "-o", output, "./cmd/lark-agent-bridge"); err != nil {
			return fmt.Errorf("build %s/%s: %w", target.goos, target.goarch, err)
		}
	}
	noteSource := filepath.Join("docs", "releases", tag+".md")
	if err := copyFile(noteSource, filepath.Join(versionDir, "release-notes.md"), 0o644); err != nil {
		return err
	}
	manifest, err := writeBundleMetadata(versionDir, tag, baseURL, publishedAt.UTC())
	if err != nil {
		return err
	}
	stableDir := filepath.Join("dist", "stable")
	if err := os.MkdirAll(stableDir, 0o755); err != nil {
		return err
	}
	guideSource, err := os.ReadFile(filepath.Join("docs", "workflow", "ai-agent-install.md"))
	if err != nil {
		return fmt.Errorf("read AI install guide: %w", err)
	}
	if err := writeAIInstallGuides(versionDir, stableDir, tag, baseURL, guideSource); err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(stableDir, "manifest.json"), manifest)
}

func parseBundleArgs(args []string) (string, string, error) {
	fs := flag.NewFlagSet("bundle", flag.ContinueOnError)
	baseURL := fs.String("base-url", "", "public HTTPS base URL")
	var tag string
	parseArgs := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		tag = args[0]
		parseArgs = args[1:]
	}
	if err := fs.Parse(parseArgs); err != nil {
		return "", "", err
	}
	if tag == "" && fs.NArg() == 1 {
		tag = fs.Arg(0)
	} else if fs.NArg() != 0 {
		return "", "", errors.New("usage: lark-bridge-release bundle vMAJOR.MINOR.PATCH[-rc.N] --base-url https://host/path")
	}
	if tag == "" || strings.TrimSpace(*baseURL) == "" {
		return "", "", errors.New("usage: lark-bridge-release bundle vMAJOR.MINOR.PATCH[-rc.N] --base-url https://host/path")
	}
	return tag, strings.TrimSpace(*baseURL), nil
}

func canonicalReleaseVersion(tag string) (string, error) {
	canonical := strings.TrimPrefix(tag, "v")
	if !strings.HasPrefix(tag, "v") || !releaseVersionPattern.MatchString(canonical) {
		return "", fmt.Errorf("release tag %q must use vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-rc.N", tag)
	}
	return canonical, nil
}

var releaseVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-rc\.(0|[1-9][0-9]*))?$`)

func compareReleaseVersions(a, b string) (int, error) {
	parse := func(value string) ([4]uint64, bool, error) {
		var numbers [4]uint64
		match := releaseVersionPattern.FindStringSubmatch(value)
		if match == nil {
			return numbers, false, fmt.Errorf("invalid release version %q", value)
		}
		for index := 1; index <= 3; index++ {
			number, err := strconv.ParseUint(match[index], 10, 64)
			if err != nil {
				return numbers, false, err
			}
			numbers[index-1] = number
		}
		if match[4] == "" {
			return numbers, false, nil
		}
		rc, err := strconv.ParseUint(match[4], 10, 64)
		if err != nil {
			return numbers, false, err
		}
		numbers[3] = rc
		return numbers, true, nil
	}
	av, arc, err := parse(a)
	if err != nil {
		return 0, err
	}
	bv, brc, err := parse(b)
	if err != nil {
		return 0, err
	}
	for index := 0; index < 3; index++ {
		if av[index] < bv[index] {
			return -1, nil
		}
		if av[index] > bv[index] {
			return 1, nil
		}
	}
	if arc != brc {
		if arc {
			return -1, nil
		}
		return 1, nil
	}
	if arc {
		if av[3] < bv[3] {
			return -1, nil
		}
		if av[3] > bv[3] {
			return 1, nil
		}
	}
	return 0, nil
}

func renderReleaseNotes(tag string, subjects []string) string {
	groups := make(map[string][]string, len(releaseNoteSections))
	for _, section := range releaseNoteSections {
		groups[section] = nil
	}
	type candidate struct {
		group       string
		description string
	}
	var candidates []candidate
	bestGroup := make(map[string]string)
	priority := map[string]int{
		"Miscellaneous":    1,
		"Bug Fixes":        2,
		"Features":         3,
		"Breaking Changes": 4,
	}
	for _, subject := range subjects {
		subject = strings.TrimSpace(subject)
		if subject == "" {
			continue
		}
		prefix, description, hasColon := strings.Cut(subject, ":")
		description = strings.TrimSpace(description)
		group := "Miscellaneous"
		if hasColon && strings.Contains(prefix, "!") {
			group = "Breaking Changes"
		} else if hasColon && conventionalCommitType(prefix) == "feat" {
			group = "Features"
		} else if hasColon && conventionalCommitType(prefix) == "fix" {
			group = "Bug Fixes"
		}
		if !hasColon || description == "" {
			description = subject
		}
		candidates = append(candidates, candidate{group: group, description: description})
		if current := bestGroup[description]; current == "" || priority[group] > priority[current] {
			bestGroup[description] = group
		}
	}
	emitted := make(map[string]struct{}, len(candidates))
	for _, item := range candidates {
		if bestGroup[item.description] != item.group {
			continue
		}
		if _, ok := emitted[item.description]; ok {
			continue
		}
		groups[item.group] = append(groups[item.group], item.description)
		emitted[item.description] = struct{}{}
	}
	var output strings.Builder
	fmt.Fprintf(&output, "# %s\n", tag)
	for _, group := range releaseNoteSections {
		fmt.Fprintf(&output, "\n## %s\n\n", group)
		if len(groups[group]) == 0 {
			output.WriteString("- 无。\n")
			continue
		}
		for _, item := range groups[group] {
			fmt.Fprintf(&output, "- %s\n", item)
		}
	}
	return output.String()
}

var releaseNoteSections = []string{
	"Breaking Changes",
	"Features",
	"Bug Fixes",
	"Upgrade Notes",
	"Miscellaneous",
}

func conventionalCommitType(prefix string) string {
	prefix = strings.TrimSuffix(strings.TrimSpace(prefix), "!")
	if index := strings.IndexByte(prefix, '('); index >= 0 {
		prefix = prefix[:index]
	}
	return prefix
}

func requireReleaseNoteTemplate() error {
	data, err := os.ReadFile(filepath.Join("docs", "releases", "TEMPLATE.md"))
	if err != nil {
		return fmt.Errorf("read release note template: %w", err)
	}
	template := string(data)
	position := 0
	for _, section := range releaseNoteSections {
		heading := "## " + section
		relative := strings.Index(template[position:], heading)
		if relative < 0 {
			return fmt.Errorf("release note template is missing ordered section %q", section)
		}
		position += relative + len(heading)
	}
	return nil
}

func validateReleaseNoteFile(tag string) error {
	path := filepath.Join("docs", "releases", tag+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read release note: %w", err)
	}
	if err := validateReleaseNote(tag, data); err != nil {
		return fmt.Errorf("invalid release note %s: %w", path, err)
	}
	return nil
}

func validateReleaseNote(tag string, data []byte) error {
	if !utf8.Valid(data) {
		return errors.New("content must be UTF-8")
	}
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(content, "\n")
	titleSeen := false
	nextSection := 0
	currentSection := -1
	bulletCounts := make([]int, len(releaseNoteSections))
	hasEmptyMarker := make([]bool, len(releaseNoteSections))
	seenBullets := make(map[string]string)

	for lineNumber, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if !titleSeen {
			if line != "# "+tag {
				return fmt.Errorf("line %d must be exact title %q", lineNumber+1, "# "+tag)
			}
			titleSeen = true
			continue
		}
		if strings.HasPrefix(line, "## ") {
			// 允许跳过缺失的 sections:不强制全 5 段都出现,只要出现的段按 releaseNoteSections
			// 顺序推进即可。之前"缺 section 报错 + 用『无。』占位"太啰嗦(release 常有整段无内容);
			// 现在没内容直接省略段头,`## Xxx` 出现时校验它是当前指针之后的合法 section 名。
			heading := strings.TrimPrefix(line, "## ")
			matched := -1
			for i := nextSection; i < len(releaseNoteSections); i++ {
				if releaseNoteSections[i] == heading {
					matched = i
					break
				}
			}
			if matched < 0 {
				return fmt.Errorf("line %d has unexpected or out-of-order section %q (expected one of %v starting at %q)",
					lineNumber+1, line, releaseNoteSections[nextSection:], releaseNoteSections[nextSection])
			}
			currentSection = matched
			nextSection = matched + 1
			continue
		}
		if strings.HasPrefix(line, "#") {
			return fmt.Errorf("line %d has an unsupported heading %q", lineNumber+1, line)
		}
		if !strings.HasPrefix(line, "- ") || currentSection < 0 {
			return fmt.Errorf("line %d must be a bullet under a template section", lineNumber+1)
		}
		item := strings.TrimSpace(strings.TrimPrefix(line, "- "))
		if item == "" {
			return fmt.Errorf("line %d has an empty bullet", lineNumber+1)
		}
		bulletCounts[currentSection]++
		if item == "无。" {
			hasEmptyMarker[currentSection] = true
			continue
		}
		if previousSection, exists := seenBullets[item]; exists {
			return fmt.Errorf("line %d duplicates a bullet from %s", lineNumber+1, previousSection)
		}
		seenBullets[item] = releaseNoteSections[currentSection]
	}

	if !titleSeen {
		return errors.New("exact release title is missing")
	}
	// 至少要出现一段(否则整篇 notes 无内容),且所有出现的段(bulletCounts>0)才检查
	// bullet 数量与"无。"占位规则;未出现的段直接跳过,不再强制 5 段全在。
	anyPresent := false
	for index, section := range releaseNoteSections {
		if bulletCounts[index] == 0 {
			continue
		}
		anyPresent = true
		if hasEmptyMarker[index] && bulletCounts[index] != 1 {
			return fmt.Errorf("section %q cannot combine %q with other bullets", section, "无。")
		}
	}
	if !anyPresent {
		return errors.New("release note must contain at least one section with content")
	}
	return nil
}

const aiInstallManifestMarker = "{{MANIFEST_URL}}"

func writeAIInstallGuides(versionDir, stableDir, tag, baseURL string, source []byte) error {
	if strings.Count(string(source), aiInstallManifestMarker) != 1 {
		return errors.New("AI install guide must contain exactly one {{MANIFEST_URL}} marker")
	}
	baseURL = strings.TrimRight(baseURL, "/")
	guides := []struct {
		path        string
		manifestURL string
	}{
		{
			path:        filepath.Join(versionDir, "AI_INSTALL.md"),
			manifestURL: baseURL + "/" + tag + "/manifest.json",
		},
		{
			path:        filepath.Join(stableDir, "AI_INSTALL.md"),
			manifestURL: baseURL + "/stable/manifest.json",
		},
	}
	for _, guide := range guides {
		content := strings.Replace(string(source), aiInstallManifestMarker, guide.manifestURL, 1)
		if err := os.WriteFile(guide.path, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write AI install guide %s: %w", guide.path, err)
		}
		if err := os.Chmod(guide.path, 0o644); err != nil {
			return fmt.Errorf("set AI install guide mode %s: %w", guide.path, err)
		}
	}
	return nil
}

func writeBundleMetadata(dir, tag, baseURL string, publishedAt time.Time) (bridgeupdate.Manifest, error) {
	canonical, err := canonicalReleaseVersion(tag)
	if err != nil {
		return bridgeupdate.Manifest{}, err
	}
	baseURL = strings.TrimRight(baseURL, "/")
	manifest := bridgeupdate.Manifest{
		SchemaVersion: 1, Version: canonical, PublishedAt: publishedAt,
		ReleaseNotesURL: baseURL + "/" + tag + "/release-notes.md",
		Assets:          map[string]bridgeupdate.Asset{},
	}
	type target struct{ platform, name string }
	targets := []target{
		{platform: "darwin/arm64", name: "lark-agent-bridge-darwin-arm64"},
		{platform: "linux/amd64", name: "lark-agent-bridge-linux-amd64"},
	}
	var checksumLines []string
	for _, target := range targets {
		path := filepath.Join(dir, target.name)
		data, err := os.ReadFile(path)
		if err != nil {
			return bridgeupdate.Manifest{}, err
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(data))
		manifest.Assets[target.platform] = bridgeupdate.Asset{
			URL: baseURL + "/" + tag + "/" + target.name, SHA256: digest, Size: int64(len(data)),
		}
		checksumLines = append(checksumLines, digest+"  "+target.name)
	}
	sort.Strings(checksumLines)
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(strings.Join(checksumLines, "\n")+"\n"), 0o644); err != nil {
		return bridgeupdate.Manifest{}, err
	}
	if err := writeJSONAtomic(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		return bridgeupdate.Manifest{}, err
	}
	return manifest, nil
}

func validateBaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("--base-url must be an absolute HTTPS URL without userinfo, query, or fragment")
	}
	return nil
}

func requireCleanWorktree() error {
	status, err := gitOutput("status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		return errors.New("release requires a clean worktree")
	}
	return nil
}

func requireExactAnnotatedTag(tag string) error {
	exact, err := gitOutput("describe", "--tags", "--exact-match", "HEAD")
	if err != nil || strings.TrimSpace(exact) != tag {
		return fmt.Errorf("HEAD is not exact tag %s", tag)
	}
	typeName, err := gitOutput("cat-file", "-t", "refs/tags/"+tag)
	if err != nil || strings.TrimSpace(typeName) != "tag" {
		return fmt.Errorf("tag %s is not annotated", tag)
	}
	return nil
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".manifest-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func copyFile(source, target string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	return errors.Join(copyErr, closeErr)
}

func gitOutput(args ...string) (string, error) { return commandOutput("git", args...) }

func commandOutput(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func command(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func commandEnv(values map[string]string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	env := os.Environ()
	for key, value := range values {
		prefix := key + "="
		filtered := env[:0]
		for _, item := range env {
			if !strings.HasPrefix(item, prefix) {
				filtered = append(filtered, item)
			}
		}
		env = append(filtered, prefix+value)
	}
	cmd.Env = env
	return cmd.Run()
}

func nonEmptyLines(value string) []string {
	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(value))
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func firstLine(value string) string {
	lines := nonEmptyLines(value)
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}

func releaseUsage() string {
	return `lark-bridge-release

Usage:
  lark-bridge-release prepare vMAJOR.MINOR.PATCH[-rc.N]
  lark-bridge-release tag vMAJOR.MINOR.PATCH[-rc.N]
  lark-bridge-release bundle vMAJOR.MINOR.PATCH[-rc.N] --base-url https://host/path
`
}

func usageError() error { return errors.New(strings.TrimSpace(releaseUsage())) }
