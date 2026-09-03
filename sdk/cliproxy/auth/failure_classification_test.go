package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

type classificationResponseError struct {
	status  int
	headers http.Header
	body    []byte
}

func (e classificationResponseError) Error() string   { return string(e.body) }
func (e classificationResponseError) StatusCode() int { return e.status }
func (e classificationResponseError) ResponseHeaders() http.Header {
	return e.headers.Clone()
}
func (e classificationResponseError) ResponseBody() []byte { return append([]byte(nil), e.body...) }

type genericResponseBodyError struct {
	status int
	body   []byte
}

func (e genericResponseBodyError) Error() string   { return "upstream request failed" }
func (e genericResponseBodyError) StatusCode() int { return e.status }
func (e genericResponseBodyError) ResponseBody() []byte {
	return append([]byte(nil), e.body...)
}

func TestResultErrorClassifiesEdgeAndCredentialFailures(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		headers    http.Header
		body       string
		wantKind   FailureKind
		wantSwitch bool
	}{
		{
			name:       "cloudflare 502",
			status:     http.StatusBadGateway,
			headers:    http.Header{"CF-Ray": []string{"abc"}, "Server": []string{"cloudflare"}},
			body:       `{"detail":"Bad Gateway"}`,
			wantKind:   FailureKindEdgeGateway,
			wantSwitch: true,
		},
		{
			name:       "opaque 502 remains edge failure",
			status:     http.StatusBadGateway,
			body:       `{"detail":"upstream response unavailable"}`,
			wantKind:   FailureKindEdgeGateway,
			wantSwitch: true,
		},
		{
			name:       "502 with invalid token",
			status:     http.StatusBadGateway,
			headers:    http.Header{"CF-Ray": []string{"abc"}},
			body:       `{"error":{"message":"invalid token"}}`,
			wantKind:   FailureKindAuth,
			wantSwitch: false,
		},
		{
			name:       "502 with authentication header",
			status:     http.StatusBadGateway,
			headers:    http.Header{"WWW-Authenticate": []string{"Bearer realm=upstream"}},
			body:       `{"detail":"Bad Gateway"}`,
			wantKind:   FailureKindAuth,
			wantSwitch: false,
		},
		{
			name:       "502 websocket upstream disconnect",
			status:     http.StatusBadGateway,
			body:       `{"error":{"message":"upstream websocket disconnected before response.completed: websocket: close 1006 (abnormal closure): unexpected EOF","type":"server_error","code":"websocket_upstream_disconnected"}}`,
			wantKind:   FailureKindEdgeGateway,
			wantSwitch: true,
		},
		{
			name:       "quota 429",
			status:     http.StatusTooManyRequests,
			body:       `{"error":{"type":"usage_limit_reached"}}`,
			wantKind:   FailureKindQuota,
			wantSwitch: false,
		},
		{
			name:       "explicit auth signal wins over 429",
			status:     http.StatusTooManyRequests,
			body:       `{"error":{"message":"invalid token"}}`,
			wantKind:   FailureKindAuth,
			wantSwitch: false,
		},
		{
			name:       "generic 429 is quota",
			status:     http.StatusTooManyRequests,
			body:       `Too Many Requests`,
			wantKind:   FailureKindQuota,
			wantSwitch: false,
		},
		{
			name:       "generic 503 remains account active",
			status:     http.StatusServiceUnavailable,
			body:       `service unavailable`,
			wantKind:   FailureKindCapacity,
			wantSwitch: true,
		},
		{
			name:       "provider overload 529 remains account active",
			status:     529,
			body:       ``,
			wantKind:   FailureKindCapacity,
			wantSwitch: true,
		},
		{
			name:       "request format",
			status:     http.StatusBadRequest,
			body:       `{"error":{"code":"invalid_encrypted_content"}}`,
			wantKind:   FailureKindRequestFault,
			wantSwitch: false,
		},
	}

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{ErrorHandling: internalconfig.DefaultErrorHandlingConfig()})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rerr := resultErrorFromError(classificationResponseError{
				status: tt.status, headers: tt.headers, body: []byte(tt.body),
			})
			if got := errorFailureKind(rerr); got != tt.wantKind {
				t.Fatalf("failure kind = %q, want %q", got, tt.wantKind)
			}
			if got := isTemporaryFailureResult(rerr); got != tt.wantSwitch {
				t.Fatalf("temporary result = %v, want %v", got, tt.wantSwitch)
			}
			if tt.wantKind == FailureKindEdgeGateway || tt.wantKind == FailureKindCapacity {
				if !manager.keepsTransientAccountActive(rerr) {
					t.Fatalf("default config should keep %s account active", tt.wantKind)
				}
			}
		})
	}
}

