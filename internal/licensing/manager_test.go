package licensing

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginpkg"
)

func TestCheckUsesMemoryOnlyLeaseState(t *testing.T) {
	now := time.Now().Unix()
	m := &Manager{cfg: Config{ProductCode: "CPA", FailOpenDuringGrace: true, GracePeriod: time.Hour}, instance: "i", integrityValid: true}
	lease := Lease{LicenseID: "l", ProductCode: "CPA", InstanceID: "i", IssuedAt: now - 10, ExpiresAt: now + 3600, LeaseExpiresAt: now - 10, Nonce: "n"}
	m.lease = &SignedLease{Lease: lease}
	if ok, reason := m.Check("core"); !ok || reason != "grace" {
		t.Fatalf("expected grace, got %v/%s", ok, reason)
	}
	m.cfg.FailOpenDuringGrace = false
	m.cfg.RejectNewRequestAfterExpiry = true
	if ok, reason := m.Check("core"); ok || reason != "lease_expired" {
		t.Fatalf("expected lease expiry, got %v/%s", ok, reason)
	}
}

func TestFeatureGate(t *testing.T) {
	now := time.Now().Unix()
	m := &Manager{cfg: Config{}, instance: "i", integrityValid: true}
	m.lease = &SignedLease{Lease: Lease{LicenseID: "l", ProductCode: "CPA", InstanceID: "i", IssuedAt: now - 1, ExpiresAt: now + 10, LeaseExpiresAt: now + 10, Nonce: "n", Features: []string{"core"}}}
	if ok, _ := m.Check("cache_affinity"); ok {
		t.Fatal("expected feature gate")
	}
}

func TestServerSignedAuthorizationGracePeriod(t *testing.T) {
	now := time.Now().Unix()
	m := &Manager{
		cfg:            Config{ProductCode: "CPA", FailOpenDuringGrace: true, GracePeriod: 0},
		instance:       "i",
		integrityValid: true,
		lease: &SignedLease{Lease: Lease{
			LicenseID: "grace-i", ProductCode: "CPA", InstanceID: "i", IssuedAt: now - 10*60,
			ExpiresAt: now + 5*60*60 + 50*60, LeaseExpiresAt: now - 1, Nonce: "n",
			Grace: true, GraceStartedAt: now - 10*60, GraceUntil: now + 5*60*60 + 50*60,
		}},
	}
	if ok, reason := m.Check("core"); !ok || reason != "grace" {
		t.Fatalf("expected server grace, got %v/%s", ok, reason)
	}
	m.lease.Lease.GraceUntil = now - 1
	if ok, reason := m.Check("core"); ok || reason != "not_activated" {
		t.Fatalf("expected expired server grace, got %v/%s", ok, reason)
	}
}

func TestExpiredLicenseUsesSignedExpiryGrace(t *testing.T) {
	now := time.Now().Unix()
	m := &Manager{cfg: Config{ProductCode: "CPA"}, instance: "i", integrityValid: true}
	m.lease = &SignedLease{Lease: Lease{
		LicenseID: "licensed-i", ProductCode: "CPA", InstanceID: "i", IssuedAt: now - 3600,
		ExpiresAt: now - 60, LeaseExpiresAt: now + 300, Nonce: "n", Features: []string{"core", "advanced_core"},
		ExpiryGraceStartedAt: now - 60, ExpiryGraceUntil: now + 3600,
	}}
	if ok, reason := m.Check("advanced_core"); !ok || reason != "expiry_grace" {
		t.Fatalf("expected expiry grace, got %v/%s", ok, reason)
	}
	if ok, reason := m.CheckStrict("advanced_core"); !ok || reason != "expiry_grace" {
		t.Fatalf("strict check did not honor expiry grace, got %v/%s", ok, reason)
	}
	m.lease.Lease.ExpiryGraceUntil = now - 1
	if ok, reason := m.Check("core"); ok || reason != "license_expired" {
		t.Fatalf("expected expired license after grace, got %v/%s", ok, reason)
	}
}

func TestLocalFallbackGracePeriodIsCapped(t *testing.T) {
	now := time.Now()
	standard := &StandardResult{
		LicenseID:       "standard-license",
		LicenseKey:      "standard-key",
		MachineCode:     "machine-code",
		VerificationURL: "http://127.0.0.1:18320/verify",
		ExpiresAt:       now.Add(24 * time.Hour).UTC().Format(time.RFC3339),
		LastVerifiedAt:  now.Add(-7 * time.Hour).Unix(),
	}
	standardManager := &Manager{
		cfg:              Config{ProductCode: "CPA", FailOpenDuringGrace: true, GracePeriod: 48 * time.Hour},
		standard:         standard,
		integrityValid:   true,
		integrityChecked: false,
	}
	if allowed, reason := standardManager.Check("core"); allowed || reason != "lease_expired" {
		t.Fatalf("standard fallback exceeded six-hour cap: got %v/%s", allowed, reason)
	}
	standard.LastVerifiedAt = now.Add(-5 * time.Hour).Unix()
	if allowed, reason := standardManager.Check("core"); !allowed || reason != "grace" {
		t.Fatalf("standard fallback inside six-hour cap was rejected: got %v/%s", allowed, reason)
	}

	modernManager := &Manager{
		cfg: Config{
			ProductCode:                 "CPA",
			FailOpenDuringGrace:         true,
			GracePeriod:                 48 * time.Hour,
			RejectNewRequestAfterExpiry: true,
		},
		instance:       "i",
		integrityValid: true,
		lease: &SignedLease{Lease: Lease{
			LicenseID: "signed-license", ProductCode: "CPA", InstanceID: "i",
			IssuedAt: now.Add(-24 * time.Hour).Unix(), ExpiresAt: now.Add(24 * time.Hour).Unix(),
			LeaseExpiresAt: now.Add(-7 * time.Hour).Unix(), Nonce: "n",
		}},
	}
	if allowed, reason := modernManager.Check("core"); allowed || reason != "lease_expired" {
		t.Fatalf("signed lease fallback exceeded six-hour cap: got %v/%s", allowed, reason)
	}
	modernManager.lease.Lease.LeaseExpiresAt = now.Add(-5 * time.Hour).Unix()
	if allowed, reason := modernManager.Check("core"); !allowed || reason != "grace" {
		t.Fatalf("signed lease fallback inside six-hour cap was rejected: got %v/%s", allowed, reason)
	}
}

