package auth

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestManager_Remove_DeletesRuntimeAuth(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()

	auth := &Auth{
		ID:       "remove-runtime-auth",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{"email": "x@example.com"},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.Remove(ctx, auth.ID)

	if _, ok := manager.GetByID(auth.ID); ok {
		t.Fatalf("expected auth %q to be removed", auth.ID)
	}
}

func TestManager_Update_MissingAuthIsNoOp(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()

	auth := &Auth{
		ID:       "missing-update-auth",
		Provider: "claude",
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	manager.Remove(ctx, auth.ID)

	updated, errUpdate := manager.Update(ctx, &Auth{
		ID:       auth.ID,
		Provider: "claude",
		Status:   StatusDisabled,
		Disabled: true,
	})
	if errUpdate != nil {
		t.Fatalf("update removed auth: %v", errUpdate)
	}
	if updated != nil {
		t.Fatalf("expected update on removed auth to be no-op, got %#v", updated)
	}
	if _, ok := manager.GetByID(auth.ID); ok {
		t.Fatalf("expected removed auth to stay absent after late update")
	}
}

func TestManager_Remove_UnschedulesAutoRefresh(t *testing.T) {
	ctx := context.Background()

	manager := NewManager(nil, nil, nil)
	loop := newAuthAutoRefreshLoop(manager, time.Second, 1)
	manager.mu.Lock()
	manager.refreshLoop = loop
	manager.mu.Unlock()

	lead := 10 * time.Minute
	setRefreshLeadFactory(t, "provider-lead-expiry", func() *time.Duration {
		d := lead
		return &d
	})

	auth := &Auth{
		ID:       "remove-refresh-auth",
		Provider: "provider-lead-expiry",
		Metadata: map[string]any{
			"email":      "x@example.com",
			"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	now := time.Now()
	if _, ok := nextRefreshCheckAt(now, auth, time.Second); !ok {
		t.Fatalf("expected auth to be scheduled before removal")
	}
	loop.applyDirty(now)
	loop.mu.Lock()
	if _, ok := loop.index[auth.ID]; !ok {
		loop.mu.Unlock()
		t.Fatalf("expected auth %q to be present in auto-refresh index before removal", auth.ID)
	}
	loop.mu.Unlock()

	manager.Remove(ctx, auth.ID)

	if _, ok := manager.GetByID(auth.ID); ok {
		t.Fatalf("expected auth to be removed")
	}
	loop.mu.Lock()
	if _, ok := loop.index[auth.ID]; ok {
		loop.mu.Unlock()
		t.Fatalf("expected auth %q to be removed from auto-refresh index", auth.ID)
	}
	loop.mu.Unlock()
}

func TestManager_Remove_ClearsIdentityScopedRuntimeState(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	auth := &Auth{ID: "remove-state-auth", Provider: "codex", Status: StatusActive}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	stored, ok := manager.GetByID(auth.ID)
	if !ok || stored == nil {
		t.Fatal("registered auth missing")
	}
	now := time.Now()
	state := stored.ensureRuntimeLimits()
	state.mu.Lock()
	state.frozenUntil = now.Add(time.Hour)
	state.usageLimitFreezeUntil = now.Add(time.Hour)
	state.rateWindowStart = now
	state.rateWindowCount = 7
	state.codexQuotaSnapshots.Store(codexQuotaSnapshotStore{"*": {UsedRatio: 1, ExpiresAt: now.Add(time.Hour)}})
	state.mu.Unlock()

	selection := &HomeDispatchSelection{Auth: stored.Clone()}
	manager.mu.Lock()
	manager.homeRuntimeAuths["session"] = map[string]*Auth{auth.ID: stored.Clone()}
	manager.homeRuntimeAuthOwners["session"] = map[string]*HomeDispatchSelection{auth.ID: selection}
	manager.homeSessionSelections["session"] = map[homeSessionSelectionKey]*HomeDispatchSelection{
		{credentialID: auth.ID, routeModel: "gpt-5.5"}: selection,
	}
	manager.codexOverdraftRunning[auth.ID+"\x00cycle"] = struct{}{}
	manager.mu.Unlock()
	manager.requestPrepareLocks.Store(auth.ID, &sync.Mutex{})
	manager.refreshLocks.Store(auth.ID, &authRefreshLock{})

	manager.Remove(ctx, auth.ID)

	if stored.hasQuotaResetRuntimeState(now) {
		t.Fatal("removed auth retained quota or throttling runtime state")
	}
	manager.mu.RLock()
	_, runtimePresent := manager.homeRuntimeAuths["session"]
	_, ownerPresent := manager.homeRuntimeAuthOwners["session"]
	_, selectionPresent := manager.homeSessionSelections["session"]
	_, overdraftPresent := manager.codexOverdraftRunning[auth.ID+"\x00cycle"]
	manager.mu.RUnlock()
	if runtimePresent || ownerPresent || selectionPresent || overdraftPresent {
		t.Fatalf("removed auth state remains runtime=%t owner=%t selection=%t overdraft=%t", runtimePresent, ownerPresent, selectionPresent, overdraftPresent)
	}
	if _, ok := manager.requestPrepareLocks.Load(auth.ID); ok {
		t.Fatal("removed auth retained request preparation lock")
	}
	if _, ok := manager.refreshLocks.Load(auth.ID); ok {
		t.Fatal("removed auth retained refresh lock")
	}
}
