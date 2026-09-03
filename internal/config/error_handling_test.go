package config

import "testing"

func TestDefaultErrorHandlingConfig(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("{}"))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	want := DefaultErrorHandlingConfig()
	if cfg.ErrorHandling != want {
		t.Fatalf("ErrorHandling = %#v, want %#v", cfg.ErrorHandling, want)
	}
}

func TestErrorHandlingConfigUsesCodeDefaults(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
error-handling:
  temporary-error-strategy: immediate-switch
  temporary-error-max-wait-seconds: 3
  retry-before-first-output-only: true
  transient-errors-keep-account-active: true
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if cfg.ErrorHandling != DefaultErrorHandlingConfig() {
		t.Fatalf("ErrorHandling = %#v, want code defaults", cfg.ErrorHandling)
	}
	if cfg.ErrorHandling.TemporaryErrorStrategy != TemporaryErrorWaitThenSwitch {
		t.Fatalf("strategy = %q", cfg.ErrorHandling.TemporaryErrorStrategy)
	}
}

func TestErrorHandlingConfigIgnoresLegacyOverrides(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
error-handling:
  retry-before-first-output-only: false
  transient-errors-keep-account-active: false
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if cfg.ErrorHandling != DefaultErrorHandlingConfig() {
		t.Fatalf("ErrorHandling = %#v, want code defaults", cfg.ErrorHandling)
	}
}

func TestErrorHandlingConfigIgnoresUnknownLegacyStrategy(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`error-handling:
  temporary-error-strategy: unknown
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if cfg.ErrorHandling != DefaultErrorHandlingConfig() {
		t.Fatalf("ErrorHandling = %#v, want code defaults", cfg.ErrorHandling)
	}
}
