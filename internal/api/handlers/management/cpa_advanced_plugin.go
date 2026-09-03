package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/licensing"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginpkg"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginstore"
)

const cpaAdvancedPluginID = "cpa-advanced-core"

type cpaAdvancedPluginRequest struct {
	Version string `json:"version"`
	GOOS    string `json:"goos"`
	GOARCH  string `json:"goarch"`
}

type cpaAdvancedPluginVersion struct {
	Version   string `json:"version"`
	Installed bool   `json:"installed"`
	Active    bool   `json:"active"`
	Selected  bool   `json:"selected"`
	Path      string `json:"path,omitempty"`
}

type cpaAdvancedPluginStatus struct {
	PluginID            string                     `json:"plugin_id"`
	Authorized          bool                       `json:"authorized"`
	AuthorizationReason string                     `json:"authorization_reason,omitempty"`
	License             licensing.PublicStatus     `json:"license"`
	PluginsEnabled      bool                       `json:"plugins_enabled"`
	Configured          bool                       `json:"configured"`
	Enabled             bool                       `json:"enabled"`
	Installed           bool                       `json:"installed"`
	Registered          bool                       `json:"registered"`
	EffectiveEnabled    bool                       `json:"effective_enabled"`
	SelectedVersion     string                     `json:"selected_version,omitempty"`
	ActiveVersion       string                     `json:"active_version,omitempty"`
	Versions            []cpaAdvancedPluginVersion `json:"versions"`
	UpdatedAt           time.Time                  `json:"updated_at,omitempty"`
}

type cpaAdvancedPluginVersionResponse struct {
	PluginID            string                     `json:"plugin_id"`
	Authorized          bool                       `json:"authorized"`
	AuthorizationReason string                     `json:"authorization_reason,omitempty"`
	SelectedVersion     string                     `json:"selected_version,omitempty"`
	ActiveVersion       string                     `json:"active_version,omitempty"`
	Versions            []cpaAdvancedPluginVersion `json:"versions"`
}

func (h *Handler) GetCPAAdvancedPluginStatus(c *gin.Context) {
	status := h.cpaAdvancedPluginStatus()
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, status)
}

func (h *Handler) ListCPAAdvancedPluginVersions(c *gin.Context) {
	status := h.cpaAdvancedPluginStatus()
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, cpaAdvancedPluginVersionResponse{
		PluginID: cpaAdvancedPluginID, Authorized: status.Authorized,
		AuthorizationReason: status.AuthorizationReason,
		SelectedVersion:     status.SelectedVersion, ActiveVersion: status.ActiveVersion,
		Versions: status.Versions,
	})
}

func (h *Handler) DownloadCPAAdvancedPlugin(c *gin.Context) {
	request, errRequest := decodeCPAAdvancedPluginRequest(c)
	if errRequest != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "message": errRequest.Error()})
		return
	}
	manager, ok := h.requireCPAAdvancedAuthorization(c)
	if !ok {
		return
	}
	opened, packageData, errDownload := h.downloadCPAAdvancedPackage(c.Request.Context(), manager, request)
	if errDownload != nil {
		h.writeCPAAdvancedError(c, errDownload)
		return
	}
	if errCache := h.cacheCPAAdvancedPackage(packageData, opened.Manifest.Version, request.GOOS, request.GOARCH); errCache != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "plugin_cache_failed", "message": errCache.Error()})
		return
	}
	c.JSON(http.StatusOK, cpaAdvancedPackageResponse(opened.Manifest, "downloaded"))
}

func (h *Handler) InstallCPAAdvancedPlugin(c *gin.Context) {
	h.installCPAAdvancedPlugin(c, false)
}

func (h *Handler) UpgradeCPAAdvancedPlugin(c *gin.Context) {
	h.installCPAAdvancedPlugin(c, true)
}

