package access

import (
	"context"
	"errors"
	"time"
)

const OwnerRefreshInterval = 30 * time.Minute

type OwnerSource interface {
	GetOwner(context.Context, string) (string, error)
}

type OwnerRefreshController struct {
	Controls *RuntimeControls
	Source   OwnerSource
	AppID    string
	Interval time.Duration
	OnError  func(error)
}

func RefreshOwner(ctx context.Context, controls *RuntimeControls, source OwnerSource, appID string) error {
	if controls == nil || source == nil {
		return errors.New("access: owner refresh unavailable")
	}
	ownerID, err := source.GetOwner(ctx, appID)
	if err != nil {
		controls.OwnerRefreshFailed(err)
		return err
	}
	if ownerID == "" {
		err = errors.New("application owner missing from API response")
		controls.OwnerRefreshFailed(err)
		return err
	}
	controls.OwnerRefreshSucceeded(ownerID)
	return nil
}

func (c OwnerRefreshController) Run(ctx context.Context) {
	interval := c.Interval
	if interval <= 0 {
		interval = OwnerRefreshInterval
	}
	refresh := func() {
		if err := RefreshOwner(ctx, c.Controls, c.Source, c.AppID); err != nil && c.OnError != nil {
			c.OnError(err)
		}
	}
	refresh()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}
