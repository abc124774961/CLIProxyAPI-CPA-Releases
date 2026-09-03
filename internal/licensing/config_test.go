package licensing

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfigDefaultsAndForcedEnablement(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"CPA_LICENSE_PUBLIC_KEY", "CPA_LICENSE_API_BASE_URL", "CPA_LICENSE_STATE_DIR"} {
		t.Setenv(key, "")
	}
	t.Setenv("CPA_LICENSE_PUBLIC_KEY", base64.RawURLEncoding.EncodeToString(pub))
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProductCode != "CPA" || cfg.Provider != "shop666" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadConfigRequiresPublicKey(t *testing.T) {
	t.Setenv("CPA_LICENSE_PUBLIC_KEY", "")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected public key validation")
	}
}

func TestLoadConfigRejectsInvalidPublicKeyLength(t *testing.T) {
	t.Setenv("CPA_LICENSE_PUBLIC_KEY", base64.RawURLEncoding.EncodeToString([]byte("too-short")))
	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected invalid public key length to be rejected")
	}
}

func TestLoadConfigAcceptsHexPublicKey(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPA_LICENSE_PUBLIC_KEY", hex.EncodeToString(pub))
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if string(cfg.PublicKey) != string(pub) {
		t.Fatalf("decoded public key mismatch")
	}
}

func TestClientSecretFileDoesNotEraseDirectSecretWhenEmpty(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "client-secret")
	if err := os.WriteFile(secretFile, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPA_LICENSE_PUBLIC_KEY", base64.RawURLEncoding.EncodeToString(pub))
	t.Setenv("CPA_LICENSE_CLIENT_SECRET", "direct-secret")
	t.Setenv("CPA_LICENSE_CLIENT_SECRET_FILE", secretFile)
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientSecret != "direct-secret" {
		t.Fatalf("empty secret file erased direct secret: %q", cfg.ClientSecret)
	}
}

func TestLoadConfigCapsCustomerGracePeriod(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPA_LICENSE_PUBLIC_KEY", base64.RawURLEncoding.EncodeToString(pub))
	t.Setenv("CPA_LICENSE_GRACE_PERIOD", "48h")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GracePeriod != maxLocalGracePeriod {
		t.Fatalf("grace period was not capped: got %s want %s", cfg.GracePeriod, maxLocalGracePeriod)
	}
}

func TestConfigFromOptionsCapsYamlGracePeriod(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPA_LICENSE_PUBLIC_KEY", base64.RawURLEncoding.EncodeToString(pub))
	t.Setenv("CPA_LICENSE_GRACE_PERIOD", "")

	cfg, err := ConfigFromOptions(Options{
		PublicKey:   base64.RawURLEncoding.EncodeToString(pub),
		GracePeriod: "12h",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GracePeriod != maxLocalGracePeriod {
		t.Fatalf("YAML grace period was not capped: got %s want %s", cfg.GracePeriod, maxLocalGracePeriod)
	}

	cfg, err = ConfigFromOptions(Options{PublicKey: base64.RawURLEncoding.EncodeToString(pub), GracePeriod: "90m"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GracePeriod != 90*time.Minute {
		t.Fatalf("valid grace period changed unexpectedly: got %s", cfg.GracePeriod)
	}
}
