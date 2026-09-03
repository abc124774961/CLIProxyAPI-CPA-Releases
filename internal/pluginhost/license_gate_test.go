package pluginhost

import (
	"context"
	"errors"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type testLicenseGate map[string]bool

func (g testLicenseGate) CheckStrict(feature string) (bool, string) {
	if g[feature] {
		return true, "active"
	}
	return false, "feature_not_enabled"
}

type testPluginFileLicenseGate struct {
	testLicenseGate
	verifyCalls int
	verifyErr   error
}

func (g *testPluginFileLicenseGate) VerifyPluginFile(_, _, _ string) error {
	g.verifyCalls++
	return g.verifyErr
}

func TestHostLicenseGateWithholdsPremiumPlugin(t *testing.T) {
	loader := newTestSymbolLoader()
	plugin := &testPlugin{registerResult: validTestPlugin("premium"), reconfigureResult: validTestPlugin("premium")}
	plugin.registerResult.Metadata.RequiredFeatures = []string{"advanced_core"}
	plugin.reconfigureResult.Metadata.RequiredFeatures = []string{"advanced_core"}
	loader.lookups["premium"] = newTestSymbolLookup(plugin)
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	h.SetLicenseGate(testLicenseGate{})
	h.ApplyConfig(context.Background(), &config.Config{Plugins: config.PluginsConfig{Enabled: true, Dir: makePluginDir(t, "premium"), Configs: enabledPluginConfigs("premium")}})
	if h.PluginRegistered("premium") {
		t.Fatal("premium plugin registered without required feature")
	}
	if h.PluginLoaded("premium") {
		t.Fatal("premium plugin remained loaded without required feature")
	}
}

func TestHostLicenseGateSkipsCPAAdvancedPluginBeforeOpen(t *testing.T) {
	loader := newTestSymbolLoader()
	loader.lookups["cpa-advanced-core"] = newTestSymbolLookup(&testPlugin{registerResult: validTestPlugin("cpa-advanced-core")})
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	h.ApplyConfig(context.Background(), &config.Config{Plugins: config.PluginsConfig{Enabled: true, Dir: makePluginDir(t, "cpa-advanced-core"), Configs: enabledPluginConfigs("cpa-advanced-core")}})
	if loader.openCalls != 0 {
		t.Fatalf("premium plugin was opened before license preflight: %d calls", loader.openCalls)
	}
}

func TestHostLicenseGateVerifiesCPAAdvancedPluginBeforeOpen(t *testing.T) {
	loader := newTestSymbolLoader()
	plugin := &testPlugin{registerResult: validTestPlugin("cpa-advanced-core"), reconfigureResult: validTestPlugin("cpa-advanced-core")}
	loader.lookups["cpa-advanced-core"] = newTestSymbolLookup(plugin)
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	features := testLicenseGate{
		"advanced_core": true, "ws_core": true, "quota_429": true,
		"engine_fingerprint": true, "cache_affinity": true, "scheduler": true,
	}
	gate := &testPluginFileLicenseGate{testLicenseGate: features}
	h.SetLicenseGate(gate)
	cfg := &config.Config{Plugins: config.PluginsConfig{Enabled: true, Dir: makePluginDir(t, "cpa-advanced-core"), Configs: enabledPluginConfigs("cpa-advanced-core")}}
	h.ApplyConfig(context.Background(), cfg)
	if gate.verifyCalls != 1 || loader.openCalls != 1 {
		t.Fatalf("verify/open calls = %d/%d, want 1/1", gate.verifyCalls, loader.openCalls)
	}

	h.ShutdownAll()
	loader.openCalls = 0
	gate.verifyCalls = 0
	gate.verifyErr = errors.New("checksum mismatch")
	h.ApplyConfig(context.Background(), cfg)
	if gate.verifyCalls != 1 || loader.openCalls != 0 {
		t.Fatalf("failed verification calls = %d/%d, want 1/0", gate.verifyCalls, loader.openCalls)
	}
}

func TestHostLicenseGateWithholdsPremiumPluginWhenGateIsMissing(t *testing.T) {
	loader := newTestSymbolLoader()
	plugin := &testPlugin{registerResult: validTestPlugin("premium"), reconfigureResult: validTestPlugin("premium")}
	plugin.registerResult.Metadata.RequiredFeatures = []string{"advanced_core"}
	plugin.reconfigureResult.Metadata.RequiredFeatures = []string{"advanced_core"}
	loader.lookups["premium"] = newTestSymbolLookup(plugin)
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	h.ApplyConfig(context.Background(), &config.Config{Plugins: config.PluginsConfig{Enabled: true, Dir: makePluginDir(t, "premium"), Configs: enabledPluginConfigs("premium")}})
	if h.PluginRegistered("premium") || h.PluginLoaded("premium") {
		t.Fatal("premium plugin registered without a license gate")
	}
}

func TestHostLicenseGateAllowsAndRevokesPremiumPlugin(t *testing.T) {
	loader := newTestSymbolLoader()
	plugin := &testPlugin{registerResult: validTestPlugin("premium"), reconfigureResult: validTestPlugin("premium")}
	plugin.registerResult.Metadata.RequiredFeatures = []string{"advanced_core"}
	plugin.reconfigureResult.Metadata.RequiredFeatures = []string{"advanced_core"}
	loader.lookups["premium"] = newTestSymbolLookup(plugin)
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	gate := testLicenseGate{"advanced_core": true}
	h.SetLicenseGate(gate)
	h.ApplyConfig(context.Background(), &config.Config{Plugins: config.PluginsConfig{Enabled: true, Dir: makePluginDir(t, "premium"), Configs: enabledPluginConfigs("premium")}})
	if !h.PluginRegistered("premium") {
		t.Fatal("premium plugin not registered with feature")
	}
	delete(gate, "advanced_core")
	if h.HasScheduler() {
		// The gate is checked on each dispatch, so existing snapshots stop
		// invoking a premium scheduler as soon as entitlement is removed.
		if _, handled, err := h.PickAuth(context.Background(), pluginapi.SchedulerPickRequest{}); err != nil || handled {
			t.Fatalf("revoked scheduler dispatch = handled %v err %v", handled, err)
		}
	}
}
