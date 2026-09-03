package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseConfigBytesIgnoresRemovedPrefixHeatShadow(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`codex:
  cache-affinity:
    enabled: true
    prefix-heat-enabled: true
    prefix-heat-shadow: true
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if !cfg.Codex.CacheAffinity.PrefixHeatEnabled {
		t.Fatal("prefix heat enabled setting was not preserved")
	}
	rendered, errMarshal := yaml.Marshal(cfg.Codex.CacheAffinity)
	if errMarshal != nil {
		t.Fatalf("marshal cache affinity: %v", errMarshal)
	}
	if string(rendered) == "" {
		t.Fatal("marshaled cache affinity is empty")
	}
	var fields map[string]any
	if errUnmarshal := yaml.Unmarshal(rendered, &fields); errUnmarshal != nil {
		t.Fatalf("unmarshal rendered cache affinity: %v", errUnmarshal)
	}
	if _, exists := fields["prefix-heat-shadow"]; exists {
		t.Fatal("removed prefix heat shadow field was emitted")
	}
}
