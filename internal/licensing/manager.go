package licensing

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginpkg"
)

var (
	ErrNotActivated = errors.New("license is not activated")
	ErrExpired      = errors.New("license lease has expired")
	ErrFeature      = errors.New("license feature is not enabled")
)

type Manager struct {
	cfg              Config
	client           *Client
	instance         string
	machineCode      string
	mu               sync.RWMutex
	lease            *SignedLease
	claim            *Claim
	standard         *StandardResult
	lastErr          string
	verified         time.Time
	refreshed        time.Time
	integrityChecked bool
	integrityValid   bool
	stop             context.CancelFunc
	lifecycleMu      sync.Mutex
	refreshMu        sync.Mutex
	storageKey       []byte
	shopMu           sync.Mutex
	shopStates       map[string]shopState
}

var global struct {
	sync.RWMutex
	manager *Manager
}

func Initialize() (*Manager, error) {
	cfg, err := LoadConfig()
	if err != nil {
		// Keep the process available so an operator can correct configuration via
		// deployment secrets; the request path remains closed until fixed.
		m := &Manager{cfg: cfg, lastErr: "configuration_error", integrityValid: false}
		global.Lock()
		global.manager = m
		global.Unlock()
		return m, err
	}
	return InitializeWithConfig(cfg)
}

// InitializeWithConfig installs a process-wide manager backed by the supplied
// CPA configuration. A configuration or storage failure remains visible as a
// fail-closed state.
func InitializeWithConfig(cfg Config) (*Manager, error) {
	// ConfigFromOptions already applies this bound, but callers may construct a
	// Config directly (including embedded SDK users). Clamp again at the manager
	// boundary so local network-failure fallback cannot be enlarged by a custom
	// integration or a hot-reloaded config value.
	cfg.GracePeriod = clampLocalGracePeriod(cfg.GracePeriod)
	id, err := instanceID(cfg.StateDir)
	if err != nil {
		// Keep a fail-closed manager visible to the relay middleware. Returning a
		// nil manager here would make a storage failure look like disabled
		// licensing and unintentionally open the request path.
		m := &Manager{
			cfg:            cfg,
			client:         newClient(cfg),
			lastErr:        "instance_storage_error",
			integrityValid: false,
		}
		global.Lock()
		global.manager = m
		global.Unlock()
		return m, err
	}
	m := &Manager{cfg: cfg, client: newClient(cfg), instance: id, integrityValid: true, storageKey: deriveStorageKey(cfg, id), shopStates: make(map[string]shopState)}
	if len(id) >= 32 {
		m.machineCode = "CPA-" + id[:32]
	}
	if cfg.ExpectedExecutableSHA256 != "" {
		m.integrityChecked = true
		m.integrityValid = verifyExecutableHash(cfg.ExpectedExecutableSHA256)
		if !m.integrityValid {
			m.lastErr = "integrity_failed"
		}
	}
	if signed, loadErr := loadLeaseWithKey(cfg.StateDir, m.storageKey); loadErr != nil {
		m.lastErr = "lease_storage_error"
	} else if signed != nil {
		if _, verifyErr := verifySignedLease(*signed, cfg.PublicKey, cfg.ProductCode, expectedInstance(cfg, id), time.Now().Unix()); verifyErr == nil {
			m.lease = signed
			m.verified = time.Now()
		} else {
			m.lastErr = "lease_invalid"
		}
	}
	if claimPath := resolveClaimPath(cfg.ClaimPath, cfg.StateDir); claimPath != "" {
		if claim, claimErr := loadClaim(claimPath, cfg.PublicKey, time.Now()); claimErr == nil {
			m.claim = claim
		} else {
			m.lastErr = "claim_invalid"
		}
	}
	if m.lease == nil {
		if standard, stateErr := loadStandardStateWithKey(cfg.StateDir, m.storageKey); stateErr == nil && standard != nil {
			machineMatches := cfg.InstanceBinding == "portable" || standard.MachineCode == m.machineCode
			if machineMatches && (m.claim == nil || m.claim.LicenseID == standard.LicenseID) {
				m.standard = standard
				if standard.LastVerifiedAt > 0 {
					m.verified = time.Unix(standard.LastVerifiedAt, 0)
				}
			}
		} else if stateErr != nil {
			m.lastErr = "standard_state_invalid"
		}
	}
	global.Lock()
	global.manager = m
	global.Unlock()
	return m, nil
}

