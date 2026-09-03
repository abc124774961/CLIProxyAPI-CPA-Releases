package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

const (
	CodexQuotaOverdraftProbeExtraKey = "codex_quota_overdraft_probe"

	CodexQuotaOverdraftProbePending      = "pending"
	CodexQuotaOverdraftProbePassed       = "passed"
	CodexQuotaOverdraftProbeFailed       = "failed"
	CodexQuotaOverdraftProbeInconclusive = "inconclusive"
	CodexQuotaOverdraftProbeRecovered    = "recovered"

	codexQuotaOverdraftAttemptLimit   = 1
	codexQuotaOverdraftAttemptTimeout = 20 * time.Second
	codexQuotaOverdraftPauseReason    = "codex_quota_overdraft"
	codexQuotaOverdraftPauseMetadata  = "codex_quota_overdraft_pause"
	codexQuotaOverdraftPrearmRatio    = 0.95
	CodexQuotaOverdraftFallbackModel  = "gpt-5.5"
	CodexQuotaOverdraftCompatModel    = "gpt-5.4-mini"
)

// CodexQuotaOverdraftProbeState is persisted in an OAuth auth file's Metadata
// map. It keeps one bounded verification decision stable across reloads.
type CodexQuotaOverdraftProbeState struct {
	Status             string     `json:"status"`
	QuotaWindow        string     `json:"quota_window"`
	CycleKey           string     `json:"cycle_key"`
	Attempts           int        `json:"attempts"`
	Limit              int        `json:"limit"`
	Model              string     `json:"model,omitempty"`
	ReasonCode         string     `json:"reason_code,omitempty"`
	StartedAt          time.Time  `json:"started_at"`
	TestedAt           *time.Time `json:"tested_at,omitempty"`
	RetryAt            *time.Time `json:"retry_at,omitempty"`
	RecoverAt          *time.Time `json:"recover_at,omitempty"`
	OverdraftStartedAt *time.Time `json:"overdraft_started_at,omitempty"`
}

// CodexQuotaOverdraftProbeResult is returned by an executor after one real
// upstream request. Status is one of available, quota_limited, or inconclusive.
type CodexQuotaOverdraftProbeResult struct {
	Status     string
	ReasonCode string
	StatusCode int
	Model      string
	Headers    http.Header
	Body       []byte
}

// CodexQuotaOverdraftProber is implemented by the Codex executor. The request
// is deliberately separate from normal execution so probes never enter the
// downstream retry loop or consume a caller's context.
type CodexQuotaOverdraftProber interface {
	ProbeCodexQuotaOverdraft(context.Context, *Auth, string) CodexQuotaOverdraftProbeResult
}

type codexQuotaOverdraftContextKey struct{}

type codexQuotaOverdraftRequestState struct {
	injectedAuthIDs sync.Map
}

// WithCodexQuotaOverdraftTracking attaches request-local state used to connect
// executor payload mutation with the Manager's final execution result.
func WithCodexQuotaOverdraftTracking(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(codexQuotaOverdraftContextKey{}).(*codexQuotaOverdraftRequestState); ok {
		return ctx
	}
	return context.WithValue(ctx, codexQuotaOverdraftContextKey{}, &codexQuotaOverdraftRequestState{})
}

// CodexQuotaOverdraftTrackingEnabled reports whether the Manager marked this
// request as eligible for Codex quota-overdraft payload handling.
func CodexQuotaOverdraftTrackingEnabled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	_, ok := ctx.Value(codexQuotaOverdraftContextKey{}).(*codexQuotaOverdraftRequestState)
	return ok
}

// MarkCodexQuotaOverdraftInjected records that a selected auth actually
// carried the internal overdraft tool pair to the upstream request.
func MarkCodexQuotaOverdraftInjected(ctx context.Context, authID string) {
	if ctx == nil || strings.TrimSpace(authID) == "" {
		return
	}
	state, _ := ctx.Value(codexQuotaOverdraftContextKey{}).(*codexQuotaOverdraftRequestState)
	if state != nil {
		state.injectedAuthIDs.Store(strings.TrimSpace(authID), struct{}{})
	}
}

