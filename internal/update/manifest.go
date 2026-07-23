package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	MaxBinaryBytes       int64 = 200 << 20
	MaxReleaseNotesBytes       = 64 << 10
	maxManifestBytes           = 256 << 10
)

type Asset struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Manifest struct {
	SchemaVersion   int              `json:"schema_version"`
	Version         string           `json:"version"`
	PublishedAt     time.Time        `json:"published_at"`
	ReleaseNotesURL string           `json:"release_notes_url"`
	Assets          map[string]Asset `json:"assets"`
}

func (m Manifest) Asset(goos, goarch string) (Asset, bool) {
	asset, ok := m.Assets[goos+"/"+goarch]
	return asset, ok
}

func ParseManifest(r io.Reader) (Manifest, error) {
	var manifest Manifest
	decoder := json.NewDecoder(io.LimitReader(r, maxManifestBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode update manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Manifest{}, errors.New("decode update manifest: trailing JSON value")
	}
	if manifest.SchemaVersion != 1 {
		return Manifest{}, fmt.Errorf("unsupported update manifest schema %d", manifest.SchemaVersion)
	}
	// Accept both stable (x.y.z) and rc (x.y.z-rc.N) versions here. Which one is
	// actually offered is gated later by Client.Check: the stable channel uses
	// CompareStable (rejects rc), the prerelease channel uses
	// CompareAllowingPrerelease. Parsing must not pre-empt that decision.
	if _, _, err := parsePrerelease(manifest.Version); err != nil {
		return Manifest{}, fmt.Errorf("invalid update version: %w", err)
	}
	if manifest.PublishedAt.IsZero() {
		return Manifest{}, errors.New("update manifest published_at is required")
	}
	if err := validateHTTPS(manifest.ReleaseNotesURL); err != nil {
		return Manifest{}, fmt.Errorf("invalid release_notes_url: %w", err)
	}
	if len(manifest.Assets) == 0 {
		return Manifest{}, errors.New("update manifest assets are required")
	}
	for platform, asset := range manifest.Assets {
		parts := strings.Split(platform, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return Manifest{}, fmt.Errorf("invalid asset platform %q", platform)
		}
		if err := validateHTTPS(asset.URL); err != nil {
			return Manifest{}, fmt.Errorf("invalid asset %s URL: %w", platform, err)
		}
		if len(asset.SHA256) != 64 || strings.ToLower(asset.SHA256) != asset.SHA256 {
			return Manifest{}, fmt.Errorf("invalid asset %s sha256", platform)
		}
		for _, c := range asset.SHA256 {
			if !strings.ContainsRune("0123456789abcdef", c) {
				return Manifest{}, fmt.Errorf("invalid asset %s sha256", platform)
			}
		}
		if asset.Size <= 0 || asset.Size > MaxBinaryBytes {
			return Manifest{}, fmt.Errorf("invalid asset %s size", platform)
		}
	}
	return manifest, nil
}

func CompareStable(a, b string) (int, error) {
	// The stable channel only ever offers stable targets, so a (the candidate
	// version from the manifest) must be strict stable SemVer — an rc target
	// can never qualify. But b (the currently running version) may legitimately
	// be an rc build (a bridge running 0.1.4-rc.N on the stable channel), so we
	// compare against its numeric core rather than rejecting it. Otherwise a
	// bridge running an rc on the stable channel makes every update check fail
	// ("暂时无法检查更新") instead of correctly reporting "already up to date".
	av, err := parseStable(a)
	if err != nil {
		return 0, err
	}
	bv, _, err := parsePrerelease(b)
	if err != nil {
		return 0, err
	}
	for i := range av {
		if av[i] < bv[i] {
			return -1, nil
		}
		if av[i] > bv[i] {
			return 1, nil
		}
	}
	return 0, nil
}

// CompareAllowingPrerelease compares two versions that may carry an -rc.N
// prerelease suffix, returning -1, 0, or 1. Precedence follows SemVer: a
// prerelease is lower than its corresponding release (0.1.4-rc.1 < 0.1.4), and
// rc numbers order numerically (0.1.4-rc.1 < 0.1.4-rc.2). Used only by the
// opt-in developer (prerelease) update channel; the stable channel keeps the
// strict CompareStable gate so rc builds can never reach it.
func CompareAllowingPrerelease(a, b string) (int, error) {
	aCore, aPre, err := parsePrerelease(a)
	if err != nil {
		return 0, err
	}
	bCore, bPre, err := parsePrerelease(b)
	if err != nil {
		return 0, err
	}
	for i := range aCore {
		if aCore[i] < bCore[i] {
			return -1, nil
		}
		if aCore[i] > bCore[i] {
			return 1, nil
		}
	}
	// Cores equal: a release (rc == 0) outranks any prerelease of the same core.
	switch {
	case aPre == 0 && bPre == 0:
		return 0, nil
	case aPre == 0:
		return 1, nil
	case bPre == 0:
		return -1, nil
	case aPre < bPre:
		return -1, nil
	case aPre > bPre:
		return 1, nil
	default:
		return 0, nil
	}
}

// parsePrerelease splits "x.y.z" or "x.y.z-rc.N" into its numeric core and an
// rc number (0 means "no prerelease", i.e. a final release). Only the -rc.N
// form is accepted; any other suffix is rejected.
func parsePrerelease(value string) ([3]uint64, uint64, error) {
	core := value
	var rc uint64
	if idx := strings.Index(value, "-"); idx >= 0 {
		core = value[:idx]
		suffix := value[idx+1:]
		rest, ok := strings.CutPrefix(suffix, "rc.")
		if !ok {
			return [3]uint64{}, 0, fmt.Errorf("%q has an unsupported prerelease suffix", value)
		}
		if rest == "" || (len(rest) > 1 && rest[0] == '0') {
			return [3]uint64{}, 0, fmt.Errorf("%q has a non-canonical rc number", value)
		}
		n, err := strconv.ParseUint(rest, 10, 64)
		if err != nil || n == 0 {
			return [3]uint64{}, 0, fmt.Errorf("%q has an invalid rc number", value)
		}
		rc = n
	}
	parsed, err := parseStable(core)
	if err != nil {
		return [3]uint64{}, 0, err
	}
	return parsed, rc, nil
}

func parseStable(value string) ([3]uint64, error) {
	var parsed [3]uint64
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return parsed, fmt.Errorf("%q is not stable SemVer", value)
	}
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return parsed, fmt.Errorf("%q is not canonical stable SemVer", value)
		}
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return parsed, fmt.Errorf("%q is not stable SemVer", value)
		}
		parsed[i] = n
	}
	return parsed, nil
}

func validateHTTPS(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return errors.New("URL must be absolute HTTPS without userinfo")
	}
	return nil
}
