package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestCodexQuotaOverdraftAuthEligibleOnlyForTeamOAuth(t *testing.T) {
	tests := []struct {
		name string
		auth *Auth
		want bool
	}{
		{
			name: "team oauth",
			auth: &Auth{
				Provider:   "codex",
				Attributes: map[string]string{"auth_kind": "oauth", "plan_type": "team"},
				Metadata:   map[string]any{"access_token": "access-token"},
			},
			want: true,
		},
		{
			name: "business oauth",
			auth: &Auth{
				Provider:   "codex",
				Attributes: map[string]string{"auth_kind": "oauth", "plan_type": "business"},
				Metadata:   map[string]any{"access_token": "access-token"},
			},
			want: true,
		},
		{
			name: "plus oauth",
			auth: &Auth{
				Provider:   "codex",
				Attributes: map[string]string{"auth_kind": "oauth", "plan_type": "plus"},
				Metadata:   map[string]any{"access_token": "access-token"},
			},
			want: false,
		},
		{
			name: "api key",
			auth: &Auth{
				Provider:   "codex",
				Attributes: map[string]string{"auth_kind": "api_key", "plan_type": "team", "api_key": "sk-test"},
			},
			want: false,
		},
		{
			name: "non codex oauth",
			auth: &Auth{
				Provider:   "claude",
				Attributes: map[string]string{"auth_kind": "oauth", "plan_type": "team"},
				Metadata:   map[string]any{"access_token": "access-token"},
			},
			want: false,
		},
		{
			name: "shadow team oauth",
			auth: &Auth{
				Provider:   "codex",
				Attributes: map[string]string{"auth_kind": "oauth", "plan_type": "team", "shadow": "true"},
				Metadata:   map[string]any{"access_token": "access-token"},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CodexQuotaOverdraftEligible(tt.auth); got != tt.want {
				t.Fatalf("CodexQuotaOverdraftEligible() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestCodexQuotaOverdraftSignalBuildsCycleKeyFromWindowAndReset(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	resetAt := now.Add(5 * time.Hour)

	tests := []struct {
		name       string
		snapshot   CodexQuotaSnapshot
		wantWindow string
		wantCycle  string
	}{
		{
			name:       "five hour",
			snapshot:   CodexQuotaSnapshot{UsedRatio: 1, Window: "five_hour", ResetAt: resetAt},
			wantWindow: "5h",
			wantCycle:  "5h:" + formatUnix(resetAt),
		},
		{
			name:       "weekly",
			snapshot:   CodexQuotaSnapshot{UsedRatio: 1, Window: "weekly", ResetAt: resetAt},
			wantWindow: "7d",
			wantCycle:  "7d:" + formatUnix(resetAt),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signal, exhausted := codexQuotaOverdraftSignalFromSnapshot(tt.snapshot, now)
			if !exhausted {
				t.Fatal("signal exhausted = false, want true")
			}
			if signal.Window != tt.wantWindow || signal.CycleKey != tt.wantCycle {
				t.Fatalf("signal = %#v, want window=%q cycle=%q", signal, tt.wantWindow, tt.wantCycle)
			}
		})
	}
}

func TestCodexQuotaOverdraftSignalIgnoresFreshQuota(t *testing.T) {
	signal, exhausted := codexQuotaOverdraftSignalFromSnapshot(CodexQuotaSnapshot{
		UsedRatio: 0.999,
		Window:    "five_hour",
	}, time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC))
	if exhausted {
		t.Fatalf("signal = %#v, exhausted = true, want false", signal)
	}
}

func TestCodexQuotaOverdraftInjectionEligibilityStartsAt95Percent(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	auth := &Auth{
		ID:       "codex-prearm-test",
		Provider: "codex",
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"plan_type": "team",
		},
		Metadata: map[string]any{"access_token": "access-token"},
	}
	_, _ = auth.setCodexQuotaSnapshot("gpt-5.4", CodexQuotaSnapshot{
		UsedRatio: 0.95,
		Window:    "five_hour",
		SampledAt: now.Add(-time.Minute),
		ExpiresAt: now.Add(time.Minute),
		ResetAt:   now.Add(4 * time.Hour),
	})
	if !CodexQuotaOverdraftInjectionEligible(auth, "gpt-5.4", now) {
		t.Fatal("95% Team OAuth snapshot is not injection eligible")
	}
	setCodexQuotaOverdraftState(auth, &CodexQuotaOverdraftProbeState{
		Status:      CodexQuotaOverdraftProbeFailed,
		QuotaWindow: "5h",
		CycleKey:    "5h:" + formatUnix(now.Add(4*time.Hour)),
	})
	if CodexQuotaOverdraftInjectionEligible(auth, "gpt-5.4", now) {
		t.Fatal("failed overdraft cycle remains injection eligible")
	}
}

func TestCodexQuotaOverdraftContextTracksInjectedBusinessResult(t *testing.T) {
	executor := &codexOverdraftTestExecutor{}
	manager, auth := newCodexOverdraftManager(t, executor)
	now := time.Now().UTC()
	if _, _, errUpdate := manager.UpdateCodexQuotaSnapshot(auth.ID, "gpt-5.4", overdraftTestSnapshot(0.95, now)); errUpdate != nil {
		t.Fatalf("UpdateCodexQuotaSnapshot() error = %v", errUpdate)
	}
	ctx := WithCodexQuotaOverdraftTracking(context.Background())
	MarkCodexQuotaOverdraftInjected(ctx, auth.ID)
	manager.MarkResult(ctx, Result{AuthID: auth.ID, Provider: "codex", Model: "gpt-5.4", Success: true})
	state := waitCodexOverdraftState(t, manager, CodexQuotaOverdraftProbePassed)
	if state.ReasonCode != "business_response_ok" {
		t.Fatalf("business success state = %#v, want business_response_ok", state)
	}
}

func TestCodexQuotaOverdraftProbeStateJSONRoundTrip(t *testing.T) {
	startedAt := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	testedAt := startedAt.Add(20 * time.Second)
	original := CodexQuotaOverdraftProbeState{
		Status:             CodexQuotaOverdraftProbePending,
		QuotaWindow:        "5h",
		CycleKey:           "5h:1788152400",
		Attempts:           2,
		Limit:              5,
		Model:              "gpt-5.5",
		ReasonCode:         "quota_limited",
		StartedAt:          startedAt,
		TestedAt:           &testedAt,
		OverdraftStartedAt: &startedAt,
	}

	encoded, errMarshal := json.Marshal(original)
	if errMarshal != nil {
		t.Fatalf("json.Marshal() error = %v", errMarshal)
	}
	var decoded CodexQuotaOverdraftProbeState
	if errUnmarshal := json.Unmarshal(encoded, &decoded); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	if decoded.Status != original.Status || decoded.CycleKey != original.CycleKey || decoded.Attempts != original.Attempts || decoded.Model != original.Model {
		t.Fatalf("decoded state = %#v, want %#v", decoded, original)
	}
	if decoded.TestedAt == nil || !decoded.TestedAt.Equal(testedAt) || decoded.OverdraftStartedAt == nil || !decoded.OverdraftStartedAt.Equal(startedAt) {
		t.Fatalf("decoded timestamps = %#v, want tested=%v overdraft=%v", decoded, testedAt, startedAt)
	}
}

func formatUnix(value time.Time) string {
	return strconv.FormatInt(value.Unix(), 10)
}

type codexOverdraftTestStore struct {
	mu   sync.Mutex
	save []*Auth
}

func (s *codexOverdraftTestStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *codexOverdraftTestStore) Save(_ context.Context, auth *Auth) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if auth != nil {
		s.save = append(s.save, auth.Clone())
	}
	return auth.ID, nil
}

func (s *codexOverdraftTestStore) Delete(context.Context, string) error { return nil }

type codexOverdraftTestExecutor struct {
	mu      sync.Mutex
	results []CodexQuotaOverdraftProbeResult
	calls   []string
}

func (e *codexOverdraftTestExecutor) Identifier() string { return "codex" }

func (e *codexOverdraftTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *codexOverdraftTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *codexOverdraftTestExecutor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }

func (e *codexOverdraftTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *codexOverdraftTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *codexOverdraftTestExecutor) ProbeCodexQuotaOverdraft(_ context.Context, _ *Auth, model string) CodexQuotaOverdraftProbeResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, model)
	if len(e.results) == 0 {
		return CodexQuotaOverdraftProbeResult{Status: "inconclusive", ReasonCode: "missing_test_result", Model: model}
	}
	result := e.results[0]
	e.results = e.results[1:]
	if result.Model == "" {
		result.Model = model
	}
	return result
}

