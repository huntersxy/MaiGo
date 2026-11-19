package snowluma

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
	adapter := NewAdapter(settings, nil)
	if err := adapter.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if adapter.Dialed() {
		t.Fatal("enabled=false must not dial")
	}
}
