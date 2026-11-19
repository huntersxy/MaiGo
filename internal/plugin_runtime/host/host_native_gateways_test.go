package host

import (
	"context"
	"testing"

	"MaiGo/internal/platform_io/drivers/qq_official"
	"MaiGo/internal/platform_io/drivers/snowluma"
	"MaiGo/internal/plugin_runtime/protocol"
)

func TestEnsureNativeMessageGatewaysSeedsQQAndSnowluma(t *testing.T) {
	h := NewHost()
	h.EnsureNativeMessageGateways()
	if entry := h.components.MessageGateway(snowluma.PluginID, snowluma.GatewayName, false, ""); entry == nil {
		t.Fatal("snowluma gateway missing")
	}
	if entry := h.components.MessageGateway(qq_official.PluginID, qq_official.GatewayName, false, ""); entry == nil {
		t.Fatal("qq official gateway missing")
	}
}

func TestApplyNativeMessageGatewayStateDoesNotRequirePluginDriver(t *testing.T) {
	h := NewHost()
	h.EnsureNativeMessageGateways()
	state, info, err := h.applyNativeMessageGatewayState(context.Background(), snowluma.PluginID, protocol.MessageGatewayStateUpdatePayload{
		GatewayName: snowluma.GatewayName,
		Ready:       true,
		Platform:    snowluma.PlatformQQ,
		AccountID:   "123456",
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !state.Ready || state.AccountID != "123456" {
		t.Fatalf("state=%+v", state)
	}
	if info["account_id"] != "123456" {
		t.Fatalf("info=%v", info)
	}
	stored, ok := h.MessageGatewayState(snowluma.PluginID, snowluma.GatewayName)
	if !ok || !stored.Ready || stored.AccountID != "123456" {
		t.Fatalf("stored=%+v ok=%v", stored, ok)
	}
}
