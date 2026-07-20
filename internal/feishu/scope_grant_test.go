package feishu

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/scene/registration"
)

func TestSDKScopeGrantProviderPublishesQRCodeBeforeRegistrationCompletes(t *testing.T) {
	finished := make(chan struct{})
	provider := &SDKScopeGrantProvider{
		now: func() time.Time { return time.Unix(1_700_000_000, 0) },
		register: func(ctx context.Context, opts *registration.Options) (*registration.RegisterAppResult, error) {
			if opts.AppID != "cli_test" {
				t.Fatalf("AppID = %q", opts.AppID)
			}
			if got := opts.Addons.Scopes.Tenant; !reflect.DeepEqual(got, []string{"im:message.group_msg"}) {
				t.Fatalf("tenant scopes = %#v", got)
			}
			opts.OnQRCode(&registration.QRCodeInfo{URL: "https://example.test/grant", ExpireIn: 60})
			<-finished
			return &registration.RegisterAppResult{}, nil
		},
	}

	grant, err := provider.Begin(context.Background(), "cli_test", []string{"im:message.group_msg"})
	if err != nil {
		t.Fatal(err)
	}
	if grant.URL != "https://example.test/grant" {
		t.Fatalf("URL = %q", grant.URL)
	}
	if want := time.Unix(1_700_000_060, 0); !grant.ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt = %v, want %v", grant.ExpiresAt, want)
	}

	close(finished)
	if err := <-grant.Done; err != nil {
		t.Fatalf("Done error = %v", err)
	}
	if _, ok := <-grant.Done; ok {
		t.Fatal("Done channel remained open")
	}
}

func TestSDKScopeGrantProviderReturnsEarlyRegistrationError(t *testing.T) {
	want := errors.New("registration unavailable")
	provider := &SDKScopeGrantProvider{
		register: func(context.Context, *registration.Options) (*registration.RegisterAppResult, error) {
			return nil, want
		},
	}

	_, err := provider.Begin(context.Background(), "cli_test", []string{"im:message.group_msg"})
	if !errors.Is(err, want) {
		t.Fatalf("Begin error = %v, want %v", err, want)
	}
}

func TestSDKScopeGrantProviderValidatesInput(t *testing.T) {
	provider := &SDKScopeGrantProvider{}
	for _, tc := range []struct {
		name   string
		appID  string
		scopes []string
	}{
		{name: "empty app id", scopes: []string{"im:message.group_msg"}},
		{name: "empty scopes", appID: "cli_test"},
		{name: "blank scope", appID: "cli_test", scopes: []string{" "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := provider.Begin(context.Background(), tc.appID, tc.scopes); err == nil {
				t.Fatal("Begin error = nil")
			}
		})
	}
}
