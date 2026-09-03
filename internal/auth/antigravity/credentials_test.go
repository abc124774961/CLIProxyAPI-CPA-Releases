package antigravity

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func setTestOAuthCredentials(t *testing.T) {
	t.Helper()
	t.Setenv(OAuthClientIDEnv, "test-antigravity-client-id")
	t.Setenv(OAuthClientSecretEnv, "test-antigravity-client-secret")
}

func TestOAuthCredentialsReadsRuntimeEnvironment(t *testing.T) {
	t.Setenv(OAuthClientIDEnv, "  runtime-client-id  ")
	t.Setenv(OAuthClientSecretEnv, "  runtime-client-secret  ")

	clientID, clientSecret, errCredentials := OAuthCredentials()
	if errCredentials != nil {
		t.Fatalf("OAuthCredentials() error = %v", errCredentials)
	}
	if clientID != "runtime-client-id" || clientSecret != "runtime-client-secret" {
		t.Fatalf("OAuthCredentials() = %q/%q", clientID, clientSecret)
	}
}

func TestOAuthCredentialsReportsEveryMissingVariable(t *testing.T) {
	t.Setenv(OAuthClientIDEnv, "")
	t.Setenv(OAuthClientSecretEnv, "")

	_, _, errCredentials := OAuthCredentials()
	if errCredentials == nil {
		t.Fatal("OAuthCredentials() error = nil, want missing-variable error")
	}
	message := errCredentials.Error()
	for _, envName := range []string{OAuthClientIDEnv, OAuthClientSecretEnv} {
		if !strings.Contains(message, envName) {
			t.Fatalf("OAuthCredentials() error %q does not name %s", message, envName)
		}
	}
}

func TestBuildAuthURLUsesRuntimeClientID(t *testing.T) {
	setTestOAuthCredentials(t)
	auth := NewAntigravityAuth(nil, nil)

	authURL, errBuild := auth.BuildAuthURL("test-state", "http://localhost:51121/oauth-callback")
	if errBuild != nil {
		t.Fatalf("BuildAuthURL() error = %v", errBuild)
	}
	parsed, errParse := url.Parse(authURL)
	if errParse != nil {
		t.Fatalf("url.Parse() error = %v", errParse)
	}
	query := parsed.Query()
	if got := query.Get("client_id"); got != "test-antigravity-client-id" {
		t.Fatalf("client_id = %q", got)
	}
	if got := query.Get("state"); got != "test-state" {
		t.Fatalf("state = %q", got)
	}
}

func TestExchangeCodeForTokensUsesRuntimeCredentials(t *testing.T) {
	setTestOAuthCredentials(t)
	auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		form, errParse := url.ParseQuery(string(body))
		if errParse != nil {
			t.Fatalf("parse request body: %v", errParse)
		}
		if got := form.Get("client_id"); got != "test-antigravity-client-id" {
			t.Fatalf("client_id = %q", got)
		}
		if got := form.Get("client_secret"); got != "test-antigravity-client-secret" {
			t.Fatalf("client_secret = %q", got)
		}
		return jsonResponse(`{"access_token":"access","refresh_token":"refresh","expires_in":3600,"token_type":"Bearer"}`), nil
	})})

	token, errExchange := auth.ExchangeCodeForTokens(context.Background(), "code", "http://localhost/callback")
	if errExchange != nil {
		t.Fatalf("ExchangeCodeForTokens() error = %v", errExchange)
	}
	if token.AccessToken != "access" || token.RefreshToken != "refresh" {
		t.Fatalf("unexpected token response: %#v", token)
	}
}
