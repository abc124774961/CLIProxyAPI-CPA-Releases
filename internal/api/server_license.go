package api

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/licensing"
	log "github.com/sirupsen/logrus"
)

func (s *Server) configureLicense(cfg *config.Config) {
	if s == nil || cfg == nil {
		return
	}
	options := licensing.Options{
		Provider:                 cfg.License.Provider,
		ProductCode:              cfg.License.ProductCode,
		APIBaseURL:               cfg.License.APIBaseURL,
		PublicKey:                cfg.License.PublicKey,
		PluginPublicKey:          cfg.License.PluginPublicKey,
		ClientID:                 cfg.License.ClientID,
		ClientSecret:             cfg.License.ClientSecret,
		StateDir:                 cfg.License.StateDir,
		ShopAuthURL:              cfg.License.ShopAuthURL,
		ShopExchangePath:         cfg.License.ShopExchangePath,
		ActivatePath:             cfg.License.ActivatePath,
		RefreshPath:              cfg.License.RefreshPath,
		VerifyPath:               cfg.License.VerifyPath,
		GracePath:                cfg.License.GracePath,
		RefreshInterval:          cfg.License.RefreshInterval,
		GracePeriod:              cfg.License.GracePeriod,
		InstanceBinding:          cfg.License.InstanceBinding,
		StorageKey:               cfg.License.StorageKey,
		ExpectedExecutableSHA256: cfg.License.ExpectedExecutableSHA256,
		ClaimPath:                cfg.License.ClaimPath,
	}
	runtimeConfig, configErr := licensing.ConfigFromOptions(options)
	manager, initErr := licensing.InitializeWithConfig(runtimeConfig)
	previous := s.licenseManager.Swap(manager)
	if s.mgmt != nil {
		s.mgmt.SetLicenseManager(manager)
	}
	if previous != nil && previous != manager {
		previous.Stop()
	}
	if configErr != nil {
		log.WithError(configErr).Error("license configuration is invalid")
		return
	}
	if initErr != nil {
		log.WithError(initErr).Error("license state initialization failed")
		return
	}
	manager.Start()
}

func (s *Server) licenseMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		manager := s.licenseManager.Load()
		if manager == nil {
			c.Header("Retry-After", "60")
			c.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{"error": gin.H{
				"type":    "license_required",
				"code":    "license_not_initialized",
				"message": "CPA license is inactive. Complete authorization in the management panel.",
			}})
			return
		}
		allowed, reason := manager.Check("core")
		if allowed {
			c.Next()
			return
		}
		c.Header("Retry-After", "60")
		c.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{"error": gin.H{
			"type":    "license_required",
			"code":    "license_" + reason,
			"message": "CPA license is inactive. Complete authorization in the management panel.",
		}})
	}
}

func (s *Server) getLicenseStatus(c *gin.Context) {
	manager := s.licenseManager.Load()
	if manager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "license service is not initialized"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, manager.Status())
}

func (s *Server) startLicenseShopAuthorization(c *gin.Context) {
	manager := s.licenseManager.Load()
	if manager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "license service is not initialized"})
		return
	}
	authorization, err := manager.StartShopAuthorization(
		c.Query("callback_url"),
		c.Query("origin"),
		managementActor(c),
	)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": licensing.PublicErrorCode(err)})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, authorization)
}

func (s *Server) exchangeLicenseShopCode(c *gin.Context) {
	manager := s.licenseManager.Load()
	if manager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "license service is not initialized"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	var input struct {
		State string `json:"state"`
		Code  string `json:"code"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	if err := manager.ExchangeShopCode(c.Request.Context(), input.State, input.Code, managementActor(c)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": licensing.PublicErrorCode(err)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "license": manager.Status()})
}

func (s *Server) refreshLicense(c *gin.Context) {
	manager := s.licenseManager.Load()
	if manager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "license service is not initialized"})
		return
	}
	if err := manager.Refresh(c.Request.Context()); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": licensing.PublicErrorCode(err)})
		return
	}
	if s.pluginHost != nil {
		s.pluginHost.SetLicenseGate(manager)
		s.refreshPluginManagementRoutes()
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "license": manager.Status()})
}

func (s *Server) activateLicense(c *gin.Context) {
	manager := s.licenseManager.Load()
	if manager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "license service is not initialized"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	var input struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || strings.TrimSpace(input.Code) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	if err := manager.Activate(c.Request.Context(), input.Code); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": licensing.PublicErrorCode(err)})
		return
	}
	if s.pluginHost != nil {
		s.pluginHost.SetLicenseGate(manager)
		s.refreshPluginManagementRoutes()
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "license": manager.Status()})
}

func (s *Server) licenseShopCallback(c *gin.Context) {
	manager := s.licenseManager.Load()
	if manager == nil {
		c.String(http.StatusServiceUnavailable, "license service is not initialized")
		return
	}
	state := c.Query("state")
	callbackError := c.Query("error")
	if callbackError == "" {
		callbackError = c.Query("error_description")
	}
	html, err := manager.ShopCallbackHTML(state, c.Query("code"), callbackError)
	if err != nil {
		c.String(http.StatusBadRequest, "authorization response expired")
		return
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
}

func managementActor(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	credential := strings.TrimSpace(c.GetHeader("Authorization"))
	if credential == "" {
		credential = strings.TrimSpace(c.GetHeader("X-Management-Key"))
	}
	if credential == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(credential))
	return hex.EncodeToString(digest[:16])
}
