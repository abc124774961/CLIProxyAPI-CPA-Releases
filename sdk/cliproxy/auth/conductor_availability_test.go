package auth

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestUpdateAggregatedAvailability_UnavailableWithoutNextRetryDoesNotBlockAuth(t *testing.T) {
	t.Parallel()

	now := time.Now()
	model := "test-model"
	auth := &Auth{
		ID: "a",
		ModelStates: map[string]*ModelState{
			model: {
				Status:      StatusError,
				Unavailable: true,
			},
		},
	}

	updateAggregatedAvailability(auth, now)

	if auth.Unavailable {
		t.Fatalf("auth.Unavailable = true, want false")
	}
	if !auth.NextRetryAfter.IsZero() {
		t.Fatalf("auth.NextRetryAfter = %v, want zero", auth.NextRetryAfter)
	}
}

func TestUpdateAggregatedAvailability_FutureNextRetryBlocksAuth(t *testing.T) {
	t.Parallel()

	now := time.Now()
	model := "test-model"
	next := now.Add(5 * time.Minute)
	auth := &Auth{
		ID: "a",
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: next,
			},
		},
	}

	updateAggregatedAvailability(auth, now)

	if !auth.Unavailable {
		t.Fatalf("auth.Unavailable = false, want true")
	}
	if auth.NextRetryAfter.IsZero() {
		t.Fatalf("auth.NextRetryAfter = zero, want %v", next)
	}
	if auth.NextRetryAfter.Sub(next) > time.Second || next.Sub(auth.NextRetryAfter) > time.Second {
		t.Fatalf("auth.NextRetryAfter = %v, want %v", auth.NextRetryAfter, next)
	}
}

func TestManager_AvailableProvidersAndHasProviderAuth_ExcludeDisabled(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()

	if _, err := manager.Register(ctx, &Auth{ID: "active", Provider: "claude", Status: StatusActive}); err != nil {
		t.Fatalf("register active auth: %v", err)
	}
	// Provider gemini only has an auth with the Disabled flag set.
	if _, err := manager.Register(ctx, &Auth{ID: "flag-disabled", Provider: "gemini", Disabled: true}); err != nil {
		t.Fatalf("register flag-disabled auth: %v", err)
	}
	// Provider codex only has an auth whose Status is StatusDisabled.
	if _, err := manager.Register(ctx, &Auth{ID: "status-disabled", Provider: "codex", Status: StatusDisabled}); err != nil {
		t.Fatalf("register status-disabled auth: %v", err)
	}

	providers := manager.AvailableProviders()
	present := make(map[string]bool, len(providers))
	for _, p := range providers {
		present[p] = true
	}
	if !present["claude"] {
		t.Errorf("AvailableProviders() = %v, want to include active provider claude", providers)
	}
	if present["gemini"] {
		t.Errorf("AvailableProviders() = %v, want to exclude Disabled provider gemini", providers)
	}
	if present["codex"] {
		t.Errorf("AvailableProviders() = %v, want to exclude StatusDisabled provider codex", providers)
	}

	if !manager.HasProviderAuth("claude") {
		t.Errorf("HasProviderAuth(claude) = false, want true")
	}
	if manager.HasProviderAuth("gemini") {
		t.Errorf("HasProviderAuth(gemini) = true, want false (only Disabled auth registered)")
	}
	if manager.HasProviderAuth("codex") {
		t.Errorf("HasProviderAuth(codex) = true, want false (only StatusDisabled auth registered)")
	}
}

func TestManager_ResetQuotaClearsRuntimeAndRegistryState(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "reset-quota-auth"
	model := "reset-quota-model"
	next := time.Now().Add(time.Hour)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	if _, errRegister := manager.Register(ctx, &Auth{
		ID:             authID,
		Provider:       "claude",
		Status:         StatusError,
		StatusMessage:  "quota exhausted",
		Unavailable:    true,
		NextRetryAfter: next,
		Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: next, BackoffLevel: 2},
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				StatusMessage:  "quota exhausted",
				Unavailable:    true,
				NextRetryAfter: next,
				Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: next, BackoffLevel: 2},
				UpdatedAt:      next,
			},
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg.SetModelQuotaExceeded(authID, model)
	reg.SuspendClientModel(authID, model, "quota")
	if count := reg.GetModelCount(model); count != 0 {
		t.Fatalf("registry model count before reset = %d, want 0", count)
	}

	updated, models, errReset := manager.ResetQuota(ctx, authID)
	if errReset != nil {
		t.Fatalf("ResetQuota() error = %v", errReset)
	}
	if updated == nil {
		t.Fatalf("ResetQuota() updated auth is nil")
	}
	if len(models) != 1 || models[0] != model {
		t.Fatalf("ResetQuota() models = %v, want [%s]", models, model)
	}
	if updated.Status != StatusActive || updated.StatusMessage != "" || updated.Unavailable || !updated.NextRetryAfter.IsZero() {
		t.Fatalf("updated auth state = status %q message %q unavailable %v next %v", updated.Status, updated.StatusMessage, updated.Unavailable, updated.NextRetryAfter)
	}
	if updated.Quota.Exceeded || updated.Quota.Reason != "" || !updated.Quota.NextRecoverAt.IsZero() || updated.Quota.BackoffLevel != 0 {
		t.Fatalf("updated auth quota = %+v, want cleared", updated.Quota)
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("updated model state missing")
	}
	if state.Status != StatusActive || state.StatusMessage != "" || state.Unavailable || !state.NextRetryAfter.IsZero() {
		t.Fatalf("updated model state = status %q message %q unavailable %v next %v", state.Status, state.StatusMessage, state.Unavailable, state.NextRetryAfter)
	}
	if state.Quota.Exceeded || state.Quota.Reason != "" || !state.Quota.NextRecoverAt.IsZero() || state.Quota.BackoffLevel != 0 {
		t.Fatalf("updated model quota = %+v, want cleared", state.Quota)
	}
	if count := reg.GetModelCount(model); count != 1 {
		t.Fatalf("registry model count after reset = %d, want 1", count)
	}
}