// CodexQuotaOverdraftWasInjected reports whether an auth's request contained
// the internal overdraft tool pair.
func CodexQuotaOverdraftWasInjected(ctx context.Context, authID string) bool {
	if ctx == nil || strings.TrimSpace(authID) == "" {
		return false
	}
	state, _ := ctx.Value(codexQuotaOverdraftContextKey{}).(*codexQuotaOverdraftRequestState)
	if state == nil {
		return false
	}
	_, ok := state.injectedAuthIDs.Load(strings.TrimSpace(authID))
	return ok
}

type codexQuotaOverdraftSignal struct {
	Window    string
	CycleKey  string
	RecoverAt time.Time
}

// CodexQuotaOverdraftEligible limits this feature to Team OAuth credentials.
// API keys, free/plus/pro credentials, shadow entries, and other providers are
// intentionally left on the normal CPA scheduling path.
func CodexQuotaOverdraftEligible(auth *Auth) bool {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(executorKeyFromAuth(auth)), "codex") {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.AuthKind()), AuthKindOAuth) {
		return false
	}
	if authIdentityBool(auth, "shadow") || authIdentityBool(auth, "is_shadow") || authIdentityBool(auth, "shadow_mode") {
		return false
	}
	planType := strings.ToLower(strings.TrimSpace(firstAuthIdentityString(auth,
		"plan_type", "chatgpt_plan_type", "planType", "chatgptPlanType")))
	return planType == "team" || planType == "business"
}

