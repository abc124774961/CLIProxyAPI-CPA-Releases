package config

import "fmt"

// TemporaryErrorStrategy controls the handling of temporary upstream/network
// failures before the first response payload is emitted.
type TemporaryErrorStrategy string

const (
	TemporaryErrorWaitThenSwitch  TemporaryErrorStrategy = "wait-then-switch"
	TemporaryErrorImmediateSwitch TemporaryErrorStrategy = "immediate-switch"
	TemporaryErrorNoSwitch        TemporaryErrorStrategy = "no-switch"
)

// ErrorHandlingConfig describes the code-owned temporary failure policy.
//
// The fields remain available to embedders that construct Config values, but
// application YAML and the Manager visual editor no longer override them.
type ErrorHandlingConfig struct {
	TemporaryErrorStrategy           TemporaryErrorStrategy `yaml:"temporary-error-strategy" json:"temporary-error-strategy"`
	TemporaryErrorMaxWaitSeconds     int                    `yaml:"temporary-error-max-wait-seconds" json:"temporary-error-max-wait-seconds"`
	RetryBeforeFirstOutputOnly       bool                   `yaml:"retry-before-first-output-only" json:"retry-before-first-output-only"`
	TransientErrorsKeepAccountActive bool                   `yaml:"transient-errors-keep-account-active" json:"transient-errors-keep-account-active"`
}

// DefaultErrorHandlingConfig returns the low-latency-safe defaults.
func DefaultErrorHandlingConfig() ErrorHandlingConfig {
	return ErrorHandlingConfig{
		TemporaryErrorStrategy:           TemporaryErrorWaitThenSwitch,
		TemporaryErrorMaxWaitSeconds:     3,
		RetryBeforeFirstOutputOnly:       true,
		TransientErrorsKeepAccountActive: true,
	}
}

// Normalize fills omitted values and bounds the wait setting.
func (c ErrorHandlingConfig) Normalize() ErrorHandlingConfig {
	d := DefaultErrorHandlingConfig()
	if c.TemporaryErrorStrategy == "" {
		c.TemporaryErrorStrategy = d.TemporaryErrorStrategy
	}
	if c.TemporaryErrorMaxWaitSeconds < 0 {
		c.TemporaryErrorMaxWaitSeconds = 0
	}
	if c.TemporaryErrorMaxWaitSeconds > 3 {
		c.TemporaryErrorMaxWaitSeconds = 3
	}
	// Retrying after output has started can duplicate content and billing. The
	// request pipeline therefore always keeps this guard enabled, including
	// when an older or hand-edited config explicitly contains false.
	c.RetryBeforeFirstOutputOnly = true
	return c
}

// Validate checks externally configurable values.
func (c ErrorHandlingConfig) Validate() error {
	switch c.TemporaryErrorStrategy {
	case TemporaryErrorWaitThenSwitch, TemporaryErrorImmediateSwitch, TemporaryErrorNoSwitch:
	default:
		return fmt.Errorf("error-handling.temporary-error-strategy must be one of %q, %q, or %q", TemporaryErrorWaitThenSwitch, TemporaryErrorImmediateSwitch, TemporaryErrorNoSwitch)
	}
	if c.TemporaryErrorMaxWaitSeconds < 0 || c.TemporaryErrorMaxWaitSeconds > 3 {
		return fmt.Errorf("error-handling.temporary-error-max-wait-seconds must be between 0 and 3")
	}
	return nil
}