func Global() *Manager { global.RLock(); defer global.RUnlock(); return global.manager }

func (m *Manager) Config() Config     { return m.cfg }
func (m *Manager) InstanceID() string { return m.instance }

func (m *Manager) Start() {
	if m == nil || !m.integrityValid {
		return
	}
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if m.stop != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.stop = cancel
	go func() {
		// Refresh asynchronously. It never blocks startup or request handling.
		m.refreshBackground(ctx)
		t := time.NewTicker(m.cfg.RefreshInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.refreshBackground(ctx)
			}
		}
	}()
}

func (m *Manager) Stop() {
	if m == nil {
		return
	}
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if m.stop == nil {
		return
	}
	m.stop()
	m.stop = nil
}

func (m *Manager) refreshBackground(ctx context.Context) {
	refreshCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	if err := m.Refresh(refreshCtx); err != nil {
		m.setError(err)
	}
}

// acquireGrace obtains the server-signed initial grace lease. The storefront
// persists the window and controls its duration; CPA never derives or extends
// this period from local configuration.
func (m *Manager) acquireGrace(ctx context.Context) error {
	if m == nil || m.client == nil {
		return ErrNotActivated
	}
	signed, err := m.client.AcquireGrace(ctx, m.instance)
	if err != nil {
		m.setError(err)
		return err
	}
	now := time.Now()
	lease, err := verifySignedLease(signed, m.cfg.PublicKey, m.cfg.ProductCode, expectedInstance(m.cfg, m.instance), now.Unix())
	if err != nil {
		m.setError(err)
		return err
	}
	if !lease.Grace || lease.GraceUntil <= 0 || lease.GraceStartedAt <= 0 || lease.GraceUntil < lease.GraceStartedAt {
		err = fmt.Errorf("license provider returned an invalid grace lease")
		m.setError(err)
		return err
	}
	if err := validateLeaseTimes(lease, now); err != nil {
		m.setError(err)
		return err
	}
	if err := persistLeaseWithKey(m.cfg.StateDir, signed, m.storageKey); err != nil {
		m.setError(err)
		return err
	}
	m.mu.Lock()
	m.lease = &signed
	m.standard = nil
	m.verified = now
	m.refreshed = now
	m.lastErr = ""
	m.mu.Unlock()
	return nil
}

func (m *Manager) Activate(ctx context.Context, code string) error {
	if m == nil {
		return ErrNotActivated
	}
	if strings.TrimSpace(code) == "" {
		return fmt.Errorf("activation code is empty")
	}
	if m.client == nil {
		return fmt.Errorf("license provider is not configured")
	}
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	signed, err := m.client.Activate(ctx, code, m.instance)
	if err != nil {
		m.setError(err)
		return err
	}
	lease, err := verifySignedLease(signed, m.cfg.PublicKey, m.cfg.ProductCode, expectedInstance(m.cfg, m.instance), time.Now().Unix())
	if err != nil {
		m.setError(err)
		return err
	}
	if err := validateLeaseTimes(lease, time.Now()); err != nil {
		m.setError(err)
		return err
	}
	if err := persistLeaseWithKey(m.cfg.StateDir, signed, m.storageKey); err != nil {
		m.setError(err)
		return err
	}
	if err := removeStandardState(m.cfg.StateDir); err != nil {
		m.setError(err)
		return err
	}
	m.mu.Lock()
	m.lease = &signed
	m.standard = nil
	m.verified = time.Now()
	m.refreshed = time.Now()
	m.lastErr = ""
	m.mu.Unlock()
	return nil
}

