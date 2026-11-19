package weixin_clawbot

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
	if got := len(AllGatewayNames()); got != MaxAccounts {
		t.Fatalf("gateway names=%d want %d", got, MaxAccounts)
	}
	if GatewayName(0) != GatewayBaseName {
		t.Fatalf("slot0 gateway=%s", GatewayName(0))
	}
	if GatewayName(1) != GatewayBaseName+"_1" {
		t.Fatalf("slot1 gateway=%s", GatewayName(1))
	}
	orch := NewOrchestrator(settings, nil)
	if err := orch.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if orch.Dialed() {
		t.Fatal("enabled=false must not dial")
	}
}
