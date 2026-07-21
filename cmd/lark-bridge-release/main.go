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
	"sort"
	"strings"
	"time"

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
		return errors.New("usage: lark-bridge-release prepare vMAJOR.MINOR.PATCH")
	}
	tag := args[0]
	canonical, err := canonicalReleaseVersion(tag)
	if err != nil {
		return err
	}
	if err := requireCleanWorktree(); err != nil {
		return err
	}
	latest, _ := gitOutput("tag", "--list", "v*", "--sort=-version:refname")
	previous := firstLine(latest)
	if previous != "" {
		previousCanonical, parseErr := canonicalReleaseVersion(previous)
		if parseErr != nil {
			return parseErr
		}
		comparison, compareErr := bridgeupdate.CompareStable(canonical, previousCanonical)
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
		return errors.New("usage: lark-bridge-release tag vMAJOR.MINOR.PATCH")
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
		return "", "", errors.New("usage: lark-bridge-release bundle vMAJOR.MINOR.PATCH --base-url https://host/path")
	}
	if tag == "" || strings.TrimSpace(*baseURL) == "" {
		return "", "", errors.New("usage: lark-bridge-release bundle vMAJOR.MINOR.PATCH --base-url https://host/path")
	}
	return tag, strings.TrimSpace(*baseURL), nil
}

func canonicalReleaseVersion(tag string) (string, error) {
	if !strings.HasPrefix(tag, "v") || len(tag) == 1 {
		return "", fmt.Errorf("release tag %q must use vMAJOR.MINOR.PATCH", tag)
	}
	canonical := strings.TrimPrefix(tag, "v")
	if _, err := bridgeupdate.CompareStable(canonical, canonical); err != nil {
		return "", fmt.Errorf("invalid stable release tag %q: %w", tag, err)
	}
	return canonical, nil
}

func renderReleaseNotes(tag string, subjects []string) string {
	groups := map[string][]string{"Breaking": {}, "Features": {}, "Fixes": {}, "Other": {}}
	for _, subject := range subjects {
		subject = strings.TrimSpace(subject)
		if subject == "" {
			continue
		}
		prefix, description, hasColon := strings.Cut(subject, ":")
		description = strings.TrimSpace(description)
		group := "Other"
		if hasColon && strings.Contains(prefix, "!") {
			group = "Breaking"
		} else if hasColon && strings.HasPrefix(prefix, "feat") {
			group = "Features"
		} else if hasColon && strings.HasPrefix(prefix, "fix") {
			group = "Fixes"
		}
		if !hasColon || description == "" {
			description = subject
		}
		groups[group] = append(groups[group], description)
	}
	var output strings.Builder
	fmt.Fprintf(&output, "# %s\n", tag)
	for _, group := range []string{"Breaking", "Features", "Fixes", "Other"} {
		if len(groups[group]) == 0 {
			continue
		}
		fmt.Fprintf(&output, "\n## %s\n\n", group)
		for _, item := range groups[group] {
			fmt.Fprintf(&output, "- %s\n", item)
		}
	}
	return output.String()
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
  lark-bridge-release prepare vMAJOR.MINOR.PATCH
  lark-bridge-release tag vMAJOR.MINOR.PATCH
  lark-bridge-release bundle vMAJOR.MINOR.PATCH --base-url https://host/path
`
}

func usageError() error { return errors.New(strings.TrimSpace(releaseUsage())) }