func (m *Manager) Refresh(ctx context.Context) (err error) {
	if m == nil {
		return nil
	}
	if m.client == nil {
		return fmt.Errorf("license provider is not configured")
	}
	defer func() {
		if err != nil {
			m.setError(err)
		}
	}()
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	m.mu.RLock()
	current := m.lease
	standard := m.standard
	claim := m.claim
	machineCode := m.machineCode
	m.mu.RUnlock()
	if standard != nil {
		if err := m.client.VerifyStandard(ctx, *standard); err != nil {
			return err
		}
		verifiedAt := time.Now()
		next := *standard
		next.LastVerifiedAt = verifiedAt.Unix()
		if err := persistStandardStateWithKey(m.cfg.StateDir, next, m.storageKey); err != nil {
			return err
		}
		m.mu.Lock()
		m.standard = &next
		m.verified = verifiedAt
		m.refreshed = verifiedAt
		m.lastErr = ""
		m.mu.Unlock()
		return nil
	}
	if current == nil && claim != nil {
		if machineCode == "" {
			return fmt.Errorf("machine code is unavailable")
		}
		result, err := m.client.ActivateClaim(ctx, *claim, machineCode)
		if err != nil {
			return err
		}
		if err := persistStandardStateWithKey(m.cfg.StateDir, result, m.storageKey); err != nil {
			return err
		}
		m.mu.Lock()
		m.standard = &result
		m.verified = time.Now()
		m.refreshed = time.Now()
		m.lastErr = ""
		m.mu.Unlock()
		return nil
	}
	if current == nil {
		return m.acquireGrace(ctx)
	}
	lease := current.NormalizedLease()
	if lease.Grace {
		return m.acquireGrace(ctx)
	}
	signed, err := m.client.Refresh(ctx, lease, m.instance)
	if err != nil {
		return err
	}
	verified, err := verifySignedLease(signed, m.cfg.PublicKey, m.cfg.ProductCode, expectedInstance(m.cfg, m.instance), time.Now().Unix())
	if err != nil {
		return err
	}
	if err := validateLeaseTimes(verified, time.Now()); err != nil {
		return err
	}
	if err := persistLeaseWithKey(m.cfg.StateDir, signed, m.storageKey); err != nil {
		return err
	}
	m.mu.Lock()
	m.lease = &signed
	m.verified = time.Now()
	m.refreshed = time.Now()
	m.lastErr = ""
	m.mu.Unlock()
	return nil
}

func expectedInstance(cfg Config, instance string) string {
	if cfg.InstanceBinding == "portable" {
		return ""
	}
	return instance
}

func validateLeaseTimes(lease Lease, now time.Time) error {
	if lease.ExpiresAt <= now.Add(-5*time.Minute).Unix() && lease.ExpiryGraceUntil <= now.Unix() {
		return ErrExpired
	}
	if lease.ExpiryGraceUntil > 0 && lease.ExpiryGraceUntil <= lease.ExpiresAt {
		return fmt.Errorf("expiry grace window is invalid")
	}
	if lease.LeaseExpiresAt <= 0 {
		return fmt.Errorf("lease expiration is missing")
	}
	return nil
}

func (m *Manager) setError(err error) {
	if err == nil {
		return
	}
	m.mu.Lock()
	m.lastErr = PublicErrorCode(err)
	m.mu.Unlock()
}

