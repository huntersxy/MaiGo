package host

import (
	"context"
	"testing"
)

func TestSyncPluginDependenciesKeepsAdapterMigrationErrors(t *testing.T) {
	runtime := NewHost()
	runtime.SetAdapterMigrationErrors(map[string]string{
		"Mai-with-u.qq-adapter": "适配器迁移失败，请修复后重启 Bot",
	})
	result, _ := runtime.SyncPluginDependencies(context.Background(), nil, nil, nil)
	if result.BlockedPluginReasons["Mai-with-u.qq-adapter"] == "" {
		t.Fatalf("迁移失败原因应进入阻止列表: %#v", result.BlockedPluginReasons)
	}
}