// CodexQuotaOverdraftInjectionEligible reports whether normal text traffic for
// this auth should carry the reference overdraft tool pair. The threshold is
// intentionally below 100% because usage snapshots are rounded upstream.
func CodexQuotaOverdraftInjectionEligible(auth *Auth, model string, now time.Time) bool {
	if !CodexQuotaOverdraftEligible(auth) {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	state, hasState := codexQuotaOverdraftStateFromAuth(auth)
	if hasState && state.RecoverAt != nil && state.RecoverAt.After(now) {
		switch state.Status {
		case CodexQuotaOverdraftProbePending, CodexQuotaOverdraftProbePassed, CodexQuotaOverdraftProbeInconclusive:
			return true
		case CodexQuotaOverdraftProbeFailed:
			return false
		}
	}
	snapshot, ok := auth.codexQuotaSnapshot(model, now)
	if !ok || snapshot.UsedRatio < codexQuotaOverdraftPrearmRatio {
		return false
	}
	signal, eligible := codexQuotaOverdraftSignalFromPrearmSnapshot(snapshot, now)
	if !eligible {
		return false
	}
	return !hasState || !codexQuotaOverdraftStateCoversSignal(state, signal) || state.Status != CodexQuotaOverdraftProbeFailed
}

func codexQuotaOverdraftEnabled(m *Manager) bool {
	if m == nil {
		return false
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	return cfg != nil && cfg.Codex.Overdraft.Enabled
}

func codexQuotaOverdraftSignalFromSnapshot(snapshot CodexQuotaSnapshot, now time.Time) (codexQuotaOverdraftSignal, bool) {
	if now.IsZero() {
		now = time.Now()
	}
	if snapshot.UsedRatio < 1 {
		return codexQuotaOverdraftSignal{}, false
	}
	window := codexQuotaOverdraftWindow(snapshot)
	if window == "" {
		return codexQuotaOverdraftSignal{}, false
	}
	recoverAt := snapshot.ResetAt
	if !recoverAt.IsZero() && !recoverAt.After(now) {
		recoverAt = time.Time{}
	}
	cyclePart := "unknown"
	if !recoverAt.IsZero() {
		cyclePart = strconv.FormatInt(recoverAt.Unix(), 10)
	}
	return codexQuotaOverdraftSignal{
		Window:    window,
		CycleKey:  window + ":" + cyclePart,
		RecoverAt: recoverAt,
	}, true
}

func codexQuotaOverdraftSignalFromPrearmSnapshot(snapshot CodexQuotaSnapshot, now time.Time) (codexQuotaOverdraftSignal, bool) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if snapshot.UsedRatio < codexQuotaOverdraftPrearmRatio {
		return codexQuotaOverdraftSignal{}, false
	}
	window := codexQuotaOverdraftWindow(snapshot)
	if window == "" {
		return codexQuotaOverdraftSignal{}, false
	}
	recoverAt := snapshot.ResetAt
	if !recoverAt.IsZero() && !recoverAt.After(now) {
		recoverAt = time.Time{}
	}
	cyclePart := "unknown"
	if !recoverAt.IsZero() {
		cyclePart = strconv.FormatInt(recoverAt.Unix(), 10)
	}
	return codexQuotaOverdraftSignal{
		Window:    window,
		CycleKey:  window + ":" + cyclePart,
		RecoverAt: recoverAt,
	}, true
}

func codexQuotaOverdraftWindow(snapshot CodexQuotaSnapshot) string {
	window := strings.ToLower(strings.TrimSpace(snapshot.Window))
	switch window {
	case "5h", "five_hour", "five-hour", "five hour", "5_hour":
		return "5h"
	case "7d", "seven_day", "seven-day", "seven day", "weekly", "week":
		return "7d"
	case "primary", "secondary":
		if !snapshot.ResetAt.IsZero() && !snapshot.SampledAt.IsZero() {
			remaining := snapshot.ResetAt.Sub(snapshot.SampledAt)
			if remaining >= 6*24*time.Hour {
				return "7d"
			}
		}
		if window == "secondary" {
			return "7d"
		}
		return "5h"
	default:
		return ""
	}
}

func codexQuotaOverdraftStateFromAuth(auth *Auth) (*CodexQuotaOverdraftProbeState, bool) {
	if auth == nil || auth.Metadata == nil {
		return nil, false
	}
	raw, ok := auth.Metadata[CodexQuotaOverdraftProbeExtraKey]
	if !ok || raw == nil {
		return nil, false
	}
	if state, ok := raw.(CodexQuotaOverdraftProbeState); ok {
		return cloneCodexQuotaOverdraftState(&state), state.CycleKey != ""
	}
	if state, ok := raw.(*CodexQuotaOverdraftProbeState); ok && state != nil {
		return cloneCodexQuotaOverdraftState(state), state.CycleKey != ""
	}
	encoded, errMarshal := json.Marshal(raw)
	if errMarshal != nil {
		return nil, false
	}
	var state CodexQuotaOverdraftProbeState
	if errUnmarshal := json.Unmarshal(encoded, &state); errUnmarshal != nil || strings.TrimSpace(state.CycleKey) == "" {
		return nil, false
	}
	return &state, true
}

func cloneCodexQuotaOverdraftState(state *CodexQuotaOverdraftProbeState) *CodexQuotaOverdraftProbeState {
	if state == nil {
		return nil
	}
	clone := *state
	clone.TestedAt = cloneTimePointer(state.TestedAt)
	clone.RetryAt = cloneTimePointer(state.RetryAt)
	clone.RecoverAt = cloneTimePointer(state.RecoverAt)
	clone.OverdraftStartedAt = cloneTimePointer(state.OverdraftStartedAt)
	return &clone
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func setCodexQuotaOverdraftState(auth *Auth, state *CodexQuotaOverdraftProbeState) {
	if auth == nil {
		return
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	if state == nil {
		delete(auth.Metadata, CodexQuotaOverdraftProbeExtraKey)
		return
	}
	auth.Metadata[CodexQuotaOverdraftProbeExtraKey] = *cloneCodexQuotaOverdraftState(state)
}

func (m *Manager) codexQuotaOverdraftProber() CodexQuotaOverdraftProber {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	executor := m.executors["codex"]
	m.mu.RUnlock()
	prober, _ := executor.(CodexQuotaOverdraftProber)
	return prober
}

func codexQuotaOverdraftProbeModels(preferred string) []string {
	models := make([]string, 0, 3)
	for _, model := range []string{preferred, CodexQuotaOverdraftFallbackModel, CodexQuotaOverdraftCompatModel} {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		duplicate := false
		for _, existing := range models {
			if strings.EqualFold(existing, model) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			models = append(models, model)
		}
	}
	if len(models) == 0 {
		return []string{CodexQuotaOverdraftFallbackModel}
	}
	return models
}

func codexQuotaOverdraftStateCoversSignal(state *CodexQuotaOverdraftProbeState, signal codexQuotaOverdraftSignal) bool {
	return state != nil && state.CycleKey != "" && signal.CycleKey != "" &&
		state.QuotaWindow == signal.Window && state.CycleKey == signal.CycleKey
}

// ObserveCodexQuotaSnapshot evaluates one accepted usage snapshot. Probe work
// is asynchronous, while the pending state is persisted before the first call.
func (m *Manager) ObserveCodexQuotaSnapshot(authID, preferredModel string, snapshot CodexQuotaSnapshot) {
	if m == nil || !m.codexOverdraftEnabled() || m.codexQuotaOverdraftProber() == nil {
		return
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	now := time.Now().UTC()
	signal, exhausted := codexQuotaOverdraftSignalFromSnapshot(snapshot, now)
	var launchState *CodexQuotaOverdraftProbeState
	var launchAuth *Auth
	var recoveredAuth *Auth

	m.mu.Lock()
	auth := m.auths[authID]
	if auth == nil || !CodexQuotaOverdraftEligible(auth) {
		m.mu.Unlock()
		return
	}
	state, hasState := codexQuotaOverdraftStateFromAuth(auth)
	if snapshot.UsedRatio < codexQuotaOverdraftPrearmRatio {
		if hasState && state.QuotaWindow == codexQuotaOverdraftWindow(snapshot) && state.Status != CodexQuotaOverdraftProbeRecovered {
			state.Status = CodexQuotaOverdraftProbeRecovered
			state.ReasonCode = "quota_recovered"
			state.RetryAt = nil
			state.RecoverAt = nil
			setCodexQuotaOverdraftState(auth, state)
			clearCodexQuotaOverdraftPause(auth)
			recoveredAuth = auth.Clone()
		}
		m.mu.Unlock()
		if recoveredAuth != nil {
			_ = m.persist(context.Background(), recoveredAuth)
			if m.scheduler != nil {
				m.scheduler.upsertAuth(recoveredAuth)
			}
		}
		return
	}
	if !exhausted {
		m.mu.Unlock()
		return
	}

	if hasState && codexQuotaOverdraftStateCoversSignal(state, signal) {
		switch state.Status {
		case CodexQuotaOverdraftProbePassed, CodexQuotaOverdraftProbeFailed:
			m.mu.Unlock()
			return
		case CodexQuotaOverdraftProbePending, CodexQuotaOverdraftProbeInconclusive:
			m.mu.Unlock()
			return
		}
	}

	startedAt := now
	launchState = &CodexQuotaOverdraftProbeState{
		Status:      CodexQuotaOverdraftProbePending,
		QuotaWindow: signal.Window,
		CycleKey:    signal.CycleKey,
		Limit:       codexQuotaOverdraftAttemptLimit,
		StartedAt:   startedAt,
	}
	if !signal.RecoverAt.IsZero() {
		launchState.RecoverAt = &signal.RecoverAt
	}
	launchKey := authID + "\x00" + signal.CycleKey
	if _, alreadyRunning := m.codexOverdraftRunning[launchKey]; alreadyRunning {
		m.mu.Unlock()
		return
	}
	if m.codexOverdraftRunning == nil {
		m.codexOverdraftRunning = make(map[string]struct{})
	}
	m.codexOverdraftRunning[launchKey] = struct{}{}
	setCodexQuotaOverdraftState(auth, launchState)
	launchAuth = auth.Clone()
	m.mu.Unlock()

	_ = m.persist(context.Background(), launchAuth)
	go m.runCodexQuotaOverdraftProbe(authID, preferredModel, signal, launchKey)
}

func (m *Manager) codexOverdraftEnabled() bool {
	return codexQuotaOverdraftEnabled(m)
}

func (m *Manager) runCodexQuotaOverdraftProbe(authID, preferredModel string, signal codexQuotaOverdraftSignal, runningKey string) {
	defer func() {
		m.mu.Lock()
		delete(m.codexOverdraftRunning, runningKey)
		m.mu.Unlock()
	}()
	prober := m.codexQuotaOverdraftProber()
	if prober == nil {
		m.finishCodexQuotaOverdraftInconclusive(authID, signal.CycleKey, "probe_unavailable")
		return
	}

	m.mu.RLock()
	auth := m.auths[authID]
	if auth != nil {
		auth = auth.Clone()
	}
	m.mu.RUnlock()
	if !CodexQuotaOverdraftEligible(auth) {
		m.finishCodexQuotaOverdraftInconclusive(authID, signal.CycleKey, "auth_unavailable")
		return
	}

	models := codexQuotaOverdraftProbeModels(preferredModel)
	quotaLimitedAttempts := 0
	lastReason := "invalid_response"
	for attempt := 0; attempt < codexQuotaOverdraftAttemptLimit; attempt++ {
		model := models[attempt%len(models)]
		attemptCtx, cancel := context.WithTimeout(context.Background(), codexQuotaOverdraftAttemptTimeout)
		result := prober.ProbeCodexQuotaOverdraft(attemptCtx, auth, model)
		cancel()
		if strings.TrimSpace(result.Model) == "" {
			result.Model = model
		}
		lastReason = strings.TrimSpace(result.ReasonCode)
		if lastReason == "" {
			lastReason = "invalid_response"
		}
		if result.Status == "quota_limited" {
			quotaLimitedAttempts++
		}
		m.recordCodexQuotaOverdraftAttempt(authID, signal.CycleKey, result, attempt+1)
		if result.Status == "available" {
			m.finishCodexQuotaOverdraftPassed(authID, signal.CycleKey, result.Model, lastReason, signal.RecoverAt)
			return
		}
		if result.Status == "inconclusive" && !codexQuotaOverdraftShouldTryNextModel(result.ReasonCode) || result.Status == "authentication_failed" || result.Status == "" {
			m.finishCodexQuotaOverdraftInconclusive(authID, signal.CycleKey, lastReason)
			return
		}
	}
	if quotaLimitedAttempts == codexQuotaOverdraftAttemptLimit {
		m.finishCodexQuotaOverdraftFailed(authID, signal.CycleKey, lastReason, signal.RecoverAt)
		return
	}
	m.finishCodexQuotaOverdraftInconclusive(authID, signal.CycleKey, lastReason)
}

func codexQuotaOverdraftShouldTryNextModel(reason string) bool {
	lower := strings.ToLower(strings.TrimSpace(reason))
	if lower == "model_unavailable" || lower == "model_not_found" || lower == "unknown_model" || lower == "unknown_provider" {
		return true
	}
	return strings.Contains(lower, "model") && (strings.Contains(lower, "unknown") || strings.Contains(lower, "unavailable") || strings.Contains(lower, "unsupported") || strings.Contains(lower, "not_found"))
}

func (m *Manager) recordCodexQuotaOverdraftAttempt(authID, cycleKey string, result CodexQuotaOverdraftProbeResult, attempt int) {
	m.updateCodexQuotaOverdraftState(authID, cycleKey, func(state *CodexQuotaOverdraftProbeState, auth *Auth, now time.Time) {
		state.Attempts = attempt
		state.Model = result.Model
		state.ReasonCode = result.ReasonCode
		testedAt := now
		state.TestedAt = &testedAt
	})
}

func (m *Manager) finishCodexQuotaOverdraftPassed(authID, cycleKey, model, reason string, recoverAt time.Time) {
	m.updateCodexQuotaOverdraftState(authID, cycleKey, func(state *CodexQuotaOverdraftProbeState, auth *Auth, now time.Time) {
		state.Status = CodexQuotaOverdraftProbePassed
		state.Model = model
		state.ReasonCode = "model_response_ok"
		state.RetryAt = nil
		if !recoverAt.IsZero() {
			state.RecoverAt = &recoverAt
		}
		startedAt := now
		if state.OverdraftStartedAt == nil {
			state.OverdraftStartedAt = &startedAt
		}
		if reason != "" && reason != "model_response_ok" {
			state.ReasonCode = reason
		}
		clearCodexQuotaOverdraftPause(auth)
	})
}

func (m *Manager) finishCodexQuotaOverdraftFailed(authID, cycleKey, reason string, recoverAt time.Time) {
	m.updateCodexQuotaOverdraftState(authID, cycleKey, func(state *CodexQuotaOverdraftProbeState, auth *Auth, _ time.Time) {
		state.Status = CodexQuotaOverdraftProbeFailed
		state.ReasonCode = reason
		state.RetryAt = nil
		if !recoverAt.IsZero() {
			state.RecoverAt = &recoverAt
		}
		applyCodexQuotaOverdraftPause(auth, state)
	})
}

func (m *Manager) finishCodexQuotaOverdraftInconclusive(authID, cycleKey, reason string) {
	m.updateCodexQuotaOverdraftState(authID, cycleKey, func(state *CodexQuotaOverdraftProbeState, _ *Auth, _ time.Time) {
		state.Status = CodexQuotaOverdraftProbeInconclusive
		state.ReasonCode = reason
		state.RetryAt = nil
	})
}

// ObserveCodexQuotaOverdraftBusinessSuccess records a successful normal
// request that carried the overdraft payload. It is the strongest evidence of
// account availability and therefore avoids an additional probe request.
func (m *Manager) ObserveCodexQuotaOverdraftBusinessSuccess(ctx context.Context, result Result) {
	if m == nil || !result.Success || !CodexQuotaOverdraftWasInjected(ctx, result.AuthID) || !m.codexOverdraftEnabled() {
		return
	}
	auth := m.authByID(result.AuthID)
	if !CodexQuotaOverdraftEligible(auth) {
		return
	}
	now := time.Now().UTC()
	snapshot, ok := auth.codexQuotaSnapshot(result.Model, now)
	if !ok {
		return
	}
	signal, eligible := codexQuotaOverdraftSignalFromPrearmSnapshot(snapshot, now)
	if !eligible {
		return
	}
	state := &CodexQuotaOverdraftProbeState{
		Status:      CodexQuotaOverdraftProbePassed,
		QuotaWindow: signal.Window,
		CycleKey:    signal.CycleKey,
		Limit:       codexQuotaOverdraftAttemptLimit,
		Model:       result.Model,
		ReasonCode:  "business_response_ok",
		StartedAt:   now,
		TestedAt:    &now,
	}
	if !signal.RecoverAt.IsZero() {
		state.RecoverAt = &signal.RecoverAt
	}
	m.updateCodexQuotaOverdraftStateForSignal(ctx, result.AuthID, signal, state, false)
}

func (m *Manager) updateCodexQuotaOverdraftStateForSignal(ctx context.Context, authID string, signal codexQuotaOverdraftSignal, next *CodexQuotaOverdraftProbeState, preserveFailed bool) {
	if m == nil || next == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var snapshot *Auth
	m.mu.Lock()
	auth := m.auths[strings.TrimSpace(authID)]
	if auth == nil || !CodexQuotaOverdraftEligible(auth) {
		m.mu.Unlock()
		return
	}
	current, hasCurrent := codexQuotaOverdraftStateFromAuth(auth)
	if hasCurrent && codexQuotaOverdraftStateCoversSignal(current, signal) {
		if preserveFailed && current.Status == CodexQuotaOverdraftProbeFailed {
			m.mu.Unlock()
			return
		}
		if !preserveFailed && current.Status == CodexQuotaOverdraftProbeFailed {
			m.mu.Unlock()
			return
		}
		if next.OverdraftStartedAt == nil {
			next.OverdraftStartedAt = cloneTimePointer(current.OverdraftStartedAt)
		}
	}
	if next.OverdraftStartedAt == nil {
		startedAt := next.StartedAt
		next.OverdraftStartedAt = cloneTimePointer(&startedAt)
	}
	setCodexQuotaOverdraftState(auth, next)
	if next.Status == CodexQuotaOverdraftProbeFailed {
		applyCodexQuotaOverdraftPause(auth, next)
	}
	if next.Status == CodexQuotaOverdraftProbePassed || next.Status == CodexQuotaOverdraftProbeRecovered {
		clearCodexQuotaOverdraftPause(auth)
	}
	snapshot = auth.Clone()
	m.mu.Unlock()
	_ = m.persist(ctx, snapshot)
	if m.scheduler != nil {
		m.scheduler.upsertAuth(snapshot)
	}
}

// ObserveCodexQuotaOverdraftBusinessQuotaFailure immediately finalizes a
// request whose injected payload received explicit subscription-quota 429.
func (m *Manager) ObserveCodexQuotaOverdraftBusinessQuotaFailure(ctx context.Context, result Result) {
	if m == nil || result.Success || !CodexQuotaOverdraftWasInjected(ctx, result.AuthID) || !m.codexOverdraftEnabled() || !isDefiniteCodexQuotaResult(result.Error) {
		return
	}
	auth := m.authByID(result.AuthID)
	if !CodexQuotaOverdraftEligible(auth) {
		return
	}
	now := time.Now().UTC()
	snapshot, ok := auth.codexQuotaSnapshot(result.Model, now)
	if !ok || snapshot.UsedRatio < codexQuotaOverdraftPrearmRatio {
		recoverAt := now.Add(5 * time.Hour)
		if result.RetryAfter != nil && *result.RetryAfter > 0 {
			recoverAt = now.Add(*result.RetryAfter)
		}
		snapshot = CodexQuotaSnapshot{UsedRatio: 1, Window: "5h", SampledAt: now, ResetAt: recoverAt, ExpiresAt: now.Add(90 * time.Second)}
	}
	signal, eligible := codexQuotaOverdraftSignalFromPrearmSnapshot(snapshot, now)
	if !eligible {
		return
	}
	state := &CodexQuotaOverdraftProbeState{
		Status:      CodexQuotaOverdraftProbeFailed,
		QuotaWindow: signal.Window,
		CycleKey:    signal.CycleKey,
		Attempts:    1,
		Limit:       codexQuotaOverdraftAttemptLimit,
		Model:       result.Model,
		ReasonCode:  "business_quota_limited",
		StartedAt:   now,
		TestedAt:    &now,
		RecoverAt:   nil,
	}
	if !signal.RecoverAt.IsZero() {
		state.RecoverAt = &signal.RecoverAt
	}
	m.updateCodexQuotaOverdraftStateForSignal(ctx, result.AuthID, signal, state, true)
}

func (m *Manager) updateCodexQuotaOverdraftState(authID, cycleKey string, update func(*CodexQuotaOverdraftProbeState, *Auth, time.Time)) {
	if m == nil || update == nil {
		return
	}
	var snapshot *Auth
	m.mu.Lock()
	auth := m.auths[authID]
	state, ok := codexQuotaOverdraftStateFromAuth(auth)
	if auth == nil || !ok || state.CycleKey != cycleKey {
		m.mu.Unlock()
		return
	}
	update(state, auth, time.Now().UTC())
	setCodexQuotaOverdraftState(auth, state)
	snapshot = auth.Clone()
	m.mu.Unlock()
	_ = m.persist(context.Background(), snapshot)
	if m.scheduler != nil {
		m.scheduler.upsertAuth(snapshot)
	}
}

func applyCodexQuotaOverdraftPause(auth *Auth, state *CodexQuotaOverdraftProbeState) {
	if auth == nil || state == nil {
		return
	}
	now := time.Now().UTC()
	recoverAt := state.RecoverAt
	if recoverAt == nil || !recoverAt.After(now) {
		fallback := now.Add(5 * time.Hour)
		recoverAt = &fallback
		state.RecoverAt = recoverAt
	}
	auth.Unavailable = true
	auth.Status = StatusError
	auth.StatusMessage = "Codex quota exhausted"
	auth.NextRetryAfter = *recoverAt
	auth.Quota = QuotaState{Exceeded: true, Reason: codexQuotaOverdraftPauseReason, NextRecoverAt: *recoverAt}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata[codexQuotaOverdraftPauseMetadata] = true
	auth.UpdatedAt = now
}

func clearCodexQuotaOverdraftPause(auth *Auth) {
	if auth == nil || auth.Metadata == nil || !authIdentityBool(auth, codexQuotaOverdraftPauseMetadata) {
		return
	}
	delete(auth.Metadata, codexQuotaOverdraftPauseMetadata)
	if auth.Quota.Reason == codexQuotaOverdraftPauseReason {
		auth.Unavailable = false
		auth.NextRetryAfter = time.Time{}
		auth.Quota = QuotaState{}
		if auth.Status == StatusError {
			auth.Status = StatusActive
		}
		auth.StatusMessage = ""
		auth.UpdatedAt = time.Now().UTC()
	}
}

// HandleCodexQuotaResult observes only explicit Codex subscription-quota
// failures. Bare/transient 429s continue through MarkResult unchanged.
func (m *Manager) HandleCodexQuotaResult(ctx context.Context, result Result) {
	if m == nil || result.Success || !CodexQuotaOverdraftEligible(m.authByID(result.AuthID)) || !isDefiniteCodexQuotaResult(result.Error) {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.RLock()
	auth := m.auths[strings.TrimSpace(result.AuthID)]
	if auth != nil {
		auth = auth.Clone()
	}
	m.mu.RUnlock()
	if auth == nil {
		return
	}
	snapshot, ok := auth.codexQuotaSnapshot(result.Model, time.Now())
	if !ok || snapshot.UsedRatio < 1 {
		now := time.Now().UTC()
		recoverAt := now.Add(5 * time.Hour)
		if result.RetryAfter != nil && *result.RetryAfter > 0 {
			recoverAt = now.Add(*result.RetryAfter)
		}
		snapshot = CodexQuotaSnapshot{UsedRatio: 1, Window: "5h", SampledAt: now, ResetAt: recoverAt, ExpiresAt: now.Add(90 * time.Second)}
	}
	_ = ctx
	m.ObserveCodexQuotaSnapshot(result.AuthID, result.Model, snapshot)
}

func (m *Manager) authByID(authID string) *Auth {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	auth := m.auths[strings.TrimSpace(authID)]
	if auth != nil {
		auth = auth.Clone()
	}
	m.mu.RUnlock()
	return auth
}

func (m *Manager) withCodexQuotaOverdraftTracking(ctx context.Context, providers []string) context.Context {
	if m == nil || !m.codexOverdraftEnabled() {
		return ctx
	}
	for _, provider := range providers {
		if strings.EqualFold(strings.TrimSpace(provider), "codex") {
			return WithCodexQuotaOverdraftTracking(ctx)
		}
	}
	return ctx
}

func isDefiniteCodexQuotaResult(err *Error) bool {
	if err == nil || statusCodeFromResult(err) != http.StatusTooManyRequests {
		return false
	}
	code := strings.ToLower(strings.TrimSpace(err.Code))
	message := strings.ToLower(strings.TrimSpace(err.Message))
	if code == "quota" || code == "usage_limit_reached" || code == "quota_exceeded" {
		return true
	}
	for _, marker := range []string{"usage_limit_reached", "usage limit has been reached", "quota exhausted", "weekly limit reached"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