func newCodexOverdraftManager(t *testing.T, executor *codexOverdraftTestExecutor) (*Manager, *Auth) {
	t.Helper()
	manager := NewManager(&codexOverdraftTestStore{}, nil, nil)
	manager.SetConfig(&internalconfig.Config{Codex: internalconfig.CodexConfig{
		Overdraft: internalconfig.CodexOverdraftConfig{Enabled: true},
	}})
	manager.RegisterExecutor(executor)
	auth := &Auth{
		ID:       "codex-overdraft-test",
		Provider: "codex",
		Status:   StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"plan_type": "team",
		},
		Metadata: map[string]any{"access_token": "access-token"},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	return manager, auth
}

func waitCodexOverdraftState(t *testing.T, manager *Manager, want string) *CodexQuotaOverdraftProbeState {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state := codexQuotaOverdraftStateFromManager(manager)
		if state != nil && state.Status == want {
			return state
		}
		time.Sleep(5 * time.Millisecond)
	}
	state := codexQuotaOverdraftStateFromManager(manager)
	t.Fatalf("overdraft state = %#v, want status %q", state, want)
	return nil
}

func codexQuotaOverdraftStateFromManager(manager *Manager) *CodexQuotaOverdraftProbeState {
	if manager == nil {
		return nil
	}
	manager.mu.RLock()
	auth := manager.auths["codex-overdraft-test"]
	if auth != nil {
		auth = auth.Clone()
	}
	manager.mu.RUnlock()
	state, _ := codexQuotaOverdraftStateFromAuth(auth)
	return state
}