func TestManager_ResetQuotaRecoversQuotaDisabledAuthAndClearsRuntimeState(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "reset-quota-disabled-auth"
	model := "reset-quota-disabled-model"
	next := time.Now().Add(time.Hour)
	auth := &Auth{
		ID:       authID,
		Provider: "codex",
		Status:   StatusDisabled,
		Disabled: true,
		Metadata: map[string]any{"disabled": true},
		ModelStates: map[string]*ModelState{
			model: {Status: StatusError, Unavailable: true, Quota: QuotaState{Exceeded: true, Reason: "usage_limit_reached"}},
		},
	}
	state := auth.ensureRuntimeLimits()
	state.mu.Lock()
	state.frozenUntil = next
	state.usageLimitFreezeUntil = next
	state.quotaPreemptFreezeUntil = next
	state.rateLimitedUntil = next
	state.upstreamRateLimitedUntil = next
	state.upstreamRateLimitBackoff = 3
	state.upstreamRateLimitLastSeen = time.Now()
	state.rateWindowStart = time.Now()
	state.rateWindowCount = 4
	state.lastSkipReason = runtimeSkipReasonUsageLimitReached
	state.lastSkipRecordedAt = time.Now()
	state.lastSkipRecoveryTarget = next
	state.codexQuotaSnapshots.Store(codexQuotaSnapshotStore{"*": {UsedRatio: 1, ExpiresAt: next}})
	state.mu.Unlock()

	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	updated, _, errReset := manager.ResetQuota(ctx, authID)
	if errReset != nil {
		t.Fatalf("ResetQuota() error = %v", errReset)
	}
	if updated == nil || updated.Disabled || updated.Status != StatusActive {
		t.Fatalf("updated disabled/status = %v/%q, want false/active", updated.Disabled, updated.Status)
	}
	if disabled, ok := updated.Metadata["disabled"].(bool); !ok || disabled {
		t.Fatalf("metadata disabled = %#v, want false", updated.Metadata["disabled"])
	}
	limits := updated.RuntimeLimitSnapshot(time.Now())
	if limits.FrozenUntil.After(time.Now()) || limits.RateLimitedUntil.After(time.Now()) || limits.LastSkipReason != "" {
		t.Fatalf("runtime limits after reset = %+v, want clear", limits)
	}
	if got, ok := updated.ensureRuntimeLimits().codexQuotaSnapshots.Load().(codexQuotaSnapshotStore); ok && len(got) != 0 {
		t.Fatalf("codex quota snapshots after reset = %#v, want empty", got)
	}
}

func TestManager_ResetQuotaPreservesManualAndCredentialDisabledAuth(t *testing.T) {
	tests := []struct {
		name          string
		statusMessage string
		lastError     *Error
	}{
		{name: "manual", statusMessage: "disabled via management API"},
		{name: "credential", statusMessage: "credential invalidated", lastError: &Error{Code: terminalCredentialErrorCode, Message: "invalid credential"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			authID := "reset-quota-preserve-" + tt.name
			auth := &Auth{ID: authID, Provider: "codex", Status: StatusDisabled, Disabled: true, StatusMessage: tt.statusMessage, LastError: tt.lastError, Metadata: map[string]any{"disabled": true}}
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}
			updated, _, errReset := manager.ResetQuota(context.Background(), authID)
			if errReset != nil {
				t.Fatalf("ResetQuota() error = %v", errReset)
			}
			if updated == nil || !updated.Disabled || updated.Status != StatusDisabled {
				t.Fatalf("updated disabled/status = %v/%q, want true/disabled", updated.Disabled, updated.Status)
			}
		})
	}
}

func TestManager_ResetQuotaPreservesInFlightConcurrency(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	authID := "reset-quota-inflight-auth"
	next := time.Now().Add(time.Hour)
	auth := &Auth{
		ID: authID, Provider: "codex", Status: StatusDisabled, Disabled: true,
		Metadata: map[string]any{"disabled": true},
	}
	state := auth.ensureRuntimeLimits()
	state.mu.Lock()
	state.currentConcurrency = 2
	state.quotaPreemptFreezeUntil = next
	state.lastSkipReason = runtimeSkipReasonQuotaPreempt
	state.mu.Unlock()
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	updated, _, errReset := manager.ResetQuota(context.Background(), authID)
	if errReset != nil {
		t.Fatalf("ResetQuota() error = %v", errReset)
	}
	if updated == nil || !updated.Disabled {
		t.Fatalf("updated disabled = %v, want true while requests are active", updated != nil && updated.Disabled)
	}
	if got := updated.RuntimeLimitSnapshot(time.Now()).CurrentConcurrency; got != 2 {
		t.Fatalf("current concurrency after reset = %d, want 2", got)
	}
	if limits := updated.RuntimeLimitSnapshot(time.Now()); !limits.FrozenUntil.IsZero() || limits.LastSkipReason != "" {
		t.Fatalf("runtime limits after reset = %+v, want stale freeze cleared", limits)
	}
}
