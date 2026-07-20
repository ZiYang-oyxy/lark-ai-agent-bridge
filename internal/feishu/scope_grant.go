package feishu

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/scene/registration"
)

type ScopeGrant struct {
	URL       string
	ExpiresAt time.Time
	Done      <-chan error
}

type ScopeGrantProvider interface {
	Begin(context.Context, string, []string) (ScopeGrant, error)
}

type registrationRunner func(context.Context, *registration.Options) (*registration.RegisterAppResult, error)

type SDKScopeGrantProvider struct {
	register registrationRunner
	now      func() time.Time
}

func NewSDKScopeGrantProvider() *SDKScopeGrantProvider {
	return &SDKScopeGrantProvider{register: registration.RegisterApp, now: time.Now}
}

func (p *SDKScopeGrantProvider) Begin(ctx context.Context, appID string, scopes []string) (ScopeGrant, error) {
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return ScopeGrant{}, errors.New("feishu scope grant: app id is required")
	}
	cleanScopes := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			return ScopeGrant{}, errors.New("feishu scope grant: scopes must not contain blanks")
		}
		cleanScopes = append(cleanScopes, scope)
	}
	if len(cleanScopes) == 0 {
		return ScopeGrant{}, errors.New("feishu scope grant: at least one scope is required")
	}
	register := p.register
	if register == nil {
		register = registration.RegisterApp
	}
	now := p.now
	if now == nil {
		now = time.Now
	}
	type readyResult struct {
		url       string
		expiresAt time.Time
		err       error
	}
	ready := make(chan readyResult, 1)
	done := make(chan error, 1)
	go func() {
		published := false
		_, err := register(ctx, &registration.Options{
			AppID: appID,
			Addons: &registration.AppAddons{
				Scopes: registration.AppAddonsScopes{Tenant: cleanScopes},
			},
			OnQRCode: func(info *registration.QRCodeInfo) {
				if info == nil || published {
					return
				}
				published = true
				ready <- readyResult{url: info.URL, expiresAt: now().Add(time.Duration(info.ExpireIn) * time.Second)}
			},
		})
		if !published {
			ready <- readyResult{err: err}
			close(done)
			return
		}
		done <- err
		close(done)
	}()

	result := <-ready
	if result.err != nil {
		return ScopeGrant{}, result.err
	}
	return ScopeGrant{URL: result.url, ExpiresAt: result.expiresAt, Done: done}, nil
}