func overdraftTestSnapshot(used float64, sampledAt time.Time) CodexQuotaSnapshot {
	return CodexQuotaSnapshot{
		UsedRatio: used,
		Window:    "five_hour",
		SampledAt: sampledAt,
		ExpiresAt: sampledAt.Add(time.Minute),
		ResetAt:   sampledAt.Add(time.Hour),
	}
}

func TestManagerCodexQuotaOverdraftPassesAndRunsOncePerCycle(t *testing.T) {
	executor := &codexOverdraftTestExecutor{results: []CodexQuotaOverdraftProbeResult{{Status: "available", ReasonCode: "model_response_ok"}}}
	manager, auth := newCodexOverdraftManager(t, executor)
	sampledAt := time.Now().UTC()
	snapshot := overdraftTestSnapshot(1, sampledAt)
	if _, accepted, errUpdate := manager.UpdateCodexQuotaSnapshot(auth.ID, "gpt-5.4", snapshot); errUpdate != nil || !accepted {
		t.Fatalf("UpdateCodexQuotaSnapshot() accepted=%t error=%v", accepted, errUpdate)
	}
	state := waitCodexOverdraftState(t, manager, CodexQuotaOverdraftProbePassed)
	if state.Attempts != 1 || state.Model != "gpt-5.4" {
		t.Fatalf("passed state = %#v, want one gpt-5.4 attempt", state)
	}

	if _, accepted, errUpdate := manager.UpdateCodexQuotaSnapshot(auth.ID, "gpt-5.4", snapshot); errUpdate != nil || accepted {
		t.Fatalf("duplicate snapshot accepted=%t error=%v, want rejected", accepted, errUpdate)
	}
	executor.mu.Lock()
	calls := append([]string(nil), executor.calls...)
	executor.mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("probe calls = %v, want one call", calls)
	}
}

func TestManagerCodexQuotaOverdraftDoesNotRetryModelFailure(t *testing.T) {
	executor := &codexOverdraftTestExecutor{results: []CodexQuotaOverdraftProbeResult{
		{Status: "inconclusive", ReasonCode: "model_unavailable"},
	}}
	manager, auth := newCodexOverdraftManager(t, executor)
	if _, _, errUpdate := manager.UpdateCodexQuotaSnapshot(auth.ID, "gpt-5.4", overdraftTestSnapshot(1, time.Now().UTC())); errUpdate != nil {
		t.Fatalf("UpdateCodexQuotaSnapshot() error = %v", errUpdate)
	}
	state := waitCodexOverdraftState(t, manager, CodexQuotaOverdraftProbeInconclusive)
	if state.Attempts != 1 || state.Model != "gpt-5.4" {
		t.Fatalf("inconclusive state = %#v, want one preferred-model attempt", state)
	}
	executor.mu.Lock()
	calls := append([]string(nil), executor.calls...)
	executor.mu.Unlock()
	if len(calls) != 1 || calls[0] != "gpt-5.4" {
		t.Fatalf("probe calls = %v, want [gpt-5.4]", calls)
	}
}