func (h *Handler) installCPAAdvancedPlugin(c *gin.Context, upgrade bool) {
	request, errRequest := decodeCPAAdvancedPluginRequest(c)
	if errRequest != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "message": errRequest.Error()})
		return
	}
	manager, ok := h.requireCPAAdvancedAuthorization(c)
	if !ok {
		return
	}
	opened, packageData, errDownload := h.downloadCPAAdvancedPackage(c.Request.Context(), manager, request)
	if errDownload != nil {
		h.writeCPAAdvancedError(c, errDownload)
		return
	}
	if errCache := h.cacheCPAAdvancedPackage(packageData, opened.Manifest.Version, request.GOOS, request.GOARCH); errCache != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "plugin_cache_failed", "message": errCache.Error()})
		return
	}
	result, errInstall := h.installOpenedCPAAdvancedPlugin(opened, request.GOOS, request.GOARCH)
	if errInstall != nil {
		if errors.Is(errInstall, pluginstore.ErrLoadedPluginLocked) {
			c.JSON(http.StatusConflict, gin.H{"error": "plugin_loaded", "message": "the current plugin is still serving requests; retry after it drains"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "plugin_install_failed", "message": errInstall.Error()})
		return
	}
	if errConfig := h.selectCPAAdvancedPluginVersion(c, opened.Manifest.Version); errConfig != nil {
		return
	}
	status := h.cpaAdvancedPluginStatus()
	response := cpaAdvancedPackageResponse(opened.Manifest, "installed")
	response["upgrade"] = upgrade
	response["path"] = result.Path
	response["skipped"] = result.Skipped
	response["status"] = status
	c.JSON(http.StatusOK, response)
}

func (h *Handler) RollbackCPAAdvancedPlugin(c *gin.Context) {
	request, errRequest := decodeCPAAdvancedPluginRequest(c)
	if errRequest != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "message": errRequest.Error()})
		return
	}
	if _, ok := h.requireCPAAdvancedAuthorization(c); !ok {
		return
	}
	version := strings.TrimSpace(request.Version)
	status := h.cpaAdvancedPluginStatus()
	if version == "" {
		version = previousCPAAdvancedVersion(status.Versions, status.SelectedVersion)
	}
	if version == "" {
		c.JSON(http.StatusConflict, gin.H{"error": "plugin_version_unavailable", "message": "no older installed plugin version is available"})
		return
	}
	var selected *cpaAdvancedPluginVersion
	for index := range status.Versions {
		if status.Versions[index].Version == version {
			selected = &status.Versions[index]
			break
		}
	}
	if selected == nil || !selected.Installed {
		c.JSON(http.StatusNotFound, gin.H{"error": "plugin_version_not_found", "message": "requested plugin version is not installed"})
		return
	}
	if errConfig := h.selectCPAAdvancedPluginVersion(c, version); errConfig != nil {
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "rolled_back", "plugin_id": cpaAdvancedPluginID, "version": version, "plugin": h.cpaAdvancedPluginStatus()})
}

func decodeCPAAdvancedPluginRequest(c *gin.Context) (cpaAdvancedPluginRequest, error) {
	request := cpaAdvancedPluginRequest{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return request, nil
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	body, errRead := io.ReadAll(c.Request.Body)
	if errRead != nil {
		return request, errRead
	}
	if strings.TrimSpace(string(body)) != "" {
		if errDecode := json.Unmarshal(body, &request); errDecode != nil {
			return request, errDecode
		}
	}
	request.Version = strings.TrimSpace(request.Version)
	request.GOOS = normalizeCPAPluginPlatform(request.GOOS, runtime.GOOS)
	request.GOARCH = normalizeCPAPluginPlatform(request.GOARCH, runtime.GOARCH)
	if request.GOOS == "" || request.GOARCH == "" {
		return request, fmt.Errorf("plugin platform is required")
	}
	return request, nil
}

func normalizeCPAPluginPlatform(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		value = strings.ToLower(strings.TrimSpace(fallback))
	}
	switch value {
	case "mac", "macos", "osx":
		return "darwin"
	case "x86_64":
		return "amd64"
	case "aarch64":
		return "arm64"
	default:
		return value
	}
}

func (h *Handler) requireCPAAdvancedAuthorization(c *gin.Context) (*licensing.Manager, bool) {
	h.mu.Lock()
	manager := h.licenseManager
	h.mu.Unlock()
	if manager == nil {
		c.JSON(http.StatusPaymentRequired, gin.H{"error": "license_required", "code": "license_not_initialized"})
		return nil, false
	}
	if allowed, reason := manager.CheckStrict("advanced_core"); !allowed {
		c.JSON(http.StatusPaymentRequired, gin.H{"error": "license_required", "code": "license_" + reason})
		return nil, false
	}
	return manager, true
}

func (h *Handler) downloadCPAAdvancedPackage(ctx context.Context, manager *licensing.Manager, request cpaAdvancedPluginRequest) (pluginpkg.Opened, []byte, error) {
	data, errDownload := manager.DownloadPluginPackage(ctx, cpaAdvancedPluginID, request.Version, request.GOOS, request.GOARCH)
	if errDownload != nil {
		return pluginpkg.Opened{}, nil, errDownload
	}
	opened, errOpen := manager.OpenPluginPackage(data, cpaAdvancedPluginID, request.Version, request.GOOS, request.GOARCH)
	if errOpen != nil {
		return pluginpkg.Opened{}, nil, errOpen
	}
	return opened, data, nil
}

