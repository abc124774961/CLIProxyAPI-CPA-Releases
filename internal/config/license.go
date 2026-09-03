package config

import "strings"

// LicenseConfig controls the CPA storefront license integration.
// Secret fields are intentionally excluded from management JSON responses.
type LicenseConfig struct {
	Provider                 string `yaml:"provider" json:"provider"`
	ProductCode              string `yaml:"product-code" json:"product-code"`
	APIBaseURL               string `yaml:"api-base-url" json:"api-base-url"`
	PublicKey                string `yaml:"public-key" json:"-"`
	PluginPublicKey          string `yaml:"plugin-public-key" json:"-"`
	ClientID                 string `yaml:"client-id" json:"-"`
	ClientSecret             string `yaml:"client-secret" json:"-"`
	StateDir                 string `yaml:"state-dir" json:"state-dir"`
	ShopAuthURL              string `yaml:"shop-auth-url" json:"shop-auth-url"`
	ShopExchangePath         string `yaml:"shop-exchange-path" json:"shop-exchange-path"`
	ActivatePath             string `yaml:"activate-path" json:"-"`
	RefreshPath              string `yaml:"refresh-path" json:"-"`
	VerifyPath               string `yaml:"verify-path" json:"-"`
	GracePath                string `yaml:"grace-path" json:"-"`
	RefreshInterval          string `yaml:"refresh-interval" json:"refresh-interval"`
	GracePeriod              string `yaml:"grace-period" json:"grace-period"`
	InstanceBinding          string `yaml:"instance-binding" json:"instance-binding"`
	StorageKey               string `yaml:"storage-key" json:"-"`
	ExpectedExecutableSHA256 string `yaml:"executable-sha256" json:"-"`
	ClaimPath                string `yaml:"claim-path" json:"-"`
}

func DefaultLicenseConfig() LicenseConfig {
	return LicenseConfig{
		Provider:         "shop666",
		ProductCode:      "CPA",
		APIBaseURL:       "https://p.666ttt.net/api/storefront",
		StateDir:         "data/license",
		ShopAuthURL:      "https://p.666ttt.net/shop/?authorize=cpa",
		ShopExchangePath: "/licenses/exchange",
		ActivatePath:     "/licenses/activate",
		RefreshPath:      "/licenses/refresh",
		VerifyPath:       "/licenses/verify",
		GracePath:        "/licenses/grace",
		RefreshInterval:  "10m",
		GracePeriod:      "6h",
		InstanceBinding:  "strict",
	}
}

func (c *LicenseConfig) Normalize() {
	if c == nil {
		return
	}
	defaults := DefaultLicenseConfig()
	if strings.TrimSpace(c.Provider) == "" {
		c.Provider = defaults.Provider
	}
	if strings.TrimSpace(c.ProductCode) == "" {
		c.ProductCode = defaults.ProductCode
	}
	if strings.TrimSpace(c.APIBaseURL) == "" {
		c.APIBaseURL = defaults.APIBaseURL
	}
	if strings.TrimSpace(c.StateDir) == "" {
		c.StateDir = defaults.StateDir
	}
	if strings.TrimSpace(c.ShopAuthURL) == "" {
		c.ShopAuthURL = defaults.ShopAuthURL
	}
	if strings.TrimSpace(c.ShopExchangePath) == "" {
		c.ShopExchangePath = defaults.ShopExchangePath
	}
	if strings.TrimSpace(c.ActivatePath) == "" {
		c.ActivatePath = defaults.ActivatePath
	}
	if strings.TrimSpace(c.RefreshPath) == "" {
		c.RefreshPath = defaults.RefreshPath
	}
	if strings.TrimSpace(c.VerifyPath) == "" {
		c.VerifyPath = defaults.VerifyPath
	}
	if strings.TrimSpace(c.GracePath) == "" {
		c.GracePath = defaults.GracePath
	}
	if strings.TrimSpace(c.RefreshInterval) == "" {
		c.RefreshInterval = defaults.RefreshInterval
	}
	if strings.TrimSpace(c.GracePeriod) == "" {
		c.GracePeriod = defaults.GracePeriod
	}
	if !strings.EqualFold(strings.TrimSpace(c.InstanceBinding), "portable") {
		c.InstanceBinding = defaults.InstanceBinding
	}
}
