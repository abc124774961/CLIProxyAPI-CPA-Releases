package executor

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	antigravityauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestAntigravityRefreshUsesRuntimeOAuthCredentials(t *testing.T) {
	t.Setenv(antigravityauth.OAuthClientIDEnv, "runtime-client-id")
	t.Setenv(antigravityauth.OAuthClientSecretEnv, "runtime-client-secret")

	exec := NewAntigravityExecutor(&config.Config{})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		form, errParse := url.ParseQuery(string(body))
		if errParse != nil {
			t.Fatalf("parse request body: %v", errParse)
		}
		if got := form.Get("client_id"); got != "runtime-client-id" {
			t.Fatalf("client_id = %q", got)
		}
		if got := form.Get("client_secret"); got != "runtime-client-secret" {
			t.Fatalf("client_secret = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"access","refresh_token":"refresh","expires_in":3600}`)),
		}, nil
	}))

	token, errRefresh := exec.refreshTokenSingleFlight(ctx, &cliproxyauth.Auth{}, "refresh-token")
	if errRefresh != nil {
		t.Fatalf("refreshTokenSingleFlight() error = %v", errRefresh)
	}
	if token.AccessToken != "access" || token.RefreshToken != "refresh" {
		t.Fatalf("unexpected token response: %#v", token)
	}
}

func TestAntigravityRefreshStopsBeforeNetworkWhenOAuthCredentialsMissing(t *testing.T) {
	t.Setenv(antigravityauth.OAuthClientIDEnv, "")
	t.Setenv(antigravityauth.OAuthClientSecretEnv, "")

	exec := NewAntigravityExecutor(&config.Config{})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected OAuth request to %s", req.URL)
		return nil, nil
	}))
	_, errRefresh := exec.refreshTokenSingleFlight(ctx, &cliproxyauth.Auth{}, "refresh-token")
	if errRefresh == nil {
		t.Fatal("refreshTokenSingleFlight() error = nil, want missing-credentials error")
	}
	message := errRefresh.Error()
	if !strings.Contains(message, antigravityauth.OAuthClientIDEnv) || !strings.Contains(message, antigravityauth.OAuthClientSecretEnv) {
		t.Fatalf("error %q does not identify missing variables", message)
	}
}