// Check is a read-only, lock-protected hot-path check. It never performs I/O.
func (m *Manager) Check(feature string) (bool, string) {
	if m == nil {
		return false, "not_initialized"
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.checkLocked(feature)
}

// CheckStrict is the feature gate used by encrypted plugins. Unlike the
// legacy Check method, an empty feature list never grants access to a plugin
// that declares a required feature. This keeps older CPA leases compatible
// for the core request path while making premium plugin capabilities explicit.
func (m *Manager) CheckStrict(feature string) (bool, string) {
	feature = strings.TrimSpace(feature)
	if feature == "" {
		return false, "feature_required"
	}
	if m == nil {
		return false, "not_initialized"
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.standard != nil {
		// The legacy standard activation format has no signed feature set. It
		// therefore cannot unlock encrypted premium plugins.
		return false, "feature_not_enabled"
	}
	if m.lease == nil {
		return false, "not_activated"
	}
	if m.lease.NormalizedLease().Grace && feature != "core" {
		return false, "feature_not_enabled"
	}
	allowed, reason := m.checkLocked(feature)
	if !allowed {
		return false, reason
	}
	lease := m.lease.NormalizedLease()
	if len(lease.Features) == 0 || !contains(lease.Features, feature) {
		return false, "feature_not_enabled"
	}
	return true, reason
}

// PluginKey derives the per-instance decryption key for an encrypted plugin.
// The derivation is memory-only and binds the key to the signed lease, CPA
// instance, plugin identifier, and exact version.
func (m *Manager) PluginKey(pluginID, version string) ([]byte, error) {
	pluginID = strings.TrimSpace(pluginID)
	version = strings.TrimSpace(version)
	if pluginID == "" || version == "" {
		return nil, fmt.Errorf("plugin id and version are required")
	}
	if ok, reason := m.CheckStrict("advanced_core"); !ok {
		return nil, fmt.Errorf("plugin authorization denied: %s", reason)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.lease == nil {
		return nil, ErrNotActivated
	}
	lease := m.lease.NormalizedLease()
	return pluginpkg.DeriveKey(lease.LicenseID, lease.InstanceID, lease.Nonce, pluginID, version)
}

// OpenPluginPackage verifies and decrypts an encrypted premium-plugin archive
// using the currently active signed lease. Lease material remains inside the
// manager; callers receive only the verified manifest and library bytes.
func (m *Manager) OpenPluginPackage(data []byte, pluginID, version, goos, goarch string) (pluginpkg.Opened, error) {
	if m == nil {
		return pluginpkg.Opened{}, ErrNotActivated
	}
	pluginID = strings.TrimSpace(pluginID)
	version = strings.TrimSpace(version)
	if pluginID == "" {
		return pluginpkg.Opened{}, fmt.Errorf("plugin id is required")
	}
	if ok, reason := m.CheckStrict("advanced_core"); !ok {
		return pluginpkg.Opened{}, fmt.Errorf("plugin authorization denied: %s", reason)
	}
	m.mu.RLock()
	lease := m.lease
	publicKey := append([]byte(nil), m.cfg.PluginPublicKey...)
	if len(publicKey) != ed25519.PublicKeySize {
		publicKey = append([]byte(nil), m.cfg.PublicKey...)
	}
	instance := m.instance
	m.mu.RUnlock()
	if lease == nil {
		return pluginpkg.Opened{}, ErrNotActivated
	}
	normalized := lease.NormalizedLease()
	// The storefront accepts an empty version as "latest".  Peek only the
	// public manifest to discover that version, then run the complete Open
	// verification (signature, feature, instance, nonce and checksums).
	if version == "" {
		manifest, errPeek := pluginpkg.PeekManifest(data)
		if errPeek != nil {
			return pluginpkg.Opened{}, errPeek
		}
		if !strings.EqualFold(strings.TrimSpace(manifest.PluginID), pluginID) {
			return pluginpkg.Opened{}, fmt.Errorf("plugin package id mismatch")
		}
		version = manifest.Version
	}
	opened, errOpen := pluginpkg.Open(data, pluginpkg.OpenOptions{
		GOOS: goos, GOARCH: goarch,
		RequiredFeatures: normalized.Features,
		LicenseID:        normalized.LicenseID, InstanceID: instance,
		LeaseNonce: normalized.Nonce, VerifyKey: publicKey,
	})
	if errOpen != nil {
		return pluginpkg.Opened{}, errOpen
	}
	normalizedVersion := strings.TrimPrefix(strings.TrimPrefix(version, "v"), "V")
	if strings.TrimSpace(opened.Manifest.Version) != normalizedVersion {
		return pluginpkg.Opened{}, fmt.Errorf("plugin package version mismatch")
	}
	if !strings.EqualFold(strings.TrimSpace(opened.Manifest.PluginID), pluginID) {
		return pluginpkg.Opened{}, fmt.Errorf("plugin package id mismatch")
	}
	return opened, nil
}

// DownloadPluginPackage obtains an encrypted package from the configured
// storefront. The signed lease is sent server-to-server and is never exposed
// to the management UI.
func (m *Manager) DownloadPluginPackage(ctx context.Context, pluginID, version, goos, goarch string) ([]byte, error) {
	if m == nil {
		return nil, ErrNotActivated
	}
	if ok, reason := m.CheckStrict("advanced_core"); !ok {
		return nil, fmt.Errorf("plugin authorization denied: %s", reason)
	}
	m.mu.RLock()
	lease := m.lease
	client := m.client
	m.mu.RUnlock()
	if lease == nil || client == nil {
		return nil, ErrNotActivated
	}
	return client.DownloadEncryptedPlugin(ctx, *lease, pluginID, version, goos, goarch)
}

// VerifyPluginFile re-opens the encrypted package cached for the current
// lease and compares its verified payload digest with the library selected by
// the host.  A missing cache entry, a changed lease nonce, or any on-disk
// replacement therefore fails closed before the dynamic loader gets a chance
// to execute library initialization code.
func (m *Manager) VerifyPluginFile(pluginID, version, path string) error {
	if m == nil {
		return ErrNotActivated
	}
	pluginID = strings.TrimSpace(pluginID)
	version = strings.TrimSpace(version)
	path = strings.TrimSpace(path)
	if pluginID == "" || version == "" || path == "" {
		return fmt.Errorf("plugin identity is incomplete")
	}
	if ok, reason := m.CheckStrict("advanced_core"); !ok {
		return fmt.Errorf("plugin authorization denied: %s", reason)
	}
	stateDir := strings.TrimSpace(m.cfg.StateDir)
	if stateDir == "" {
		return fmt.Errorf("license state directory is empty")
	}
	goos, goarch := runtime.GOOS, runtime.GOARCH
	packagePath := filepath.Join(stateDir, "plugin-packages", pluginID, normalizePluginVersion(version), goos+"-"+goarch+".pkg")
	packageInfo, errStat := os.Stat(packagePath)
	if errStat != nil {
		return fmt.Errorf("verified plugin package is unavailable: %w", errStat)
	}
	if packageInfo.IsDir() || packageInfo.Size() <= 0 || packageInfo.Size() > pluginpkg.DefaultMaxPackage {
		return fmt.Errorf("verified plugin package has invalid size")
	}
	packageData, errRead := os.ReadFile(packagePath)
	if errRead != nil {
		return fmt.Errorf("read verified plugin package: %w", errRead)
	}
	opened, errOpen := m.OpenPluginPackage(packageData, pluginID, version, goos, goarch)
	if errOpen != nil {
		return errOpen
	}
	fileInfo, errFile := os.Stat(path)
	if errFile != nil {
		return fmt.Errorf("stat plugin library: %w", errFile)
	}
	if fileInfo.IsDir() || fileInfo.Size() != int64(len(opened.Payload)) || fileInfo.Size() <= 0 || fileInfo.Size() > pluginpkg.DefaultMaxPayload {
		return fmt.Errorf("plugin library size does not match verified package")
	}
	file, errOpenFile := os.Open(path)
	if errOpenFile != nil {
		return fmt.Errorf("open plugin library: %w", errOpenFile)
	}
	defer file.Close()
	digest := sha256.New()
	if _, errCopy := io.CopyN(digest, file, fileInfo.Size()); errCopy != nil {
		return fmt.Errorf("hash plugin library: %w", errCopy)
	}
	verifiedDigest := sha256.Sum256(opened.Payload)
	if !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), hex.EncodeToString(verifiedDigest[:])) {
		return fmt.Errorf("plugin library checksum does not match verified package")
	}
	return nil
}

func normalizePluginVersion(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 1 && (value[0] == 'v' || value[0] == 'V') {
		return value[1:]
	}
	return value
}

func contains(items []string, value string) bool {
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item), value) {
			return true
		}
	}
	return false
}