func TestManagerCodexQuotaOverdraftPausesAfterOneExplicitQuotaResult(t *testing.T) {
	executor := &codexOverdraftTestExecutor{results: []CodexQuotaOverdraftProbeResult{
		{Status: "quota_limited", ReasonCode: "quota_limited"},
	}}
	manager, auth := newCodexOverdraftManager(t, executor)
	if _, _, errUpdate := manager.UpdateCodexQuotaSnapshot(auth.ID, "gpt-5.4", overdraftTestSnapshot(1, time.Now().UTC())); errUpdate != nil {
		t.Fatalf("UpdateCodexQuotaSnapshot() error = %v", errUpdate)
	}
	state := waitCodexOverdraftState(t, manager, CodexQuotaOverdraftProbeFailed)
	if state.Attempts != 1 {
		t.Fatalf("failed state attempts = %d, want 1", state.Attempts)
	}
	manager.mu.RLock()
	stored := manager.auths[auth.ID].Clone()
	manager.mu.RUnlock()
	if !stored.Unavailable || stored.Quota.Reason != codexQuotaOverdraftPauseReason {
		t.Fatalf("stored auth = %#v, want overdraft pause", stored)
	}
}

func TestManagerCodexQuotaOverdraftDoesNotPauseOnInconclusiveProbe(t *testing.T) {
	executor := &codexOverdraftTestExecutor{results: []CodexQuotaOverdraftProbeResult{{Status: "inconclusive", ReasonCode: "request_timeout"}}}
	manager, auth := newCodexOverdraftManager(t, executor)
	if _, _, errUpdate := manager.UpdateCodexQuotaSnapshot(auth.ID, "gpt-5.4", overdraftTestSnapshot(1, time.Now().UTC())); errUpdate != nil {
		t.Fatalf("UpdateCodexQuotaSnapshot() error = %v", errUpdate)
	}
	waitCodexOverdraftState(t, manager, CodexQuotaOverdraftProbeInconclusive)
	manager.mu.RLock()
	stored := manager.auths[auth.ID].Clone()
	manager.mu.RUnlock()
	if stored.Unavailable {
		t.Fatalf("inconclusive probe paused auth: %#v", stored)
	}
}

func TestManagerCodexQuotaOverdraftRecoversAndClearsItsPause(t *testing.T) {
	executor := &codexOverdraftTestExecutor{results: []CodexQuotaOverdraftProbeResult{
		{Status: "quota_limited", ReasonCode: "quota_limited"},
	}}
	manager, auth := newCodexOverdraftManager(t, executor)
	firstSample := time.Now().UTC()
	if _, _, errUpdate := manager.UpdateCodexQuotaSnapshot(auth.ID, "gpt-5.4", overdraftTestSnapshot(1, firstSample)); errUpdate != nil {
		t.Fatalf("first UpdateCodexQuotaSnapshot() error = %v", errUpdate)
	}
	waitCodexOverdraftState(t, manager, CodexQuotaOverdraftProbeFailed)
	second := overdraftTestSnapshot(0.5, firstSample.Add(time.Second))
	if _, accepted, errUpdate := manager.UpdateCodexQuotaSnapshot(auth.ID, "gpt-5.4", second); errUpdate != nil || !accepted {
		t.Fatalf("recovery UpdateCodexQuotaSnapshot() accepted=%t error=%v", accepted, errUpdate)
	}
	waitCodexOverdraftState(t, manager, CodexQuotaOverdraftProbeRecovered)
	manager.mu.RLock()
	stored := manager.auths[auth.ID].Clone()
	manager.mu.RUnlock()
	if stored.Unavailable || stored.Quota.Reason != "" {
		t.Fatalf("recovered auth = %#v, want available without overdraft quota", stored)
	}
}

func TestManagerCodexQuotaOverdraftIgnoresBare429ButHandlesExplicitQuota(t *testing.T) {
	executor := &codexOverdraftTestExecutor{results: []CodexQuotaOverdraftProbeResult{{Status: "available", ReasonCode: "model_response_ok"}}}
	manager, auth := newCodexOverdraftManager(t, executor)
	manager.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: "codex", Model: "gpt-5.4", Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limit exceeded"}})
	time.Sleep(25 * time.Millisecond)
	if state := codexQuotaOverdraftStateFromManager(manager); state != nil {
		t.Fatalf("bare 429 created overdraft state: %#v", state)
	}
	manager.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: "codex", Model: "gpt-5.4", Error: &Error{HTTPStatus: http.StatusTooManyRequests, Code: "usage_limit_reached", Message: "usage_limit_reached"}})
	waitCodexOverdraftState(t, manager, CodexQuotaOverdraftProbePassed)
}
