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