func (m *Manager) Status() PublicStatus {
	if m == nil {
		return PublicStatus{Reason: "not_initialized"}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	st := PublicStatus{Enabled: true, Configured: len(m.cfg.PublicKey) > 0, Provider: m.cfg.Provider, ProductCode: m.cfg.ProductCode, InstanceID: shortID(m.instance), InstanceBound: m.instance != "", IntegrityChecked: m.integrityChecked, IntegrityValid: m.integrityValid, LastRefreshError: m.lastErr}
	if m.standard != nil {
		st.ActivationMode = "shop666-claim"
		st.LicenseID = m.standard.LicenseID
		st.ExpiresAt = standardExpiryUnix(*m.standard, m.claim)
		if m.standard.LastVerifiedAt > 0 {
			st.LastVerifiedAt = m.standard.LastVerifiedAt
		}
	} else if m.lease != nil {
		st.ActivationMode = "signed-lease"
		l := m.lease.NormalizedLease()
		st.LicenseID = l.LicenseID
		st.ProductName = l.ProductName
		st.Features = append([]string(nil), l.Features...)
		st.ExpiresAt = l.ExpiresAt
		st.LeaseExpiresAt = l.LeaseExpiresAt
		if l.Grace {
			st.GraceStartedAt = l.GraceStartedAt
			st.GraceUntil = l.GraceUntil
			if l.GraceUntil > l.GraceStartedAt {
				st.GracePeriodSeconds = l.GraceUntil - l.GraceStartedAt
				remaining := l.GraceUntil - time.Now().Unix()
				if remaining > 0 {
					st.GraceRemainingSecs = remaining
				}
			}
		}
		if l.ExpiryGraceUntil > 0 {
			st.ExpiryGraceStartedAt = l.ExpiryGraceStartedAt
			st.ExpiryGraceUntil = l.ExpiryGraceUntil
			remaining := l.ExpiryGraceUntil - time.Now().Unix()
			if remaining > 0 {
				st.ExpiryGrace = true
				st.ExpiryGraceRemainingSecs = remaining
			}
		}
	}
	if !m.verified.IsZero() {
		st.LastVerifiedAt = m.verified.Unix()
	}
	if !m.refreshed.IsZero() {
		st.LastRefreshAt = m.refreshed.Unix()
	}
	allowed, reason := m.checkLocked("")
	st.Allowed = allowed
	st.Reason = reason
	st.Valid = reason == "active" || reason == "grace" || reason == "expiry_grace"
	st.InGrace = reason == "grace" || reason == "expiry_grace"
	return st
}

func (m *Manager) checkLocked(feature string) (bool, string) {
	if m == nil {
		return false, "not_initialized"
	}
	if !m.integrityValid {
		return false, "integrity_failed"
	}
	localGracePeriod := clampLocalGracePeriod(m.cfg.GracePeriod)
	if m.standard != nil {
		result := *m.standard
		now := time.Now()
		if expires := standardExpiryTime(result, m.claim); !expires.IsZero() && now.After(expires) {
			return false, "license_expired"
		}
		lastVerified := time.Unix(result.LastVerifiedAt, 0)
		if result.LastVerifiedAt > 0 && now.Sub(lastVerified) <= m.cfg.RefreshInterval*2 {
			if feature == "" || len(m.leaseFeatures()) == 0 || contains(m.leaseFeatures(), feature) {
				return true, "active"
			}
			return false, "feature_not_enabled"
		}
		if result.LastVerifiedAt > 0 && m.cfg.FailOpenDuringGrace && now.Sub(lastVerified) <= localGracePeriod {
			return true, "grace"
		}
		return false, "lease_expired"
	}
	if m.lease == nil {
		return false, "not_activated"
	}
	lease := m.lease.NormalizedLease()
	now := time.Now().Unix()
	if lease.Grace {
		if lease.GraceUntil > 0 && now < lease.GraceUntil {
			return true, "grace"
		}
		return false, "not_activated"
	}
	if lease.ExpiresAt > 0 && now > lease.ExpiresAt {
		if lease.ExpiryGraceUntil > now {
			if feature != "" && len(lease.Features) > 0 && !contains(lease.Features, feature) {
				return false, "feature_not_enabled"
			}
			return true, "expiry_grace"
		}
		return false, "license_expired"
	}
	if lease.LeaseExpiresAt > 0 && now > lease.LeaseExpiresAt {
		if m.cfg.FailOpenDuringGrace && now <= lease.LeaseExpiresAt+int64(localGracePeriod.Seconds()) {
			return true, "grace"
		}
		if m.cfg.RejectNewRequestAfterExpiry {
			return false, "lease_expired"
		}
	}
	if feature != "" && len(lease.Features) > 0 && !contains(lease.Features, feature) {
		return false, "feature_not_enabled"
	}
	return true, "active"
}

func (m *Manager) leaseFeatures() []string {
	if m.lease != nil {
		return m.lease.NormalizedLease().Features
	}
	return nil
}

func standardExpiryTime(result StandardResult, claim *Claim) time.Time {
	if strings.TrimSpace(result.ExpiresAt) != "" {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(result.ExpiresAt)); err == nil {
			return t
		}
	}
	if claim != nil {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(claim.ExpiresAt)); err == nil {
			return t
		}
	}
	return time.Time{}
}

