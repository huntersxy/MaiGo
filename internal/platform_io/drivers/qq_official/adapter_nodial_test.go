package qq_official

import (
	"context"
	"testing"
)

func TestEnabledFalseDoesNotDial(t *testing.T) {
	settings := DefaultSettings()
	if settings.Plugin.Enabled {
		t.Fatal("plugin.enabled default must be false")
	}
	if settings.Plugin.ConfigVersion != SupportedConfigVersion {
		t.Fatalf("config_version=%s want %s", settings.Plugin.ConfigVersion, SupportedConfigVersion)
	}
	if _, ok := DefaultConfigMap()["filter"]; !ok {
		t.Fatal("default config must use section name filter")
	}
	if _, ok := DefaultConfigMap()["filters"]; ok {
		t.Fatal("default config must not use filters")
	}
	adapter := NewAdapter(settings, nil)
	if err := adapter.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if adapter.Dialed() {
		t.Fatal("enabled=false must not dial")
	}
}

func TestMissingCredentialsDoesNotDial(t *testing.T) {
	settings := DefaultSettings()
	settings.Plugin.Enabled = true
	adapter := NewAdapter(settings, nil)
	if err := adapter.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if adapter.Dialed() {
		t.Fatal("missing credentials must not dial")
	}
}