func TestStrictFeatureGateRequiresExplicitFeature(t *testing.T) {
	now := time.Now().Unix()
	m := &Manager{cfg: Config{}, instance: "i", integrityValid: true}
	m.lease = &SignedLease{Lease: Lease{LicenseID: "l", ProductCode: "CPA", InstanceID: "i", IssuedAt: now - 1, ExpiresAt: now + 10, LeaseExpiresAt: now + 10, Nonce: "n"}}
	if ok, reason := m.CheckStrict("advanced_core"); ok || reason != "feature_not_enabled" {
		t.Fatalf("empty feature set result = %v/%s", ok, reason)
	}
	m.lease.Lease.Features = []string{"advanced_core"}
	if ok, reason := m.CheckStrict("advanced_core"); !ok || reason != "active" {
		t.Fatalf("explicit feature result = %v/%s", ok, reason)
	}
}

func TestPluginKeyBindsLeaseMaterial(t *testing.T) {
	now := time.Now().Unix()
	m := &Manager{cfg: Config{}, instance: "i", integrityValid: true}
	m.lease = &SignedLease{Lease: Lease{LicenseID: "l", ProductCode: "CPA", InstanceID: "i", IssuedAt: now - 1, ExpiresAt: now + 10, LeaseExpiresAt: now + 10, Nonce: "n", Features: []string{"advanced_core"}}}
	a, err := m.PluginKey("cpa-advanced-core", "1.0.0")
	if err != nil || len(a) != 32 {
		t.Fatalf("PluginKey() = %x/%v", a, err)
	}
	m.lease.Lease.Nonce = "other"
	b, err := m.PluginKey("cpa-advanced-core", "1.0.0")
	if err != nil || string(a) == string(b) {
		t.Fatalf("nonce did not change derived key")
	}
}

func TestVerifyPluginFileUsesCachedSignedPackage(t *testing.T) {
	publicKey, privateKey, errKey := ed25519.GenerateKey(nil)
	if errKey != nil {
		t.Fatal(errKey)
	}
	stateDir := t.TempDir()
	now := time.Now().Unix()
	lease := Lease{
		LicenseID: "license-a", ProductCode: "CPA", InstanceID: "instance-a",
		IssuedAt: now - 1, ExpiresAt: now + 3600, LeaseExpiresAt: now + 3600,
		Nonce: "lease-nonce", Features: []string{"advanced_core"},
	}
	manager := &Manager{
		cfg:      Config{StateDir: stateDir, ProductCode: "CPA", PluginPublicKey: publicKey},
		instance: "instance-a", integrityValid: true, lease: &SignedLease{Lease: lease},
	}
	library := []byte("verified dynamic library bytes")
	packageData, errBuild := pluginpkg.Build(pluginpkg.BuildOptions{
		PluginID: "cpa-advanced-core", Version: "1.0.0", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		RequiredFeatures: []string{"advanced_core"}, LicenseID: lease.LicenseID,
		InstanceID: lease.InstanceID, LeaseNonce: lease.Nonce, SigningKey: privateKey, Library: library,
	})
	if errBuild != nil {
		t.Fatal(errBuild)
	}
	packagePath := filepath.Join(stateDir, "plugin-packages", "cpa-advanced-core", "1.0.0", runtime.GOOS+"-"+runtime.GOARCH+".pkg")
	if err := os.MkdirAll(filepath.Dir(packagePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(packagePath, packageData, 0o600); err != nil {
		t.Fatal(err)
	}
	libraryPath := filepath.Join(t.TempDir(), "cpa-advanced-core-v1.0.0"+map[bool]string{true: ".dylib", false: ".so"}[runtime.GOOS == "darwin"])
	if err := os.WriteFile(libraryPath, library, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := manager.VerifyPluginFile("cpa-advanced-core", "1.0.0", libraryPath); err != nil {
		t.Fatalf("VerifyPluginFile(valid) = %v", err)
	}
	if err := os.WriteFile(libraryPath, []byte("replaced dynamic library bytes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := manager.VerifyPluginFile("cpa-advanced-core", "1.0.0", libraryPath); err == nil {
		t.Fatal("tampered plugin library was accepted")
	}
}

func TestInitializeStorageFailureFailsClosed(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	statePath := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(statePath, []byte("not-a-directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPA_LICENSE_PUBLIC_KEY", base64.RawURLEncoding.EncodeToString(pub))
	t.Setenv("CPA_LICENSE_STATE_DIR", statePath)
	old := Global()
	defer func() {
		global.Lock()
		global.manager = old
		global.Unlock()
	}()
	m, err := Initialize()
	if err == nil {
		t.Fatal("expected state directory initialization error")
	}
	if m == nil {
		t.Fatal("expected fail-closed manager")
	}
	if allowed, reason := m.Check("core"); allowed || reason != "integrity_failed" {
		t.Fatalf("expected fail-closed integrity result, got %v/%s", allowed, reason)
	}
}
