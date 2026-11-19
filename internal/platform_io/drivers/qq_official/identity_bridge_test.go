package qq_official

import (
	"context"
	"testing"
)

func TestIdentityBridgeSkipsWithoutInvoker(t *testing.T) {
	b := NewIdentityBridge(nil)
	_, err := b.callLease(context.Background(), "123", "owner", true)
	if err == nil || err.Error() != "snowluma LocalAPI 未就绪" {
		t.Fatalf("callLease without invoker: %v", err)
	}
	_, err = b.callObservations(context.Background(), "123", nil)
	if err == nil || err.Error() != "snowluma LocalAPI 未就绪" {
		t.Fatalf("callObservations without invoker: %v", err)
	}
}

func TestIdentityBridgeRenewRequiresDecimalUnified(t *testing.T) {
	b := NewIdentityBridge(nil)
	settings := DefaultSettings()
	settings.Plugin.Enabled = true
	settings.QQOfficial.SnowlumaIdentityOnly = true
	settings.QQOfficial.UnifiedAccountID = "not-decimal"
	if err := b.Renew(context.Background(), settings, true, true); err != nil {
		t.Fatalf("non-decimal unified should release/skip without error, got %v", err)
	}
}