func (h *Handler) installOpenedCPAAdvancedPlugin(opened pluginpkg.Opened, goos, goarch string) (pluginstore.InstallResult, error) {
	h.mu.Lock()
	cfg := h.cfg
	host := h.pluginHost
	pluginsDir := "plugins"
	if cfg != nil {
		pluginsDir = cfg.Plugins.Dir
	}
	h.mu.Unlock()
	resolved, errResolve := config.ResolvePluginsDir(pluginsDir)
	if errResolve != nil {
		return pluginstore.InstallResult{}, errResolve
	}
	return pluginstore.InstallLibrary(opened.Payload, cpaAdvancedPluginID, opened.Manifest.Version, pluginstore.InstallOptions{
		PluginsDir:   resolved,
		GOOS:         goos,
		GOARCH:       goarch,
		RejectLoaded: true,
		PluginLoaded: func() bool { return pluginBusy(host, cpaAdvancedPluginID) },
		BeforeWrite:  func() error { return nil },
	})
}

func (h *Handler) selectCPAAdvancedPluginVersion(c *gin.Context, version string) error {
	version = strings.TrimSpace(version)
	if version == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "plugin_version_missing"})
		return errors.New("plugin version is empty")
	}
	h.mu.Lock()
	if h.cfg == nil {
		h.mu.Unlock()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config_unavailable"})
		return errors.New("config unavailable")
	}
	ensurePluginConfigMap(h.cfg)
	if !h.cfg.Plugins.Enabled {
		h.cfg.Plugins.Enabled = true
	}
	manifest := cpaAdvancedStoreManifest(version)
	if errEnable := h.enablePluginConfigLocked(cpaAdvancedPluginID, manifest); errEnable != nil {
		h.mu.Unlock()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "plugin_config_failed", "message": errEnable.Error()})
		return errEnable
	}
	snapshot, okSnapshot := h.saveConfigAndSnapshotLocked(c)
	h.mu.Unlock()
	if !okSnapshot {
		return errors.New("save config failed")
	}
	h.reloadConfigAfterManagementSaveAsync(c.Request.Context(), snapshot)
	return nil
}

func cpaAdvancedStoreManifest(version string) pluginstore.Manifest {
	return pluginstore.Manifest{
		SchemaVersion: 2,
		ID:            cpaAdvancedPluginID,
		Name:          "CPA 高级核心",
		Description:   "CPA 高级请求、WebSocket、指纹、缓存和调度核心",
		Author:        "CPA",
		Version:       strings.TrimSpace(version),
		SourceID:      "shop666",
		SourceName:    "666 商城",
		SourceURL:     "https://p.666ttt.net/api/storefront",
		Install:       pluginstore.InstallPlan{Type: pluginstore.InstallTypeDirect},
	}
}

func (h *Handler) cpaAdvancedPluginStatus() cpaAdvancedPluginStatus {
	status := cpaAdvancedPluginStatus{PluginID: cpaAdvancedPluginID, Versions: []cpaAdvancedPluginVersion{}}
	h.mu.Lock()
	manager := h.licenseManager
	cfg := h.cfg
	host := h.pluginHost
	pluginsDir := "plugins"
	if cfg != nil {
		status.PluginsEnabled = cfg.Plugins.Enabled
		pluginsDir = cfg.Plugins.Dir
		item, configured := cfg.Plugins.Configs[cpaAdvancedPluginID]
		status.Configured = configured
		status.Enabled = pluginInstanceEnabled(item)
		status.SelectedVersion = pluginStoreDesiredVersion(item)
	}
	h.mu.Unlock()
	if manager != nil {
		status.License = manager.Status()
		status.Authorized, status.AuthorizationReason = manager.CheckStrict("advanced_core")
	}
	if resolved, errResolve := config.ResolvePluginsDir(pluginsDir); errResolve == nil {
		if versions, errVersions := pluginstore.DiscoverInstalledPluginVersions(resolved, cpaAdvancedPluginID, runtime.GOOS, runtime.GOARCH); errVersions == nil {
			for _, item := range versions {
				status.Versions = append(status.Versions, cpaAdvancedPluginVersion{Version: item.Version, Installed: true, Path: item.Path, Selected: item.Version == status.SelectedVersion})
			}
		}
	}
	if host != nil {
		for _, item := range host.RegisteredPlugins() {
			if item.ID != cpaAdvancedPluginID {
				continue
			}
			status.Registered = true
			status.ActiveVersion = strings.TrimSpace(item.Metadata.Version)
			break
		}
	}
	status.Installed = len(status.Versions) > 0 || status.Registered
	status.EffectiveEnabled = status.PluginsEnabled && status.Enabled && status.Registered && status.Authorized
	status.UpdatedAt = time.Now()
	for index := range status.Versions {
		status.Versions[index].Active = status.Versions[index].Version == status.ActiveVersion
	}
	return status
}

