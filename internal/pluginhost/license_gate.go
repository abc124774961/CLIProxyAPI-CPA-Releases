package pluginhost

import (
	"context"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// LicenseGate is deliberately small so the plugin host does not depend on a
// particular licensing implementation. The production CPA licensing manager
// implements CheckStrict; tests and embedders can provide their own gate.
type LicenseGate interface {
	CheckStrict(feature string) (bool, string)
}

// PluginFileVerifier is implemented by the CPA licensing manager for premium
// plugins whose on-disk library is delivered as an encrypted package.  The
// host performs this check immediately before dlopen so replacing a verified
// library with another file cannot bypass the package signature and checksum.
// It is intentionally optional for third-party test gates and legacy plugins.
type PluginFileVerifier interface {
	VerifyPluginFile(pluginID, version, path string) error
}

// builtInPluginFeatures are the pre-load entitlements for plugins whose
// binary must not be opened before the license gate has been evaluated. The
// dynamic registration response is still checked after loading as a second
// line of defense, but this map closes the startup window where a premium
// library could otherwise run its init code before its metadata was known.
var builtInPluginFeatures = map[string][]string{
	"cpa-advanced-core": {"advanced_core", "ws_core", "quota_429", "engine_fingerprint", "cache_affinity", "scheduler"},
}

// SetLicenseGate installs the runtime entitlement checker and reapplies the
// current plugin configuration. Reapplication ensures an already-loaded
// premium plugin is detached immediately when a lease is missing or expired.
func (h *Host) SetLicenseGate(gate LicenseGate) {
	if h == nil {
		return
	}
	h.licenseMu.Lock()
	h.licenseGate = gate
	h.licenseMu.Unlock()
	h.mu.Lock()
	cfg := h.runtimeConfig
	h.mu.Unlock()
	if cfg != nil {
		h.ApplyConfig(context.Background(), cfg)
	}
}

func (h *Host) currentLicenseGate() LicenseGate {
	if h == nil {
		return nil
	}
	h.licenseMu.RLock()
	gate := h.licenseGate
	h.licenseMu.RUnlock()
	return gate
}

// pluginAllowed is checked at registration and at every host dispatch point.
// The latter keeps a running process closed if a lease expires between
// refresh cycles without adding any I/O to the request path.
func (h *Host) pluginAllowed(meta pluginapi.Metadata) (bool, string) {
	gate := h.currentLicenseGate()
	if len(meta.RequiredFeatures) == 0 {
		return true, ""
	}
	// A plugin that declares entitlements must never be loaded before the
	// runtime license manager has been attached.  Keeping ordinary plugins
	// compatible while failing premium plugins closed prevents a startup race
	// from becoming an authorization bypass.
	if gate == nil {
		return false, "license_gate_unavailable"
	}
	for _, feature := range meta.RequiredFeatures {
		feature = strings.TrimSpace(feature)
		if feature == "" {
			return false, "feature_required"
		}
		if ok, reason := gate.CheckStrict(feature); !ok {
			if strings.TrimSpace(reason) == "" {
				reason = "feature_not_enabled"
			}
			return false, reason
		}
	}
	return true, ""
}

func (h *Host) pluginIDLicenseError(id string) error {
	features := builtInPluginFeatures[strings.ToLower(strings.TrimSpace(id))]
	if len(features) == 0 {
		return nil
	}
	allowed, reason := h.pluginAllowed(pluginapi.Metadata{RequiredFeatures: features})
	if allowed {
		return nil
	}
	if strings.TrimSpace(reason) == "" {
		reason = "feature_not_enabled"
	}
	return fmt.Errorf("plugin license denied: %s", reason)
}

func (h *Host) pluginFileIntegrityError(file pluginFile) error {
	if h == nil || !strings.EqualFold(strings.TrimSpace(file.ID), "cpa-advanced-core") {
		return nil
	}
	verifier, ok := h.currentLicenseGate().(PluginFileVerifier)
	if !ok || verifier == nil {
		return fmt.Errorf("plugin integrity verifier unavailable")
	}
	if err := verifier.VerifyPluginFile(file.ID, file.Version, file.Path); err != nil {
		return fmt.Errorf("plugin integrity verification failed: %w", err)
	}
	return nil
}

func (h *Host) pluginLicenseError(meta pluginapi.Metadata) error {
	ok, reason := h.pluginAllowed(meta)
	if ok {
		return nil
	}
	return fmt.Errorf("plugin license denied: %s", reason)
}

func (h *Host) pluginMetadata(id string) pluginapi.Metadata {
	for _, record := range h.activeRecords() {
		if record.id == id {
			return record.meta
		}
	}
	return pluginapi.Metadata{}
}

// discardUnlicensedPlugin removes a loaded plugin without leaving its dynamic
// library in the active snapshot. It is called while ApplyConfig owns the
// lifecycle lock, then closes the client after releasing host state locks.
func (h *Host) discardUnlicensedPlugin(id string, loaded *loadedPlugin) {
	if h == nil || loaded == nil {
		return
	}
	h.mu.Lock()
	if current := h.loaded[id]; current == loaded {
		delete(h.loaded, id)
		h.removePluginRuntimeStateLocked(id)
	}
	h.mu.Unlock()
	h.discardLoadedPlugin(loaded)
}
