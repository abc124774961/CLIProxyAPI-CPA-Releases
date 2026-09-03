package licensing

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultProvider       = "shop666"
	defaultProductCode    = "CPA"
	defaultShopAPIBaseURL = "https://p.666ttt.net/api/storefront"
)

type Config struct {
	Provider                    string
	ProductCode                 string
	APIBaseURL                  string
	PublicKey                   []byte
	PluginPublicKey             []byte
	ClientID                    string
	ClientSecret                string
	StateDir                    string
	RefreshInterval             time.Duration
	GracePeriod                 time.Duration
	InstanceBinding             string
	FailOpenDuringGrace         bool
	RejectNewRequestAfterExpiry bool
	ExpectedExecutableSHA256    string
	ClaimPath                   string
	ActivatePath                string
	RefreshPath                 string
	VerifyPath                  string
	GracePath                   string
	ShopAuthURL                 string
	ShopExchangePath            string
	StorageKey                  string
}

// Options is the serializable license section used by config.yaml.
type Options struct {
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

func ConfigFromOptions(options Options) (Config, error) {
	cfg := Config{
		Provider:                    strings.TrimSpace(options.Provider),
		ProductCode:                 strings.TrimSpace(options.ProductCode),
		APIBaseURL:                  strings.TrimRight(strings.TrimSpace(options.APIBaseURL), "/"),
		ClientID:                    strings.TrimSpace(options.ClientID),
		ClientSecret:                strings.TrimSpace(options.ClientSecret),
		StateDir:                    strings.TrimSpace(options.StateDir),
		ShopAuthURL:                 strings.TrimSpace(options.ShopAuthURL),
		ShopExchangePath:            cleanPath(options.ShopExchangePath, "/licenses/exchange"),
		ActivatePath:                cleanPath(options.ActivatePath, "/licenses/activate"),
		RefreshPath:                 cleanPath(options.RefreshPath, "/licenses/refresh"),
		VerifyPath:                  cleanPath(options.VerifyPath, "/licenses/verify"),
		GracePath:                   cleanPath(options.GracePath, "/licenses/grace"),
		RefreshInterval:             parseDuration(options.RefreshInterval, 10*time.Minute, time.Minute),
		GracePeriod:                 parseDuration(options.GracePeriod, 6*time.Hour, 0),
		InstanceBinding:             strings.ToLower(strings.TrimSpace(options.InstanceBinding)),
		FailOpenDuringGrace:         true,
		RejectNewRequestAfterExpiry: true,
		StorageKey:                  strings.TrimSpace(options.StorageKey),
		ExpectedExecutableSHA256:    strings.TrimSpace(options.ExpectedExecutableSHA256),
		ClaimPath:                   strings.TrimSpace(options.ClaimPath),
	}
	if cfg.Provider == "" {
		cfg.Provider = defaultProvider
	}
	if cfg.ProductCode == "" {
		cfg.ProductCode = defaultProductCode
	}
	if cfg.APIBaseURL == "" {
		cfg.APIBaseURL = defaultShopAPIBaseURL
	}
	if cfg.StateDir == "" {
		cfg.StateDir = filepath.Join("data", "license")
	}
	if cfg.ShopAuthURL == "" {
		cfg.ShopAuthURL = "https://p.666ttt.net/shop/?authorize=cpa"
	}
	if cfg.InstanceBinding != "portable" {
		cfg.InstanceBinding = "strict"
	}
	applyEnvironmentOverrides(&cfg)
	publicKey := strings.TrimSpace(options.PublicKey)
	if value := strings.TrimSpace(os.Getenv("CPA_LICENSE_PUBLIC_KEY")); value != "" {
		publicKey = value
	}
	if publicKey != "" {
		key, err := decodePublicKey(publicKey)
		if err != nil {
			return cfg, fmt.Errorf("decode license public key: %w", err)
		}
		cfg.PublicKey = key
	} else {
		return cfg, fmt.Errorf("license public key is required")
	}
	pluginPublicKey := strings.TrimSpace(options.PluginPublicKey)
	if value := strings.TrimSpace(os.Getenv("CPA_LICENSE_PLUGIN_PUBLIC_KEY")); value != "" {
		pluginPublicKey = value
	}
	if pluginPublicKey == "" {
		// Older deployments used one publisher key for both leases and plugin
		// manifests. Keep that format working until a dedicated key is set.
		cfg.PluginPublicKey = append([]byte(nil), cfg.PublicKey...)
	} else if key, err := decodePublicKey(pluginPublicKey); err != nil {
		return cfg, fmt.Errorf("decode plugin public key: %w", err)
	} else {
		cfg.PluginPublicKey = key
	}
	return cfg, nil
}

func LoadConfig() (Config, error) {
	return ConfigFromOptions(Options{})
}

func applyEnvironmentOverrides(cfg *Config) {
	if cfg == nil {
		return
	}
	setString := func(name string, target *string) {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			*target = value
		}
	}
	setString("CPA_LICENSE_PROVIDER", &cfg.Provider)
	setString("CPA_LICENSE_PRODUCT_CODE", &cfg.ProductCode)
	setString("CPA_LICENSE_API_BASE_URL", &cfg.APIBaseURL)
	setString("CPA_LICENSE_CLIENT_ID", &cfg.ClientID)
	setString("CPA_LICENSE_CLIENT_SECRET", &cfg.ClientSecret)
	setString("CPA_LICENSE_STATE_DIR", &cfg.StateDir)
	setString("CPA_LICENSE_SHOP_AUTH_URL", &cfg.ShopAuthURL)
	setString("CPA_LICENSE_SHOP_EXCHANGE_PATH", &cfg.ShopExchangePath)
	setString("CPA_LICENSE_ACTIVATE_PATH", &cfg.ActivatePath)
	setString("CPA_LICENSE_REFRESH_PATH", &cfg.RefreshPath)
	setString("CPA_LICENSE_VERIFY_PATH", &cfg.VerifyPath)
	setString("CPA_LICENSE_GRACE_PATH", &cfg.GracePath)
	setString("CPA_LICENSE_STORAGE_KEY", &cfg.StorageKey)
	setString("CPA_LICENSE_EXECUTABLE_SHA256", &cfg.ExpectedExecutableSHA256)
	setString("CPA_LICENSE_CLAIM_PATH", &cfg.ClaimPath)
	if file := strings.TrimSpace(os.Getenv("CPA_LICENSE_CLIENT_SECRET_FILE")); file != "" {
		if value, err := os.ReadFile(file); err == nil {
			// An empty placeholder secret file must not erase a direct secret
			// supplied through CPA_LICENSE_CLIENT_SECRET. This is useful for
			// Compose deployments that always mount a Docker secret while the
			// storefront client guard remains disabled.
			if secret := strings.TrimSpace(string(value)); secret != "" {
				cfg.ClientSecret = secret
			}
		}
	}
	if value := strings.TrimSpace(os.Getenv("CPA_LICENSE_REFRESH_INTERVAL")); value != "" {
		cfg.RefreshInterval = parseDuration(value, cfg.RefreshInterval, time.Minute)
	}
	if value := strings.TrimSpace(os.Getenv("CPA_LICENSE_GRACE_PERIOD")); value != "" {
		cfg.GracePeriod = parseDuration(value, cfg.GracePeriod, 0)
	}
}

func parseDuration(value string, fallback, minimum time.Duration) time.Duration {
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || parsed < minimum {
		return fallback
	}
	return parsed
}
func cleanPath(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if !strings.HasPrefix(value, "/") {
		return "/" + value
	}
	return value
}
func decodePublicKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	for _, encoding := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if decoded, err := encoding.DecodeString(value); err == nil && len(decoded) == ed25519.PublicKeySize {
			return decoded, nil
		}
	}
	if decoded, err := hex.DecodeString(strings.TrimPrefix(value, "0x")); err == nil && len(decoded) == ed25519.PublicKeySize {
		return decoded, nil
	}
	return nil, fmt.Errorf("expected a 32-byte base64url, base64, or hex encoded Ed25519 public key")
}