func previousCPAAdvancedVersion(versions []cpaAdvancedPluginVersion, current string) string {
	current = strings.TrimSpace(current)
	if len(versions) == 0 {
		return ""
	}
	items := append([]cpaAdvancedPluginVersion(nil), versions...)
	sort.SliceStable(items, func(i, j int) bool { return compareCPAPluginVersions(items[i].Version, items[j].Version) > 0 })
	for _, item := range items {
		if item.Installed && item.Version != current {
			return item.Version
		}
	}
	return ""
}

func compareCPAPluginVersions(a, b string) int {
	partsA, partsB := strings.Split(strings.TrimPrefix(strings.TrimPrefix(a, "v"), "V"), "."), strings.Split(strings.TrimPrefix(strings.TrimPrefix(b, "v"), "V"), ".")
	length := len(partsA)
	if len(partsB) > length {
		length = len(partsB)
	}
	for index := 0; index < length; index++ {
		av, aok := cpaPluginVersionSegment(partsA, index)
		bv, bok := cpaPluginVersionSegment(partsB, index)
		if aok && bok && av != bv {
			if av > bv {
				return 1
			}
			return -1
		}
		if !aok || !bok {
			break
		}
	}
	if a == b {
		return 0
	}
	if a > b {
		return 1
	}
	return -1
}

func cpaPluginVersionSegment(parts []string, index int) (int64, bool) {
	if index >= len(parts) {
		return 0, true
	}
	var number int64
	if _, err := fmt.Sscanf(parts[index], "%d", &number); err != nil || number < 0 {
		return 0, false
	}
	return number, true
}

func (h *Handler) cacheCPAAdvancedPackage(data []byte, version, goos, goarch string) error {
	h.mu.Lock()
	manager := h.licenseManager
	h.mu.Unlock()
	if manager == nil || len(data) == 0 {
		return errors.New("license manager is unavailable")
	}
	stateDir := strings.TrimSpace(manager.Config().StateDir)
	if stateDir == "" {
		return errors.New("license state directory is empty")
	}
	path := filepath.Join(stateDir, "plugin-packages", cpaAdvancedPluginID, strings.TrimSpace(version), normalizeCPAPluginPlatform(goos, runtime.GOOS)+"-"+normalizeCPAPluginPlatform(goarch, runtime.GOARCH)+".pkg")
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		return errMkdir
	}
	tmp, errCreate := os.CreateTemp(filepath.Dir(path), ".package-*.tmp")
	if errCreate != nil {
		return errCreate
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if errChmod := tmp.Chmod(0o600); errChmod != nil {
		_ = tmp.Close()
		return errChmod
	}
	if _, errWrite := tmp.Write(data); errWrite != nil {
		_ = tmp.Close()
		return errWrite
	}
	if errSync := tmp.Sync(); errSync != nil {
		_ = tmp.Close()
		return errSync
	}
	if errClose := tmp.Close(); errClose != nil {
		return errClose
	}
	return os.Rename(tmpName, path)
}

func cpaAdvancedPackageResponse(manifest pluginpkg.Manifest, status string) gin.H {
	return gin.H{
		"status":            status,
		"plugin_id":         manifest.PluginID,
		"version":           manifest.Version,
		"goos":              manifest.GOOS,
		"goarch":            manifest.GOARCH,
		"required_features": append([]string(nil), manifest.RequiredFeatures...),
		"payload_size":      manifest.PayloadSize,
		"payload_sha256":    manifest.PayloadSHA256,
	}
}

func (h *Handler) writeCPAAdvancedError(c *gin.Context, err error) {
	code := licensing.PublicErrorCode(err)
	status := http.StatusBadGateway
	if code == "purchase_required" || code == "expired" || code == "revoked" || code == "authorization_invalid" || code == "authorization_expired" {
		status = http.StatusPaymentRequired
	}
	c.JSON(status, gin.H{"error": code, "message": err.Error()})
}
