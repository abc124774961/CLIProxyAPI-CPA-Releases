package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestPrepareCodexQuotaOverdraftBodyRequiresTrackedEligibleRequest(t *testing.T) {
	now := time.Now().UTC()
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetConfig(&config.Config{Codex: config.CodexConfig{
		Overdraft: config.CodexOverdraftConfig{Enabled: true},
	}})
	auth := &cliproxyauth.Auth{
		ID:       "codex-overdraft-executor-test",
		Provider: "codex",
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"plan_type": "team",
		},
		Metadata: map[string]any{"access_token": "access-token"},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	if _, accepted, errUpdate := manager.UpdateCodexQuotaSnapshot(auth.ID, "gpt-5.4", cliproxyauth.CodexQuotaSnapshot{
		UsedRatio: 0.95,
		Window:    "five_hour",
		SampledAt: now.Add(-time.Minute),
		ExpiresAt: now.Add(time.Minute),
		ResetAt:   now.Add(time.Hour),
	}); errUpdate != nil || !accepted {
		t.Fatalf("UpdateCodexQuotaSnapshot() accepted=%t error=%v", accepted, errUpdate)
	}
	selected, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("registered auth was not found")
	}
	body := []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":[]}]}`)
	ctx := cliproxyauth.WithCodexQuotaOverdraftTracking(context.Background())
	updated := prepareCodexQuotaOverdraftBody(ctx, selected, "gpt-5.4", cliproxyexecutor.Options{}, body)
	if !codexQuotaOverdraftBodyHasInjection(updated) {
		t.Fatalf("tracked eligible body was not injected: %s", updated)
	}
	if !cliproxyauth.CodexQuotaOverdraftWasInjected(ctx, auth.ID) {
		t.Fatal("injected auth was not recorded in request context")
	}

	compact := prepareCodexQuotaOverdraftBody(ctx, selected, "gpt-5.4", cliproxyexecutor.Options{Alt: "responses/compact"}, body)
	if !bytes.Equal(compact, body) {
		t.Fatalf("compact body changed: %s", compact)
	}
	untracked := prepareCodexQuotaOverdraftBody(context.Background(), selected, "gpt-5.4", cliproxyexecutor.Options{}, body)
	if !bytes.Equal(untracked, body) {
		t.Fatalf("untracked body changed: %s", untracked)
	}
}