func standardExpiryUnix(result StandardResult, claim *Claim) int64 {
	if t := standardExpiryTime(result, claim); !t.IsZero() {
		return t.Unix()
	}
	return 0
}

func verifyExecutableHash(expected string) bool {
	f, err := os.Open(os.Args[0])
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return false
	}
	actual := hex.EncodeToString(h.Sum(nil))
	return strings.EqualFold(strings.TrimSpace(expected), actual)
}

// BuildInfo is included in diagnostics without exposing secrets.
type BuildInfo struct {
	GoVersion string `json:"go_version"`
	Arch      string `json:"arch"`
}

func (m *Manager) BuildInfo() BuildInfo {
	return BuildInfo{GoVersion: runtime.Version(), Arch: runtime.GOOS + "/" + runtime.GOARCH}
}

// PublicErrorCode maps internal provider details to a stable, non-sensitive
// code suitable for the admin API.
func PublicErrorCode(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case isProviderError(err, "purchase_required"):
		return "purchase_required"
	case isProviderError(err, "revoked"):
		return "revoked"
	case isProviderError(err, "authorization_expired"):
		return "authorization_expired"
	case isProviderError(err, "authorization_invalid"):
		return "authorization_invalid"
	case isProviderError(err, "expired"):
		return "expired"
	case isProviderError(err, "provider_unavailable"):
		return "provider_unavailable"
	case isProviderError(err, "provider_rejected"):
		return "provider_rejected"
	case errors.Is(err, ErrNotActivated):
		return "not_activated"
	case errors.Is(err, ErrExpired):
		return "expired"
	case errors.Is(err, ErrFeature):
		return "feature_not_enabled"
	default:
		return "provider_error"
	}
}
