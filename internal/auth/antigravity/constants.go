// Package antigravity provides OAuth2 authentication functionality for the Antigravity provider.
package antigravity

import (
	"fmt"
	"os"
	"strings"
)

// OAuth client credentials and configuration
const (
	OAuthClientIDEnv     = "ANTIGRAVITY_OAUTH_CLIENT_ID"
	OAuthClientSecretEnv = "ANTIGRAVITY_OAUTH_CLIENT_SECRET"
	CallbackPort         = 51121
)

// OAuthCredentials reads the Antigravity Google OAuth application credentials at
// request time. Reading lazily keeps values loaded from a runtime .env file visible.
func OAuthCredentials() (clientID, clientSecret string, err error) {
	clientID = strings.TrimSpace(os.Getenv(OAuthClientIDEnv))
	clientSecret = strings.TrimSpace(os.Getenv(OAuthClientSecretEnv))

	missing := make([]string, 0, 2)
	if clientID == "" {
		missing = append(missing, OAuthClientIDEnv)
	}
	if clientSecret == "" {
		missing = append(missing, OAuthClientSecretEnv)
	}
	if len(missing) > 0 {
		return "", "", fmt.Errorf("antigravity OAuth credentials are not configured; set %s", strings.Join(missing, " and "))
	}
	return clientID, clientSecret, nil
}

// Scopes defines the OAuth scopes required for Antigravity authentication
var Scopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

// OAuth2 endpoints for Google authentication
const (
	TokenEndpoint    = "https://oauth2.googleapis.com/token"
	AuthEndpoint     = "https://accounts.google.com/o/oauth2/v2/auth"
	UserInfoEndpoint = "https://www.googleapis.com/oauth2/v2/userinfo?alt=json"
)

// Antigravity API configuration
const (
	APIEndpoint      = "https://cloudcode-pa.googleapis.com"
	DailyAPIEndpoint = "https://daily-cloudcode-pa.googleapis.com"
	APIVersion       = "v1internal"
)