func TestCodexExecutorExecuteInjectsTrackedCodexQuotaOverdraftBody(t *testing.T) {
	requestBody := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read request body: %v", errRead)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requestBody <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{
		Codex: config.CodexConfig{Overdraft: config.CodexOverdraftConfig{Enabled: true}},
		SDKConfig: config.SDKConfig{
			DisableImageGeneration: config.DisableImageGenerationAll,
		},
	})
	auth := &cliproxyauth.Auth{
		ID:       "codex-overdraft-http-test",
		Provider: "codex",
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"plan_type": "team",
			"base_url":  server.URL,
		},
		Metadata: map[string]any{"access_token": "access-token"},
	}
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetConfig(&config.Config{Codex: config.CodexConfig{Overdraft: config.CodexOverdraftConfig{Enabled: true}}})
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	if _, accepted, errUpdate := manager.UpdateCodexQuotaSnapshot(auth.ID, "gpt-5.4", cliproxyauth.CodexQuotaSnapshot{
		UsedRatio: 0.95,
		Window:    "five_hour",
		SampledAt: time.Now().UTC().Add(-time.Minute),
		ExpiresAt: time.Now().UTC().Add(time.Minute),
		ResetAt:   time.Now().UTC().Add(time.Hour),
	}); errUpdate != nil || !accepted {
		t.Fatalf("UpdateCodexQuotaSnapshot() accepted=%t error=%v", accepted, errUpdate)
	}
	selected, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("registered auth was not found")
	}
	_, errExecute := executor.Execute(
		cliproxyauth.WithCodexQuotaOverdraftTracking(context.Background()),
		selected,
		cliproxyexecutor.Request{
			Model:   "gpt-5.4",
			Format:  sdktranslator.FormatCodex,
			Payload: []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":[]}]}`),
		},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatCodex},
	)
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	select {
	case body := <-requestBody:
		if !codexQuotaOverdraftBodyHasInjection(body) {
			t.Fatalf("upstream body was not injected: %s", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for upstream request")
	}
}

func TestCodexWebsocketsExecuteInjectsTrackedCodexQuotaOverdraftBody(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	capturedBody := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !websocket.IsWebSocketUpgrade(r) {
			http.Error(w, "expected websocket upgrade", http.StatusBadRequest)
			return
		}
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade websocket: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()
		_, body, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("read websocket request: %v", errRead)
			return
		}
		capturedBody <- body
		completed := []byte(`{"type":"response.completed","response":{"id":"resp-overdraft-ws","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Errorf("write websocket response: %v", errWrite)
		}
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, nil, nil)
	auth := &cliproxyauth.Auth{
		ID:       "codex-overdraft-websocket-test",
		Provider: "codex",
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"plan_type": "team",
			"base_url":  server.URL,
		},
		Metadata: map[string]any{
			"access_token": "access-token",
			"websockets":   true,
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	now := time.Now().UTC()
	if _, accepted, errUpdate := manager.UpdateCodexQuotaSnapshot(auth.ID, "gpt-5.4", cliproxyauth.CodexQuotaSnapshot{
		UsedRatio: 0.95,
		Window:    "five_hour",
		SampledAt: now.Add(-time.Minute),
		ExpiresAt: now.Add(time.Minute),
		ResetAt:   now.Add(time.Hour),
	}); errUpdate != nil || !accepted {
		t.Fatalf("UpdateCodexQuotaSnapshot() accepted=%t error=%v", accepted, errUpdate)
	}
	selected, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("registered auth was not found")
	}

	executor := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{
		DisableImageGeneration: config.DisableImageGenerationAll,
	}})
	executor.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	_, errExecute := executor.Execute(
		cliproxyauth.WithCodexQuotaOverdraftTracking(context.Background()),
		selected,
		cliproxyexecutor.Request{
			Model:   "gpt-5.4",
			Format:  sdktranslator.FormatCodex,
			Payload: []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":[]}]}`),
		},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatCodex},
	)
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	select {
	case body := <-capturedBody:
		if !codexQuotaOverdraftBodyHasInjection(body) {
			t.Fatalf("websocket body was not injected: %s", body)
		}
		if gjson.GetBytes(body, "type").String() != "response.create" {
			t.Fatalf("websocket request type = %q, want response.create", gjson.GetBytes(body, "type").String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for websocket request")
	}
}

func TestCodexWebsocketFallbackContextPreservesOverdraftTracking(t *testing.T) {
	ctx := cliproxyauth.WithCodexQuotaOverdraftTracking(context.Background())
	if !cliproxyauth.CodexQuotaOverdraftTrackingEnabled(codexWebsocketFallbackContext(ctx)) {
		t.Fatal("websocket HTTP fallback dropped overdraft tracking context")
	}
}

func TestInjectCodexQuotaOverdraftMatchesReferenceShapeAndRequiresTrailingUser(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	updated, changed, errInject := injectCodexQuotaOverdraft(body)
	if errInject != nil || !changed {
		t.Fatalf("injectCodexQuotaOverdraft() changed=%t error=%v", changed, errInject)
	}
	input := gjson.GetBytes(updated, "input")
	if !input.IsArray() || len(input.Array()) != 3 {
		t.Fatalf("input = %s, want original plus call/output pair", input.Raw)
	}
	if input.Get("1.type").String() != "custom_tool_call" || input.Get("2.type").String() != "custom_tool_call_output" {
		t.Fatalf("injected input = %s", input.Raw)
	}
	callID := input.Get("1.call_id").String()
	if !strings.HasPrefix(callID, "call_sub2api_overdraft_") || input.Get("2.call_id").String() != callID {
		t.Fatalf("call IDs = %q and %q", callID, input.Get("2.call_id").String())
	}
	if input.Get("1.name").String() != "exec" {
		t.Fatalf("injected tool name = %q, want exec", input.Get("1.name").String())
	}
	if input.Get("1.input").String() == "" || !strings.Contains(input.Get("1.input").String(), `tools.exec_command`) {
		t.Fatalf("injected tool input = %q, want exec_command payload", input.Get("1.input").String())
	}
	if input.Get("2.output.0.type").String() != "input_text" {
		t.Fatalf("injected output = %s, want input_text array", input.Get("2.output").Raw)
	}

	repeated, changedAgain, errRepeat := injectCodexQuotaOverdraft(updated)
	if errRepeat != nil || changedAgain || string(repeated) != string(updated) {
		t.Fatalf("repeat injection changed=%t error=%v body=%s", changedAgain, errRepeat, repeated)
	}

	continuedBody := []byte(`{"input":[{"type":"message","role":"user","content":[]},{"type":"message","role":"assistant","content":[]}]}`)
	unchanged, changedContinued, errContinued := injectCodexQuotaOverdraft(continuedBody)
	if errContinued != nil || changedContinued || string(unchanged) != string(continuedBody) {
		t.Fatalf("continued request injection changed=%t error=%v body=%s", changedContinued, errContinued, unchanged)
	}
}

func TestInjectCodexQuotaOverdraftSkipsOversizedPayload(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","role":"user","content":[]}]}`)
	if len(body) > codexQuotaOverdraftMaxBodyBytes {
		t.Fatal("test body unexpectedly exceeds the injection limit")
	}
	body = append(body, bytes.Repeat([]byte{' '}, codexQuotaOverdraftMaxBodyBytes-len(body)+1)...)
	updated, changed, errInject := injectCodexQuotaOverdraft(body)
	if errInject != nil || changed || string(updated) != string(body) {
		t.Fatalf("oversized injection changed=%t error=%v", changed, errInject)
	}
}

func TestClassifyCodexQuotaOverdraftResponse(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		want       string
	}{
		{
			name:       "completed sse",
			statusCode: 200,
			body:       `data: {"type":"response.completed","response":{"status":"completed"}}` + "\n\n",
			want:       "available",
		},
		{
			name:       "output item sse",
			statusCode: 200,
			body:       `data: {"type":"response.output_item.done","item":{"type":"message"}}` + "\n\n",
			want:       "available",
		},
		{
			name:       "quota error",
			statusCode: 429,
			body:       `{"error":{"type":"usage_limit_reached","message":"quota exhausted"}}`,
			want:       "quota_limited",
		},
		{
			name:       "ordinary 429",
			statusCode: 429,
			body:       `{"error":{"type":"rate_limit_error","message":"try again"}}`,
			want:       "inconclusive",
		},
		{
			name:       "auth error",
			statusCode: 401,
			body:       `{"error":{"type":"authentication_error"}}`,
			want:       "authentication_failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyCodexQuotaOverdraftResponse(tt.statusCode, []byte(tt.body)); got != tt.want {
				t.Fatalf("classifyCodexQuotaOverdraftResponse() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProbeCodexQuotaOverdraftUsesResponsesAndAuth(t *testing.T) {
	var gotPath string
	var gotAuthorization string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthorization = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"))
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  server.URL,
		},
		Metadata: map[string]any{"access_token": "access-token"},
	}
	result := NewCodexExecutor(&config.Config{}).ProbeCodexQuotaOverdraft(t.Context(), auth, "gpt-5.4")
	if result.Status != "available" || result.ReasonCode != "model_response_ok" {
		t.Fatalf("probe result = %#v, want available/model_response_ok", result)
	}
	if gotPath != "/responses" {
		t.Fatalf("probe path = %q, want /responses", gotPath)
	}
	if gotAuthorization != "Bearer access-token" {
		t.Fatalf("probe authorization = %q, want bearer token", gotAuthorization)
	}
	var payload map[string]any
	if errJSON := json.Unmarshal(gotBody, &payload); errJSON != nil {
		t.Fatalf("probe request is not JSON: %v", errJSON)
	}
	input, ok := payload["input"].([]any)
	if !ok || len(input) != 3 {
		t.Fatalf("probe input = %#v, want user plus tool pair", payload["input"])
	}
	if gjson.GetBytes(gotBody, "input.1.name").String() != codexQuotaOverdraftProbeName {
		t.Fatalf("probe call = %s", gjson.GetBytes(gotBody, "input.1").Raw)
	}
}
