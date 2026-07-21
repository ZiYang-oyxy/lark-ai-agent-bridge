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
	if _, err := parseStable(manifest.Version); err != nil {
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
	av, err := parseStable(a)
	if err != nil {
		return 0, err
	}
	bv, err := parseStable(b)
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
