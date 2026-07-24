package update

import (
	"context"
	"errors"
)

type PreparedUpdate interface {
	Replace() error
	Restart([]string, []string) error
	Abort()
}

type Manager struct {
	Client    *Client
	Installer Installer
}

func (m *Manager) Check(ctx context.Context, currentVersion string) (CheckResult, error) {
	if m == nil || m.Client == nil {
		return CheckResult{}, errors.New("update client is unavailable")
	}
	return m.Client.Check(ctx, currentVersion)
}

func (m *Manager) Refresh(ctx context.Context, currentVersion string) (CheckResult, error) {
	if m == nil || m.Client == nil {
		return CheckResult{}, errors.New("update client is unavailable")
	}
	m.Client.Invalidate()
	return m.Client.Check(ctx, currentVersion)
}

func (m *Manager) ReleaseNotes(ctx context.Context, manifest Manifest) (string, error) {
	if m == nil || m.Client == nil {
		return "", errors.New("update client is unavailable")
	}
	return m.Client.ReleaseNotes(ctx, manifest)
}

// AggregatedReleaseNotes returns the release notes for every version between
// currentVersion (exclusive) and the target manifest's version (inclusive),
// newest-first. onSkip, if non-nil, is invoked for each intermediate version
// whose notes could not be fetched (they are skipped, not fatal).
func (m *Manager) AggregatedReleaseNotes(ctx context.Context, currentVersion string, manifest Manifest, onSkip func(version string, err error)) (string, error) {
	if m == nil || m.Client == nil {
		return "", errors.New("update client is unavailable")
	}
	return m.Client.AggregatedReleaseNotes(ctx, currentVersion, manifest, onSkip)
}

func (m *Manager) Prepare(ctx context.Context, asset Asset) (PreparedUpdate, error) {
	if m == nil || m.Client == nil {
		return nil, errors.New("update client is unavailable")
	}
	installer := m.Installer
	if installer.Stage == nil {
		installer.Stage = m.Client.Stage
	}
	return installer.Prepare(ctx, asset)
}