func TestRequestParameterErrorsDoNotRotateCredentials(t *testing.T) {
	for _, message := range []string{
		"response item not found",
		"Invalid type for input content",
		"invalid_encrypted_content",
	} {
		t.Run(message, func(t *testing.T) {
			err := classificationResponseError{status: http.StatusBadGateway, body: []byte(message)}
			if !isRequestInvalidError(err) {
				t.Fatalf("isRequestInvalidError(%q) = false", message)
			}
		})
	}
}

func TestRequestParameterErrorsInspectResponseBodyWhenMessageIsGeneric(t *testing.T) {
	for _, message := range []string{
		`{"error":{"code":"invalid_encrypted_content"}}`,
		"response item not found",
	} {
		t.Run(message, func(t *testing.T) {
			err := genericResponseBodyError{status: http.StatusBadGateway, body: []byte(message)}
			if !isRequestInvalidError(err) {
				t.Fatalf("isRequestInvalidError(%q) = false", message)
			}
			rerr := resultErrorFromError(err)
			if got := errorFailureKind(rerr); got != FailureKindRequestFault {
				t.Fatalf("failure kind = %q, want %q", got, FailureKindRequestFault)
			}
		})
	}
}

func TestTransportFailuresUseTemporaryHandling(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{ErrorHandling: internalconfig.DefaultErrorHandlingConfig()})
	for _, message := range []string{
		"dial tcp 192.0.2.1:443: connect: connection refused",
		"read tcp: i/o timeout",
		"http2: stream closed",
		"write: broken pipe",
	} {
		t.Run(message, func(t *testing.T) {
			rerr := resultErrorFromError(genericResponseBodyError{body: []byte(message)})
			if got := errorFailureKind(rerr); got != FailureKindTransport {
				t.Fatalf("failure kind = %q, want %q", got, FailureKindTransport)
			}
			if !isTemporaryFailureResult(rerr) {
				t.Fatal("transport failure should be temporary")
			}
			if !manager.keepsTransientAccountActive(rerr) {
				t.Fatal("default temporary policy should keep transport failures active")
			}
		})
	}
}

func TestTemporaryFailuresDoNotFreezeRuntimeAccount(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		ErrorHandling: internalconfig.DefaultErrorHandlingConfig(),
	})
	auth, err := manager.Register(context.Background(), &Auth{
		ID:       "temporary-edge-auth",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{"selection_error_freeze_seconds": 60},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	rerr := resultErrorFromError(classificationResponseError{
		status:  http.StatusBadGateway,
		headers: http.Header{"CF-Ray": []string{"fixture"}},
		body:    []byte("Bad Gateway"),
	})
	manager.MarkResult(context.Background(), Result{
		AuthID:  auth.ID,
		Model:   "gpt-5.6",
		Success: false,
		Error:   rerr,
	})

	snapshot, ok := manager.GetByID(auth.ID)
	if !ok || snapshot == nil {
		t.Fatal("temporary edge auth was not retained")
	}
	if frozen := snapshot.RuntimeLimitSnapshot(time.Now()).FrozenUntil; !frozen.IsZero() {
		t.Fatalf("temporary edge failure froze runtime account until %v", frozen)
	}
}

func TestTransientRetryActionUsesCodeDefaultsAndSingleSwitch(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	// A runtime config supplied by an embedder cannot override the code-owned
	// temporary failure policy.
	manager.SetConfig(&internalconfig.Config{ErrorHandling: internalconfig.ErrorHandlingConfig{
		TemporaryErrorStrategy:           internalconfig.TemporaryErrorNoSwitch,
		TemporaryErrorMaxWaitSeconds:     0,
		RetryBeforeFirstOutputOnly:       false,
		TransientErrorsKeepAccountActive: false,
	}})
	err := resultErrorFromError(classificationResponseError{
		status:  http.StatusBadGateway,
		headers: http.Header{"Retry-After": []string{"1"}},
		body:    []byte("Bad Gateway"),
	})
	started := time.Now()
	switches := 0
	got, errWait := manager.transientRetryAction(context.Background(), err, &switches)
	if errWait != nil {
		t.Fatalf("transientRetryAction() error = %v", errWait)
	}
	if !got || switches != 1 {
		t.Fatalf("switch = %v, count = %d, want one code-default switch", got, switches)
	}
	if elapsed := time.Since(started); elapsed < time.Second {
		t.Fatalf("waited %v, want at least 1s", elapsed)
	}
	if got, _ := manager.transientRetryAction(context.Background(), err, &switches); got {
		t.Fatal("second temporary failure should not switch another credential")
	}
}
